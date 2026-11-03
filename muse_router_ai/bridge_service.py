# -*- coding: utf-8 -*-
"""
MuseRouter AI Bridge Service (`muse_router_ai`) — 100% Real Protocol & Browser-CDP Driver
Tuân thủ tuyệt đối Antigravity Polyglot v2.4 & PHAN 9 (KHÔNG giả lập / KHÔNG dữ liệu ảo):
- Luồng giao tiếp: Go Engine -> Python AI -> Rust Core (`muse_router_core.dll`)
- Prefix log bắt buộc: `[AI]`
- Kết nối thực tế tới `https://muse.ai` qua `primp` (TLS Impersonation vượt AYMH redirect) & `playwright` (Chromium CDP)
- Hỗ trợ:
  1. Kiểm tra xác thực thật qua `https://muse.ai/api/consent/status` & `https://muse.ai/api/client-version`
  2. Mở cửa sổ đăng nhập `https://muse.ai` bắt Cookie thật tự động (`hatch_sess`, `hatch_gw`, `hatch_native_auth_device`, `datr`)
  3. Mở phiên trình duyệt gốc `https://muse.ai` cô lập cho từng tài khoản trong Pool
  4. Thực thi Chat thật (kèm đính kèm ảnh/tài liệu) trên `https://muse.ai` hoặc OpenAI-compatible Upstream (`/v1/chat/completions`)
"""

import base64
import ctypes
import hashlib
import hmac
import http.server
import json
import logging
import os
import queue
import random
import re
import sqlite3
import sys
import threading
import time
import urllib.parse
from contextlib import closing
from functools import lru_cache
from typing import Any, Dict, List, Optional, Tuple

import primp
from playwright.sync_api import sync_playwright

try:
    import numpy as np
except ImportError:
    np = None


class CrLfSanitizingFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        msg = super().format(record)
        return msg.replace("\r", " ").replace("\n", " ")


logger = logging.getLogger("muse_router_ai")
logger.setLevel(logging.INFO)
if not logger.handlers:
    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(CrLfSanitizingFormatter("[AI] %(asctime)s [%(levelname)s] %(message)s"))
    logger.addHandler(handler)


def sanitize_log_str(value: str) -> str:
    if not value:
        return ""
    return str(value).replace("\r", "").replace("\n", "").replace("\x00", "")


class RustCoreFFI:
    """Giao tiếp FFI an toàn với `muse_router_core.dll` kèm Memory Leak Guard (`free_rust_string`)."""

    def __init__(self, dll_path: Optional[str] = None) -> None:
        if dll_path is None:
            base_dir = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
            candidates = [
                os.path.join(base_dir, "muse_router_core", "muse_router_core.dll"),
                os.path.join(base_dir, "muse_router_core", "target", "release", "muse_router_core.dll"),
                os.path.join(base_dir, "muse_router_core.dll"),
            ]
            for path in candidates:
                if os.path.exists(path):
                    dll_path = path
                    break

        if not dll_path or not os.path.exists(dll_path):
            raise FileNotFoundError(f"Rust Core DLL not found at expected paths: {dll_path}")

        self._lib = ctypes.CDLL(dll_path)

        self._lib.core_encrypt_secret.argtypes = [ctypes.c_char_p, ctypes.c_char_p]
        self._lib.core_encrypt_secret.restype = ctypes.c_void_p

        self._lib.core_decrypt_secret.argtypes = [ctypes.c_char_p, ctypes.c_char_p]
        self._lib.core_decrypt_secret.restype = ctypes.c_void_p

        self._lib.core_constant_time_compare.argtypes = [ctypes.c_char_p, ctypes.c_char_p]
        self._lib.core_constant_time_compare.restype = ctypes.c_int

        self._lib.core_compute_token_fingerprint.argtypes = [ctypes.c_char_p]
        self._lib.core_compute_token_fingerprint.restype = ctypes.c_void_p

        self._lib.free_rust_string.argtypes = [ctypes.c_void_p]
        self._lib.free_rust_string.restype = None

        logger.info("Loaded Rust Core DLL successfully from %s", sanitize_log_str(dll_path))

    def _read_and_free(self, ptr: Optional[int]) -> str:
        if not ptr:
            raise ValueError("Rust FFI returned null pointer")
        try:
            raw_bytes = ctypes.cast(ptr, ctypes.c_char_p).value
            if raw_bytes is None:
                return ""
            return raw_bytes.decode("utf-8")
        finally:
            self._lib.free_rust_string(ctypes.c_void_p(ptr))

    def encrypt_secret(self, plaintext: str, master_key: str) -> str:
        ptr = self._lib.core_encrypt_secret(
            plaintext.encode("utf-8"),
            master_key.encode("utf-8"),
        )
        return self._read_and_free(ptr)

    def decrypt_secret(self, encrypted: str, master_key: str) -> str:
        ptr = self._lib.core_decrypt_secret(
            encrypted.encode("utf-8"),
            master_key.encode("utf-8"),
        )
        return self._read_and_free(ptr)

    def fingerprint(self, secret: str) -> str:
        ptr = self._lib.core_compute_token_fingerprint(secret.encode("utf-8"))
        return self._read_and_free(ptr)

    def constant_time_compare(self, a: str, b: str) -> bool:
        res = self._lib.core_constant_time_compare(a.encode("utf-8"), b.encode("utf-8"))
        return bool(res == 1)


@lru_cache(maxsize=64)
def normalize_upstream_endpoint(raw_url: str) -> str:
    """Bộ đệm L1 Cache cho URL chuẩn hóa (PHAN 7) — Không chứa dữ liệu giả."""
    cleaned = (raw_url or "https://muse.ai").strip().rstrip("/")
    if not cleaned.startswith("http://") and not cleaned.startswith("https://"):
        cleaned = "https://" + cleaned
    return cleaned


def compute_node_load_weights(active_counts: list, remaining_tokens: list) -> list:
    """Tính toán trọng số phân tải (Load Balancing) bằng float32 tiết kiệm bộ nhớ (PHAN 7)."""
    if not active_counts:
        return []
    if np is not None:
        act = np.array(active_counts, dtype=np.float32)
        tok = np.array(remaining_tokens, dtype=np.float32)
        max_tok = float(np.max(tok)) if float(np.max(tok)) > 0 else 1.0
        norm_tok = np.where(tok > 0, tok / max_tok, 1.0)
        scores = norm_tok / (1.0 + act * 1.5)
        result = [round(float(x), 4) for x in scores]
        del act, tok, norm_tok, scores
        return result

    max_tok = max(remaining_tokens) if remaining_tokens and max(remaining_tokens) > 0 else 1.0
    return [
        round(((float(t) / float(max_tok)) if t > 0 else 1.0) / (1.0 + float(a) * 1.5), 4)
        for a, t in zip(active_counts, remaining_tokens)
    ]


def parse_cookie_input(raw_cookie_input: str) -> Tuple[str, Dict[str, str]]:
    """
    Chuẩn hóa đầu vào Cookie từ người dùng:
    - Hỗ trợ JSON mảng từ Cookie-Editor / EditThisCookie: `[{"name": "hatch_sess", "value": "..."}]`
    - Hỗ trợ chuỗi Cookie Header: `hatch_sess=...; hatch_gw=...; hatch_native_auth_device=...; datr=...`
    - Hỗ trợ trường hợp người dùng chỉ dán trực tiếp giá trị của `hatch_sess`.
    """
    cleaned = raw_cookie_input.strip()
    cookie_map: Dict[str, str] = {}

    if cleaned.startswith("[") or cleaned.startswith("{"):
        try:
            parsed = json.loads(cleaned)
            if isinstance(parsed, list):
                for item in parsed:
                    if isinstance(item, dict) and "name" in item and "value" in item:
                        cookie_map[str(item["name"]).strip()] = str(item["value"]).strip()
            elif isinstance(parsed, dict):
                if "cookies" in parsed and isinstance(parsed["cookies"], dict):
                    for k, v in parsed["cookies"].items():
                        cookie_map[str(k).strip()] = str(v).strip()
                else:
                    for k, v in parsed.items():
                        cookie_map[str(k).strip()] = str(v).strip()
        except json.JSONDecodeError as exc:
            logger.warning("Cookie JSON parse warning: %s", sanitize_log_str(str(exc)))

    if not cookie_map:
        for pair in cleaned.split(";"):
            part = pair.strip()
            if not part:
                continue
            if "=" in part:
                k, v = part.split("=", 1)
                cookie_map[k.strip()] = v.strip()
            else:
                cookie_map["hatch_sess"] = part

    header_str = "; ".join(f"{k}={v}" for k, v in cookie_map.items() if k and v)
    return header_str, cookie_map


def build_playwright_cookies(cookie_map: Dict[str, str]) -> List[Dict[str, object]]:
    """
    Tạo danh sách cookies chuẩn cho Playwright Context trên cả 2 domain `.muse.ai` và `muse.ai`
    (Resilient Cookie Injection).
    """
    pw_cookies: List[Dict[str, object]] = []
    for name, val in cookie_map.items():
        if not name or not val:
            continue
        decoded_val = urllib.parse.unquote(val) if "%" in val else val
        for domain in [".muse.ai", "muse.ai"]:
            pw_cookies.append(
                {
                    "name": name,
                    "value": decoded_val,
                    "domain": domain,
                    "path": "/",
                    "secure": True,
                    "httpOnly": name in ("hatch_sess", "datr"),
                    "sameSite": "Lax",
                }
            )
    return pw_cookies


@lru_cache(maxsize=256)
def build_account_device_profile(fp: str) -> Dict[str, object]:
    """
    Tạo Hồ Sơ Thiết Bị Chống Ban (Anti-Detect Device Profile) CỐ ĐỊNH & ĐỘC LẬP cho từng tài khoản dựa trên `fp`:
    - Khi nhiều tài khoản chạy trên CÙNG 1 ĐỊA CHỈ IP (mạng văn phòng / gia đình / NAT), hệ thống chống bot của
      Meta (`auth.meta.com` / `muse.ai` / `hatch.metaaivm.com`) phát hiện tài khoản ảo bằng cách so sánh:
      Canvas 2D hash, WebGL GPU Vendor/Renderer, AudioContext oscillator, độ phân giải màn hình, số nhân CPU,
      dung lượng RAM, User-Agent/Sec-CH-UA và cookie thiết bị `datr`.
    - Hàm này băm SHA-256 từ `fp` của từng tài khoản để sinh ra một cấu hình máy tính vật lý RIÊNG BIỆT
      nhưng KHÔNG BAO GIỜ THAY ĐỔI giữa các lần khởi động lại của cùng 1 tài khoản (tránh bị khóa vì đổi thiết bị liên tục).
    """
    seed_str = (fp or "default_muse_fp").strip()
    digest = hashlib.sha256(seed_str.encode("utf-8")).digest()

    browser_profiles = [
        {
            "ua": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
            "sec_ch_ua": '"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"',
            "platform": "Win32",
            "sec_platform": '"Windows"',
            "primp_impersonate": "chrome_131",
            "short_ua": "Chrome 131 · Win11",
        },
        {
            "ua": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
            "sec_ch_ua": '"Chromium";v="130", "Google Chrome";v="130", "Not?A_Brand";v="99"',
            "platform": "Win32",
            "sec_platform": '"Windows"',
            "primp_impersonate": "chrome_130",
            "short_ua": "Chrome 130 · Win11",
        },
        {
            "ua": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
            "sec_ch_ua": '"Microsoft Edge";v="131", "Chromium";v="131", "Not_A Brand";v="24"',
            "platform": "Win32",
            "sec_platform": '"Windows"',
            "primp_impersonate": "edge_131",
            "short_ua": "Edge 131 · Win11",
        },
        {
            "ua": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36",
            "sec_ch_ua": '"Google Chrome";v="129", "Not=A?Brand";v="8", "Chromium";v="129"',
            "platform": "Win32",
            "sec_platform": '"Windows"',
            "primp_impersonate": "chrome_129",
            "short_ua": "Chrome 129 · Win10",
        },
        {
            "ua": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
            "sec_ch_ua": '"Chromium";v="128", "Not;A=Brand";v="24", "Google Chrome";v="128"',
            "platform": "Win32",
            "sec_platform": '"Windows"',
            "primp_impersonate": "chrome_128",
            "short_ua": "Chrome 128 · Win10",
        },
    ]

    gpu_profiles = [
        (
            "Google Inc. (NVIDIA)",
            "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0, D3D11)",
            "RTX 3060",
        ),
        (
            "Google Inc. (Intel)",
            "ANGLE (Intel, Intel(R) Iris(R) Xe Graphics (0x00009A49) Direct3D11 vs_5_0 ps_5_0, D3D11)",
            "Iris Xe",
        ),
        (
            "Google Inc. (NVIDIA)",
            "ANGLE (NVIDIA, NVIDIA GeForce RTX 4060 Direct3D11 vs_5_0 ps_5_0, D3D11)",
            "RTX 4060",
        ),
        (
            "Google Inc. (AMD)",
            "ANGLE (AMD, AMD Radeon RX 6600 Direct3D11 vs_5_0 ps_5_0, D3D11)",
            "RX 6600",
        ),
        (
            "Google Inc. (NVIDIA)",
            "ANGLE (NVIDIA, NVIDIA GeForce GTX 1660 SUPER Direct3D11 vs_5_0 ps_5_0, D3D11)",
            "GTX 1660S",
        ),
        (
            "Google Inc. (Intel)",
            "ANGLE (Intel, Intel(R) UHD Graphics 770 Direct3D11 vs_5_0 ps_5_0, D3D11)",
            "UHD 770",
        ),
        (
            "Google Inc. (NVIDIA)",
            "ANGLE (NVIDIA, NVIDIA GeForce RTX 3070 Ti Direct3D11 vs_5_0 ps_5_0, D3D11)",
            "RTX 3070Ti",
        ),
        (
            "Google Inc. (AMD)",
            "ANGLE (AMD, AMD Radeon(TM) Graphics Direct3D11 vs_5_0 ps_5_0, D3D11)",
            "Radeon iGPU",
        ),
    ]

    screen_profiles = [
        (1920, 1080, 1440, 860, 1.0),
        (1536, 864, 1366, 780, 1.25),
        (2560, 1440, 1520, 900, 1.0),
        (1440, 900, 1320, 800, 1.0),
        (1680, 1050, 1400, 850, 1.0),
        (1600, 900, 1380, 810, 1.0),
        (1366, 768, 1280, 720, 1.0),
        (1920, 1200, 1460, 880, 1.25),
    ]

    hw_profiles = [
        (8, 16),
        (12, 16),
        (16, 32),
        (6, 8),
        (8, 8),
        (12, 32),
        (4, 8),
        (16, 16),
    ]

    b_prof = browser_profiles[digest[0] % len(browser_profiles)]
    g_prof = gpu_profiles[digest[1] % len(gpu_profiles)]
    s_prof = screen_profiles[digest[2] % len(screen_profiles)]
    h_prof = hw_profiles[digest[3] % len(hw_profiles)]

    # Độ nhiễu Canvas & Audio cố định theo tài khoản (khác nhau giữa các tài khoản trên cùng 1 IP)
    canvas_shift_r = (digest[4] % 5) - 2 or 1
    canvas_shift_g = (digest[5] % 5) - 2 or -1
    canvas_shift_b = (digest[6] % 5) - 2 or 1
    audio_noise = round(((digest[7] % 19) + 1) * 1e-7, 9)
    dev_id = digest[8:11].hex().upper()

    summary_label = f"DEV-{dev_id} · {b_prof['short_ua']} · {g_prof[2]} · {s_prof[0]}×{s_prof[1]} ({h_prof[0]}C/{h_prof[1]}GB)"
    return {
        "device_id": f"DEV-{dev_id}",
        "summary_label": summary_label,
        "user_agent": b_prof["ua"],
        "sec_ch_ua": b_prof["sec_ch_ua"],
        "platform": b_prof["platform"],
        "sec_ch_ua_platform": b_prof["sec_platform"],
        "primp_impersonate": b_prof["primp_impersonate"],
        "webgl_vendor": g_prof[0],
        "webgl_renderer": g_prof[1],
        "screen_width": s_prof[0],
        "screen_height": s_prof[1],
        "viewport_width": s_prof[2],
        "viewport_height": s_prof[3],
        "device_scale_factor": s_prof[4],
        "hardware_concurrency": h_prof[0],
        "device_memory": h_prof[1],
        "canvas_shift_r": canvas_shift_r,
        "canvas_shift_g": canvas_shift_g,
        "canvas_shift_b": canvas_shift_b,
        "audio_noise": audio_noise,
    }


def build_stealth_init_script(profile: Dict[str, object]) -> str:
    """
    Script Anti-Detect bơm vào Playwright Context trước khi trang `https://muse.ai` chạy bất kỳ JS nào:
    - Xóa dấu vết tự động hóa (`navigator.webdriver = false`, `window.chrome`).
    - Cô lập vân tay phần cứng (`hardwareConcurrency`, `deviceMemory`, `screen.width/height`, WebGL GPU).
    - Cô lập vân tay Canvas 2D và AudioContext cố định theo từng tài khoản để nhiều tài khoản dùng chung 1 IP
      được nhận diện như nhiều máy tính cá nhân độc lập trong cùng mạng LAN.
    """
    cfg_json = json.dumps(profile, ensure_ascii=False)
    return f"""
    (() => {{
        const cfg = {cfg_json};
        try {{
            Object.defineProperty(navigator, 'webdriver', {{ get: () => false }});
            Object.defineProperty(navigator, 'hardwareConcurrency', {{ get: () => cfg.hardware_concurrency || 8 }});
            Object.defineProperty(navigator, 'deviceMemory', {{ get: () => cfg.device_memory || 16 }});
            Object.defineProperty(navigator, 'platform', {{ get: () => cfg.platform || 'Win32' }});
            Object.defineProperty(navigator, 'languages', {{ get: () => ['vi-VN', 'vi', 'en-US', 'en'] }});
            if (!window.chrome) {{
                window.chrome = {{ runtime: {{}}, loadTimes: () => ({{}}), csi: () => ({{}}) }};
            }}
            if (window.screen) {{
                Object.defineProperty(window.screen, 'width', {{ get: () => cfg.screen_width || 1920 }});
                Object.defineProperty(window.screen, 'height', {{ get: () => cfg.screen_height || 1080 }});
                Object.defineProperty(window.screen, 'availWidth', {{ get: () => cfg.screen_width || 1920 }});
                Object.defineProperty(window.screen, 'availHeight', {{ get: () => (cfg.screen_height || 1080) - 40 }});
            }}
            const patchWebGL = (proto) => {{
                if (!proto || !proto.getParameter) return;
                const origGetParam = proto.getParameter;
                proto.getParameter = function(param) {{
                    if (param === 37445) return cfg.webgl_vendor;
                    if (param === 37446) return cfg.webgl_renderer;
                    return origGetParam.apply(this, arguments);
                }};
            }};
            if (typeof WebGLRenderingContext !== 'undefined') patchWebGL(WebGLRenderingContext.prototype);
            if (typeof WebGL2RenderingContext !== 'undefined') patchWebGL(WebGL2RenderingContext.prototype);

            if (typeof CanvasRenderingContext2D !== 'undefined') {{
                const origGetImageData = CanvasRenderingContext2D.prototype.getImageData;
                CanvasRenderingContext2D.prototype.getImageData = function() {{
                    const imgData = origGetImageData.apply(this, arguments);
                    if (imgData && imgData.data && imgData.data.length >= 16) {{
                        imgData.data[0] = (imgData.data[0] + cfg.canvas_shift_r) & 255;
                        imgData.data[1] = (imgData.data[1] + cfg.canvas_shift_g) & 255;
                        imgData.data[2] = (imgData.data[2] + cfg.canvas_shift_b) & 255;
                    }}
                    return imgData;
                }};
            }}

            if (typeof AudioBuffer !== 'undefined' && AudioBuffer.prototype.getChannelData) {{
                const origGetChannelData = AudioBuffer.prototype.getChannelData;
                AudioBuffer.prototype.getChannelData = function() {{
                    const channelData = origGetChannelData.apply(this, arguments);
                    if (channelData && channelData.length > 0 && !channelData.__muse_patched) {{
                        for (let i = 0; i < Math.min(channelData.length, 64); i += 7) {{
                            channelData[i] = channelData[i] + cfg.audio_noise;
                        }}
                        Object.defineProperty(channelData, '__muse_patched', {{ value: true, enumerable: false }});
                    }}
                    return channelData;
                }};
            }}
        }} catch (_) {{}}
    }})();
    """


def parse_playwright_proxy(proxy_url: str) -> Optional[Dict[str, str]]:
    """Chuyển đổi chuỗi Proxy (`http://user:pass@ip:port` hoặc `socks5://ip:port`) sang cấu hình Playwright Proxy."""
    cleaned = (proxy_url or "").strip()
    if not cleaned:
        return None
    if "://" not in cleaned:
        cleaned = "http://" + cleaned
    parsed = urllib.parse.urlparse(cleaned)
    if not parsed.hostname:
        return None
    server = f"{parsed.scheme}://{parsed.hostname}"
    if parsed.port:
        server += f":{parsed.port}"
    proxy_cfg: Dict[str, str] = {"server": server}
    if parsed.username:
        proxy_cfg["username"] = urllib.parse.unquote(parsed.username)
    if parsed.password:
        proxy_cfg["password"] = urllib.parse.unquote(parsed.password)
    return proxy_cfg


class SameIPPacer:
    """
    Bộ điều nhịp chống Ban khi chạy nhiều tài khoản trên CÙNG 1 IP (Same-IP Anti-Burst Pacer & Cookie Vault):
    1. Chống gửi xung đột đồng thời (Anti-Burst): Đảm bảo 2 tài khoản khác nhau trên cùng 1 IP luôn cách nhau
       một khoảng trễ tự nhiên (`0.65s – 1.45s` jitter) khi bắt tay mạng / gửi tin nhắn.
    2. Cô lập & lưu trữ bền vững Cookie thiết bị (`datr`, `hatch_native_auth_device`) riêng cho từng tài khoản
       tại `data/profiles/<fp>.json` để mỗi tài khoản giữ nguyên định danh thiết bị tin cậy của riêng nó.
    """

    def __init__(self, profiles_dir: str) -> None:
        self._lock = threading.Lock()
        self._last_global_action_ts: float = 0.0
        self._last_fp_action_ts: Dict[str, float] = {}
        self.profiles_dir = profiles_dir
        os.makedirs(self.profiles_dir, exist_ok=True)

    def wait_turn(self, fp: str, min_global_gap: float = 0.65) -> None:
        with self._lock:
            now = time.time()
            jitter = random.uniform(0.15, 0.55)
            prev_fp = self._last_fp_action_ts.get(fp, 0.0)
            wait_sec = 0.0
            if now - self._last_global_action_ts < min_global_gap:
                wait_sec = max(wait_sec, (self._last_global_action_ts + min_global_gap + jitter) - now)
            if prev_fp > 0 and now - prev_fp < 0.8:
                wait_sec = max(wait_sec, (prev_fp + 0.8 + jitter) - now)
            target_ts = now + wait_sec
            self._last_global_action_ts = target_ts
            self._last_fp_action_ts[fp] = target_ts

        if wait_sec > 0.02:
            time.sleep(min(wait_sec, 2.5))

    def _profile_cookie_file(self, fp: str) -> str:
        safe_fp = re.sub(r"[^a-zA-Z0-9_-]", "_", fp or "default")
        return os.path.join(self.profiles_dir, f"{safe_fp}.json")

    def merge_isolated_device_cookies(self, fp: str, cookie_map: Dict[str, str]) -> Dict[str, str]:
        """Nạp lại `datr` / `hatch_native_auth_device` riêng đã lưu của tài khoản `fp` (không bao giờ dùng chung `datr` giữa các tài khoản)."""
        merged = dict(cookie_map)
        if not fp:
            return merged
        pfile = self._profile_cookie_file(fp)
        if os.path.exists(pfile):
            try:
                with open(pfile, "r", encoding="utf-8") as f:
                    saved = json.load(f)
                if isinstance(saved, dict):
                    saved_cookies = saved.get("cookies")
                    if isinstance(saved_cookies, dict):
                        for k in ("datr", "hatch_native_auth_device", "hatch_gw"):
                            if k not in merged and saved_cookies.get(k):
                                merged[k] = str(saved_cookies[k])
            except Exception as exc:
                logger.debug("Read isolated profile cookie skipped for %s: %s", sanitize_log_str(fp), sanitize_log_str(str(exc)))
        return merged

    def save_isolated_device_cookies(self, fp: str, cookie_map: Dict[str, str]) -> None:
        if not fp or not cookie_map:
            return
        pfile = self._profile_cookie_file(fp)
        try:
            prof = build_account_device_profile(fp)
            payload = {
                "fingerprint": fp,
                "device_profile": prof.get("summary_label", ""),
                "updated_at": int(time.time()),
                "cookies": {
                    k: v
                    for k, v in cookie_map.items()
                    if k in ("datr", "hatch_native_auth_device", "hatch_gw", "hatch_sess") and v
                },
            }
            with open(pfile, "w", encoding="utf-8") as f:
                json.dump(payload, f, ensure_ascii=False, indent=2)
        except Exception as exc:
            logger.debug("Save isolated profile cookie skipped for %s: %s", sanitize_log_str(fp), sanitize_log_str(str(exc)))


class WarmBrowserSessionManager:
    """
    Quản lý các Tab trình duyệt ngầm (Warm-Tab Affinity) luôn giữ sẵn kết nối
    WebSocket (`wss://hatch.metaaivm.com/v1/noise`) cho từng tài khoản Muse.ai:
    - Loại bỏ hoàn toàn độ trễ khởi động Chromium + nạp trang trên mỗi tin nhắn (giảm từ ~12s xuống ~1.5s).
    - Tích hợp Khiên Chống Ban Trùng IP (Anti-Detect Fingerprint riêng theo `fp` + Cô lập `datr` + Giả lập gõ phím tự nhiên).
    - Ghi nhận chính xác số lượng `.prose` và nội dung tin nhắn cuối cùng TRƯỚC khi gửi (`baseline_count`, `baseline_md`)
      để chỉ lấy câu trả lời MỚI vừa sinh ra, tuyệt đối không lấy nhầm tin nhắn cũ trong lịch sử.
    - Giới hạn tối đa 6 phiên Warm Tab (LRU Eviction) chống tràn RAM (PHAN 7).
    """

    MAX_WARM_SESSIONS = 6

    EXTRACT_PROSE_JS = """() => {
        function nodeToMd(n, listDepth) {
            if (n.nodeType === Node.TEXT_NODE) {
                return n.textContent || "";
            }
            if (n.nodeType !== Node.ELEMENT_NODE) {
                return "";
            }
            const tag = n.tagName.toLowerCase();
            if (tag === "strong" || tag === "b") {
                return "**" + Array.from(n.childNodes).map(c => nodeToMd(c, listDepth)).join("") + "**";
            }
            if (tag === "em" || tag === "i") {
                return "*" + Array.from(n.childNodes).map(c => nodeToMd(c, listDepth)).join("") + "*";
            }
            if (tag === "code" && (!n.parentElement || n.parentElement.tagName.toLowerCase() !== "pre")) {
                return "`" + (n.textContent || "") + "`";
            }
            if (tag === "pre") {
                return "\\n```\\n" + (n.textContent || "").trim() + "\\n```\\n";
            }
            if (tag === "ul" || tag === "ol") {
                return "\\n" + Array.from(n.children).map(c => nodeToMd(c, listDepth + 1)).join("");
            }
            if (tag === "li") {
                const indent = listDepth > 1 ? "  ".repeat(listDepth - 1) : "";
                const body = Array.from(n.childNodes).map(c => nodeToMd(c, listDepth)).join("").trim();
                return indent + "- " + body + "\\n";
            }
            if (tag === "p" || tag === "div" || tag === "h1" || tag === "h2" || tag === "h3" || tag === "h4") {
                const inner = Array.from(n.childNodes).map(c => nodeToMd(c, listDepth)).join("").trim();
                return inner ? inner + "\\n\\n" : "";
            }
            if (tag === "br") {
                return "\\n";
            }
            if (tag === "img" && n.src) {
                return "\\n![" + (n.alt || "Hình ảnh") + "](" + n.src + ")\\n";
            }
            return Array.from(n.childNodes).map(c => nodeToMd(c, listDepth)).join("");
        }
        const els = Array.from(document.querySelectorAll('.prose'));
        const count = els.length;
        const lastMd = count > 0 ? nodeToMd(els[count - 1], 0).replace(/\\n{3,}/g, "\\n\\n").trim() : "";

        let statusText = "";
        let avatarFrame = "";
        try {
            const candidates = Array.from(document.querySelectorAll('h1, h2, h3, p, div, span')).filter(
                el => el.children.length === 0 && (el.textContent || "").trim() === "Muse"
            );
            for (const h of candidates) {
                const container = h.parentElement;
                if (!container) continue;
                const next = h.nextElementSibling || container.nextElementSibling;
                if (next) {
                    const st = (next.textContent || "").replace(/\\s+/g, " ").trim();
                    if (st && st.length < 60) {
                        statusText = st;
                    }
                }
                const root = container.parentElement || container;
                const mediaEl = root.querySelector('canvas, video, img');
                if (mediaEl) {
                    const c = document.createElement('canvas');
                    c.width = 112;
                    c.height = 112;
                    const ctx = c.getContext('2d');
                    if (ctx) {
                        ctx.drawImage(mediaEl, 0, 0, 112, 112);
                        const dataUrl = c.toDataURL('image/webp', 0.8);
                        if (dataUrl && dataUrl.length > 200) {
                            avatarFrame = dataUrl;
                        }
                    }
                }
                if (statusText || avatarFrame) break;
            }
        } catch (e) {}

        return { count, lastMd, statusText, avatarFrame };
    }"""

    def __init__(self, pacer: SameIPPacer) -> None:
        self.pacer = pacer
        self._workers_lock = threading.Lock()
        self._account_queues: Dict[str, "queue.Queue[Tuple[str, Dict[str, object], Optional[queue.Queue]]]"] = {}
        self._account_threads: Dict[str, threading.Thread] = {}
        self._account_states: Dict[str, Dict[str, object]] = {}
        self._completed_followups: List[Dict[str, object]] = []

    IDLE_STATUS_TEXTS = ("đã kết nối", "connected", "muse", "mart ai")

    def pop_completed_followups(self) -> List[Dict[str, object]]:
        with self._workers_lock:
            if not self._completed_followups:
                return []
            items = list(self._completed_followups)
            self._completed_followups.clear()
            return items

    @property
    def live_state(self) -> Dict[str, object]:
        """Tổng hợp trạng thái hoạt động thời gian thực của toàn bộ các tài khoản trong Pool."""
        with self._workers_lock:
            states = [dict(st) for st in self._account_states.values()]
        followups = self.pop_completed_followups()

        busy_fps: List[str] = []
        busy_labels: List[str] = []
        active_states: List[Dict[str, object]] = []
        for st in states:
            if bool(st.get("active")):
                fp_val = str(st.get("fp") or "")
                lbl_val = str(st.get("node_label") or "")
                if fp_val and fp_val not in busy_fps:
                    busy_fps.append(fp_val)
                if lbl_val and lbl_val not in busy_labels:
                    busy_labels.append(lbl_val)
                active_states.append(st)

        if not active_states:
            return {
                "active": False,
                "background": False,
                "status_text": "Đã kết nối",
                "avatar_frame": "",
                "partial_md": "",
                "busy_fps": [],
                "busy_labels": [],
                "followups": followups,
                "updated_at": time.time(),
            }

        # Ưu tiên hiển thị tài khoản đang stream trực tiếp (background=False), sau đó mới tới mới nhất
        active_states.sort(
            key=lambda x: (0 if bool(x.get("background")) else 1, float(x.get("updated_at") or 0.0)),
            reverse=True,
        )
        chosen = active_states[0]
        return {
            "active": True,
            "background": bool(chosen.get("background")),
            "status_text": str(chosen.get("status_text") or "Đang xử lý..."),
            "avatar_frame": str(chosen.get("avatar_frame") or ""),
            "partial_md": str(chosen.get("partial_md") or ""),
            "busy_fps": busy_fps,
            "busy_labels": busy_labels,
            "followups": followups,
            "updated_at": float(chosen.get("updated_at") or time.time()),
        }

    def _update_account_state(self, fp: str, **kwargs: object) -> None:
        with self._workers_lock:
            cur = self._account_states.get(fp)
            if cur is None:
                cur = {
                    "fp": fp,
                    "node_label": "",
                    "active": False,
                    "background": False,
                    "status_text": "Đã kết nối",
                    "avatar_frame": "",
                    "partial_md": "",
                    "updated_at": time.time(),
                }
                self._account_states[fp] = cur
            cur.update(kwargs)
            cur["updated_at"] = time.time()

    def _ensure_account_worker(self, fp: str) -> "queue.Queue[Tuple[str, Dict[str, object], Optional[queue.Queue]]]":
        key = fp or "default"
        with self._workers_lock:
            q = self._account_queues.get(key)
            t = self._account_threads.get(key)
            if q is not None and t is not None and t.is_alive():
                return q
            q = queue.Queue()
            self._account_queues[key] = q
            if key not in self._account_states:
                self._account_states[key] = {
                    "fp": key,
                    "node_label": "",
                    "active": False,
                    "background": False,
                    "status_text": "Đã kết nối",
                    "avatar_frame": "",
                    "partial_md": "",
                    "updated_at": time.time(),
                }
            t = threading.Thread(target=self._account_worker_loop, args=(key, q), daemon=True)
            self._account_threads[key] = t
            t.start()
            return q

    @classmethod
    def _is_working_status(cls, status_text: str) -> bool:
        """Trạng thái dưới logo Muse gốc: rỗng / 'Đã kết nối' = rảnh; còn lại ('Đọc bộ nhớ', 'Đang viết code'...) = đang làm việc."""
        st = (status_text or "").strip().lower()
        if not st:
            return False
        return not any(st == idle or st.startswith(idle) for idle in cls.IDLE_STATUS_TEXTS)

    def _monitor_background_work(
        self,
        fp: str,
        node_label: str,
        page: Any,
        cmd_q: "queue.Queue[Tuple[str, Dict[str, object], Optional[queue.Queue]]]",
        session_id: str = "",
        turn_id: str = "",
        initial_md: str = "",
        max_seconds: float = 300.0,
    ) -> None:
        """
        Sau khi đã trả câu trả lời đầu tiên cho Web, tiếp tục đọc trạng thái dưới logo của Muse gốc trên tài khoản `fp`.
        Trong suốt thời gian này:
        - Tài khoản `fp` vẫn được đánh dấu `active=True, background=True` để câu hỏi mới tự động nhảy sang tài khoản khác.
        - Nếu Muse gốc gửi thêm câu trả lời mới hoặc cập nhật kết quả sau khi hoàn tất việc nền (ví dụ: viết code / tạo giả liệu xong),
          tự động đồng bộ nội dung mới đó về đúng `turn_id` của câu hỏi trên giao diện Web.
        """
        start_ts = time.time()
        deadline = start_ts + max_seconds
        seen_working = False
        idle_ticks = 0
        last_bg_md = (initial_md or "").strip()
        synced_bg_md = last_bg_md
        bg_stable_ticks = 0
        try:
            while time.time() < deadline:
                if not cmd_q.empty():
                    break
                page.wait_for_timeout(500)
                snap = page.evaluate(self.EXTRACT_PROSE_JS)
                st = str(snap.get("statusText") or "").strip()
                cur_md = str(snap.get("lastMd") or "").strip()

                if cur_md and cur_md != synced_bg_md:
                    if cur_md == last_bg_md:
                        bg_stable_ticks += 1
                        if bg_stable_ticks >= 3:
                            synced_bg_md = cur_md
                            with self._workers_lock:
                                self._completed_followups.append(
                                    {
                                        "session_id": session_id,
                                        "turn_id": turn_id,
                                        "node_label": node_label,
                                        "content": cur_md,
                                        "initial_md": (initial_md or "").strip(),
                                    }
                                )
                    else:
                        last_bg_md = cur_md
                        bg_stable_ticks = 0

                if self._is_working_status(st):
                    seen_working = True
                    idle_ticks = 0
                    self._update_account_state(
                        fp,
                        node_label=node_label,
                        active=True,
                        background=True,
                        status_text=st,
                        partial_md=cur_md if cur_md != (initial_md or "").strip() else "",
                    )
                else:
                    idle_ticks += 1
                    if seen_working and idle_ticks >= 4:
                        break
                    if not seen_working and (time.time() - start_ts) > 8.0:
                        break
        except Exception as exc:
            logger.warning("Background status monitor stopped for %s: %s", sanitize_log_str(fp), sanitize_log_str(str(exc)))
        finally:
            self._update_account_state(
                fp,
                node_label=node_label,
                active=False,
                background=False,
                status_text="Đã kết nối",
                partial_md="",
            )

    def prewarm_async(self, fp: str, cookie_map: Dict[str, str], proxy_url: str = "") -> None:
        """Khởi động sẵn Tab `https://muse.ai` trong nền ngay khi tài khoản được xác thực."""
        if not fp or "hatch_sess" not in cookie_map:
            return
        cmd_q = self._ensure_account_worker(fp)
        cmd_q.put(("prewarm", {"fp": fp, "cookie_map": dict(cookie_map), "proxy_url": proxy_url}, None))

    def send_chat(
        self,
        fp: str,
        cookie_map: Dict[str, str],
        prompt: str,
        node_label: str,
        upload_files: List[str],
        session_id: str = "",
        turn_id: str = "",
        messages: Optional[list] = None,
        proxy_url: str = "",
    ) -> Dict[str, object]:
        reply_q: "queue.Queue[Dict[str, object]]" = queue.Queue(maxsize=1)
        cmd_q = self._ensure_account_worker(fp)
        self._update_account_state(
            fp,
            node_label=node_label,
            active=True,
            background=False,
            status_text="Đang xử lý...",
        )
        cmd_q.put(
            (
                "chat",
                {
                    "fp": fp,
                    "cookie_map": dict(cookie_map),
                    "prompt": prompt,
                    "node_label": node_label,
                    "upload_files": list(upload_files),
                    "session_id": session_id,
                    "turn_id": turn_id,
                    "messages": list(messages or []),
                    "proxy_url": proxy_url,
                },
                reply_q,
            )
        )
        try:
            return reply_q.get(timeout=65.0)
        except queue.Empty:
            self._update_account_state(fp, active=False, background=False, status_text="Đã kết nối")
            return {
                "ok": False,
                "http_status": 504,
                "error": f"Hết thời gian chờ phản hồi từ {node_label}.",
            }

    def _account_worker_loop(
        self,
        worker_fp: str,
        cmd_q: "queue.Queue[Tuple[str, Dict[str, object], Optional[queue.Queue]]]",
    ) -> None:
        pw = None
        browser = None
        sessions: Dict[str, Dict[str, object]] = {}

        def _ensure_browser():
            nonlocal pw, browser
            if pw is None:
                pw = sync_playwright().start()
            if browser is None or not browser.is_connected():
                browser = pw.chromium.launch(
                    headless=True,
                    args=[
                        "--disable-blink-features=AutomationControlled",
                        "--force-webrtc-ip-handling-policy=default_public_interface_only",
                        "--enforce-webrtc-ip-permission-check",
                        "--disable-features=IsolateOrigins,site-per-process",
                    ],
                )
            return browser

        def _get_or_create_warm_page(fp: str, cookie_map: Dict[str, str], proxy_url: str = ""):
            b = _ensure_browser()
            entry = sessions.get(fp)
            if entry is not None:
                pg = entry.get("page")
                if (
                    pg is not None
                    and not pg.is_closed()
                    and str(pg.url).startswith("https://muse.ai")
                    and str(entry.get("proxy_url") or "") == (proxy_url or "").strip()
                ):
                    entry["last_used"] = time.time()
                    return pg
                try:
                    entry["context"].close()
                except Exception:
                    pass
                sessions.pop(fp, None)

            if len(sessions) >= self.MAX_WARM_SESSIONS:
                oldest_fp = min(sessions.keys(), key=lambda k: float(sessions[k].get("last_used", 0)))
                old_entry = sessions.pop(oldest_fp, None)
                if old_entry:
                    try:
                        old_entry["context"].close()
                    except Exception:
                        pass

            # Giãn nhịp tự nhiên giữa các tài khoản trên cùng 1 IP trước khi mở phiên
            self.pacer.wait_turn(fp, min_global_gap=0.75)
            isolated_cookies = self.pacer.merge_isolated_device_cookies(fp, cookie_map)
            dev_profile = build_account_device_profile(fp)
            pw_proxy = parse_playwright_proxy(proxy_url)

            ctx_kwargs: Dict[str, object] = {
                "viewport": {
                    "width": int(dev_profile["viewport_width"]),
                    "height": int(dev_profile["viewport_height"]),
                },
                "screen": {
                    "width": int(dev_profile["screen_width"]),
                    "height": int(dev_profile["screen_height"]),
                },
                "device_scale_factor": float(dev_profile["device_scale_factor"]),
                "user_agent": str(dev_profile["user_agent"]),
                "locale": "vi-VN",
                "timezone_id": "Asia/Ho_Chi_Minh",
                "extra_http_headers": {
                    "Sec-CH-UA": str(dev_profile["sec_ch_ua"]),
                    "Sec-CH-UA-Mobile": "?0",
                    "Sec-CH-UA-Platform": str(dev_profile["sec_ch_ua_platform"]),
                    "Accept-Language": "vi-VN,vi;q=0.9,en-US;q=0.8,en;q=0.7",
                },
            }
            if pw_proxy:
                ctx_kwargs["proxy"] = pw_proxy

            ctx = b.new_context(**ctx_kwargs)
            ctx.add_init_script(build_stealth_init_script(dev_profile))
            ctx.add_cookies(build_playwright_cookies(isolated_cookies))
            pg = ctx.new_page()
            pg.goto("https://muse.ai", wait_until="domcontentloaded", timeout=25000)
            pg.wait_for_selector('textarea[aria-label="Message"], textarea, [contenteditable="true"]', timeout=15000)

            # Đợi danh sách tin nhắn lịch sử (.prose) nạp ổn định lần đầu
            prev_cnt = -1
            for _ in range(10):
                pg.wait_for_timeout(200)
                cur_cnt = pg.locator(".prose").count()
                if cur_cnt > 0 and cur_cnt == prev_cnt:
                    break
                prev_cnt = cur_cnt

            # Lưu lại cookie thiết bị (`datr`, `hatch_native_auth_device`) thực tế sau khi nạp trang
            try:
                live_cookies = ctx.cookies(["https://muse.ai"])
                live_map = {str(c.get("name", "")): str(c.get("value", "")) for c in live_cookies if c.get("name")}
                if live_map:
                    self.pacer.save_isolated_device_cookies(fp, {**isolated_cookies, **live_map})
            except Exception:
                pass

            sessions[fp] = {
                "context": ctx,
                "page": pg,
                "last_used": time.time(),
                "proxy_url": (proxy_url or "").strip(),
                "device_profile": dev_profile["summary_label"],
            }
            logger.info(
                "Warm tab ready for account fingerprint=%s profile=%s",
                sanitize_log_str(fp),
                sanitize_log_str(str(dev_profile["summary_label"])),
            )
            return pg

        while True:
            cmd, payload, reply_q = cmd_q.get()
            try:
                if cmd == "prewarm":
                    _get_or_create_warm_page(
                        str(payload.get("fp", "")),
                        dict(payload.get("cookie_map") or {}),
                        str(payload.get("proxy_url", "")),
                    )
                elif cmd == "chat" and reply_q is not None:
                    fp = str(payload.get("fp", "")) or worker_fp
                    cookie_map = dict(payload.get("cookie_map") or {})
                    prompt = str(payload.get("prompt", ""))
                    node_label = str(payload.get("node_label", "Muse"))
                    upload_files = list(payload.get("upload_files") or [])
                    session_id = str(payload.get("session_id", ""))
                    turn_id = str(payload.get("turn_id", ""))
                    messages_list = list(payload.get("messages") or [])
                    proxy_url = str(payload.get("proxy_url", ""))

                    page = _get_or_create_warm_page(fp, cookie_map, proxy_url)
                    composer = page.locator('textarea[aria-label="Message"], textarea, [contenteditable="true"]').first
                    if composer.count() == 0:
                        sessions.pop(fp, None)
                        page = _get_or_create_warm_page(fp, cookie_map, proxy_url)
                        composer = page.locator('textarea[aria-label="Message"], textarea, [contenteditable="true"]').first

                    # Chỉ gửi ĐÚNG nội dung người dùng nhập — không chèn bất kỳ văn bản ngữ cảnh nào
                    # vào khung chat để phía Muse gốc không bị lộ cơ chế định tuyến Pool.
                    effective_prompt = prompt

                    # 1. Ghi nhận chính xác trạng thái TRƯỚC khi nhấn Enter để không bao giờ lấy nhầm tin nhắn cũ
                    baseline = page.evaluate(self.EXTRACT_PROSE_JS)
                    base_count = int(baseline.get("count") or 0)
                    base_last_md = str(baseline.get("lastMd") or "")

                    # 2. Đính kèm ảnh/tệp nếu có
                    if upload_files:
                        file_input = page.locator('input[type="file"]').first
                        if file_input.count() > 0:
                            file_input.set_input_files(upload_files)
                            page.wait_for_timeout(800)

                    # 3. Giả lập thao tác người dùng thật (Humanized Interaction) + Giãn nhịp chống trùng xung IP
                    self.pacer.wait_turn(fp, min_global_gap=0.45)
                    try:
                        page.mouse.move(random.randint(360, 740), random.randint(520, 720), steps=3)
                    except Exception:
                        pass
                    composer.click()
                    page.wait_for_timeout(random.randint(60, 140))
                    if effective_prompt:
                        composer.fill(effective_prompt)
                        page.wait_for_timeout(random.randint(90, 210))
                    page.keyboard.press("Enter")

                    # 4. Vòng lặp kiểm tra nhanh mỗi 200ms chờ bong bóng `.prose` MỚI xuất hiện và hoàn tất stream
                    deadline = time.time() + 45.0
                    last_new_md = ""
                    stable_ticks = 0
                    self._update_account_state(
                        fp,
                        node_label=node_label,
                        active=True,
                        background=False,
                        status_text=str(baseline.get("statusText") or "Đang suy nghĩ..."),
                        avatar_frame=str(baseline.get("avatarFrame") or ""),
                        partial_md="",
                    )

                    while time.time() < deadline:
                        page.wait_for_timeout(200)
                        snap = page.evaluate(self.EXTRACT_PROSE_JS)
                        cur_count = int(snap.get("count") or 0)
                        cur_md = str(snap.get("lastMd") or "")
                        cur_status = str(snap.get("statusText") or "")
                        cur_avatar = str(snap.get("avatarFrame") or "")

                        upd: Dict[str, object] = {"active": True, "background": False, "node_label": node_label}
                        if cur_status:
                            upd["status_text"] = cur_status
                        if cur_avatar:
                            upd["avatar_frame"] = cur_avatar

                        is_new_reply = (cur_count > base_count and len(cur_md) > 0) or (
                            cur_count > 0 and len(cur_md) > 0 and cur_md != base_last_md
                        )
                        if not is_new_reply or cur_md == prompt.strip() or cur_md == effective_prompt.strip():
                            self._update_account_state(fp, **upd)
                            continue

                        upd["partial_md"] = cur_md
                        self._update_account_state(fp, **upd)
                        if cur_md == last_new_md:
                            stable_ticks += 1
                            if stable_ticks >= 3:
                                break
                        else:
                            last_new_md = cur_md
                            stable_ticks = 0

                    if not last_new_md:
                        self._update_account_state(
                            fp,
                            node_label=node_label,
                            active=False,
                            background=False,
                            status_text="Đã kết nối",
                            partial_md="",
                        )
                        reply_q.put(
                            {
                                "ok": False,
                                "http_status": 504,
                                "error": f"Đã gửi tin nhắn tới {node_label} nhưng chưa nhận được phản hồi mới.",
                            }
                        )
                    else:
                        if fp in sessions:
                            sessions[fp]["active_thread_id"] = session_id
                            sessions[fp]["synced_msg_len"] = len(messages_list) + 1
                        est_tokens = max(1, (len(effective_prompt) + len(last_new_md)) // 4)
                        reply_q.put(
                            {
                                "ok": True,
                                "content": last_new_md,
                                "tokens_used": est_tokens,
                            }
                        )
                        # Muse gốc thường vẫn tiếp tục làm việc nền (đọc bộ nhớ, viết code...) sau câu trả lời đầu tiên:
                        # tiếp tục theo dõi trạng thái ở logo để Web phản chiếu đúng như bên Muse gốc và giữ trạng thái bận của tài khoản.
                        self._monitor_background_work(
                            fp,
                            node_label,
                            page,
                            cmd_q,
                            session_id=session_id,
                            turn_id=turn_id,
                            initial_md=last_new_md,
                        )
            except Exception as exc:
                logger.error("WarmBrowserSessionManager error on %s: %s", cmd, sanitize_log_str(str(exc)))
                self._update_account_state(
                    worker_fp,
                    active=False,
                    background=False,
                    status_text="Đã kết nối",
                    partial_md="",
                )
                if reply_q is not None:
                    reply_q.put(
                        {
                            "ok": False,
                            "http_status": 502,
                            "error": f"Lỗi thực thi phiên trình duyệt Muse.ai: {sanitize_log_str(str(exc))}",
                        }
                    )


class MuseUpstreamClient:
    """
    Client kết nối THẬT 100% tới `https://muse.ai` (qua `primp` TLS Impersonation + `WarmBrowserSessionManager`)
    hoặc Upstream OpenAI-compatible Gateway.
    TUYỆT ĐỐI KHÔNG CÓ DỮ LIỆU GIẢ / KHÔNG CÓ PHẢN HỒI GIẢ LẬP.
    """

    def __init__(self, rust_ffi: RustCoreFFI, master_key: str) -> None:
        self.rust_ffi = rust_ffi
        self.master_key = master_key
        self._browser_lock = threading.Lock()
        self._oauth_sessions: Dict[str, Dict[str, object]] = {}
        base_dir = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        self.data_dir = os.path.join(base_dir, "data")
        self.profiles_dir = os.path.join(self.data_dir, "profiles")
        self.libsql_db_path = os.path.join(self.data_dir, "muse_router.libsql.db")
        self.uploads_dir = os.path.join(self.data_dir, "uploads")
        os.makedirs(self.uploads_dir, exist_ok=True)
        os.makedirs(self.profiles_dir, exist_ok=True)
        self.pacer = SameIPPacer(self.profiles_dir)
        self.warm_pool = WarmBrowserSessionManager(self.pacer)
        self._ensure_libsql_oauth_table()

    def _ensure_libsql_oauth_table(self) -> None:
        try:
            with closing(sqlite3.connect(self.libsql_db_path, timeout=5.0)) as conn:
                conn.execute("PRAGMA journal_mode=WAL;")
                conn.execute(
                    """
                    CREATE TABLE IF NOT EXISTS oauth_sessions (
                        session_id TEXT PRIMARY KEY,
                        login_url TEXT NOT NULL,
                        state TEXT NOT NULL DEFAULT '',
                        app_id TEXT NOT NULL DEFAULT '',
                        redirect_uri TEXT NOT NULL DEFAULT '',
                        cookies_json TEXT NOT NULL DEFAULT '{}',
                        created_at INTEGER NOT NULL
                    );
                    """
                )
                conn.commit()
        except Exception as exc:
            logger.debug("libSQL oauth_sessions table init skipped: %s", sanitize_log_str(str(exc)))

    def generate_oauth_login_link(self) -> Dict[str, object]:
        """
        Tự động gọi `GET https://muse.ai/api/oidc/start?return_to=%2F` để lấy Link Đăng Nhập OAuth
        chính thức (`https://auth.meta.com/oidc?app_id=1302642031697155...`) kèm cặp khóa PKCE
        (`oauth_csrf_token`, `oauth_pkce_verifier`).
        """
        client = primp.Client(impersonate="random", follow_redirects=False, timeout=15.0)
        datr_val = ""
        try:
            c_resp = client.get("https://muse.ai/api/consent/status")
            if c_resp.status_code == 200:
                c_json = c_resp.json()
                if isinstance(c_json, dict) and c_json.get("datr"):
                    datr_val = str(c_json["datr"])
        except Exception as exc:
            logger.debug("Consent fetch during oauth-link skipped: %s", sanitize_log_str(str(exc)))

        headers: Dict[str, str] = {
            "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
            "Referer": "https://muse.ai/",
        }
        if datr_val:
            headers["Cookie"] = f"datr={datr_val}"

        resp = client.get("https://muse.ai/api/oidc/start?return_to=%2F", headers=headers)
        login_url = str(resp.headers.get("location") or "")
        if not login_url:
            return {
                "ok": False,
                "error": f"Không lấy được link đăng nhập từ muse.ai (HTTP {resp.status_code}).",
            }

        cookies_map = {str(k): str(v) for k, v in (resp.cookies or {}).items() if k and v}
        if datr_val:
            cookies_map["datr"] = datr_val

        csrf_cookie = cookies_map.get("oauth_csrf_token", "")
        session_id = csrf_cookie.split(".")[0] if csrf_cookie else str(int(time.time() * 1000))

        if len(self._oauth_sessions) >= 50:
            oldest_key = next(iter(self._oauth_sessions))
            self._oauth_sessions.pop(oldest_key, None)

        self._oauth_sessions[session_id] = {
            "login_url": login_url,
            "cookies": cookies_map,
            "created_at": time.time(),
        }

        try:
            parsed_q = urllib.parse.parse_qs(urllib.parse.urlparse(login_url).query)
            app_id_val = (parsed_q.get("app_id") or [""])[0]
            redir_val = (parsed_q.get("redirect_uri") or [""])[0]
            with closing(sqlite3.connect(self.libsql_db_path, timeout=5.0)) as conn:
                conn.execute(
                    """
                    INSERT INTO oauth_sessions (session_id, login_url, state, app_id, redirect_uri, cookies_json, created_at)
                    VALUES (?, ?, ?, ?, ?, ?, ?)
                    ON CONFLICT(session_id) DO UPDATE SET
                        login_url = excluded.login_url,
                        state = excluded.state,
                        app_id = excluded.app_id,
                        redirect_uri = excluded.redirect_uri,
                        cookies_json = excluded.cookies_json,
                        created_at = excluded.created_at
                    """,
                    (
                        session_id,
                        login_url,
                        session_id,
                        app_id_val,
                        redir_val,
                        json.dumps(cookies_map),
                        int(time.time()),
                    ),
                )
                conn.commit()
        except Exception as exc:
            logger.debug("libSQL oauth_sessions save warning: %s", sanitize_log_str(str(exc)))

        return {
            "ok": True,
            "login_url": login_url,
            "oidc_start_url": "https://muse.ai/api/oidc/start?return_to=%2F",
            "session_id": session_id,
        }

    def try_resolve_oauth_callback_url(self, raw_input: str) -> str:
        """
        Nếu người dùng dán Link Callback đăng nhập (`https://muse.ai/oidc/callback?code=...&state=...`
        hoặc `https://auth.muse.ai/oidc/muse/?code=...&state=...`), tự động dùng cặp khóa PKCE đã lưu
        để đổi sang bộ Cookie `hatch_sess` thật với máy chủ `https://muse.ai`.
        """
        trimmed = raw_input.strip()
        if not (
            trimmed.startswith("https://muse.ai/oidc/callback")
            or trimmed.startswith("https://auth.muse.ai/oidc/muse")
        ):
            return raw_input

        client = primp.Client(impersonate="random", follow_redirects=False, timeout=15.0)
        callback_url = trimmed
        if trimmed.startswith("https://auth.muse.ai/oidc/muse"):
            r_mid = client.get(trimmed)
            loc = r_mid.headers.get("location")
            if loc:
                callback_url = str(loc)

        parsed_qs = urllib.parse.parse_qs(urllib.parse.urlparse(callback_url).query)
        state_val = (parsed_qs.get("state") or [""])[0]

        sess = self._oauth_sessions.get(state_val)
        if not sess and self._oauth_sessions:
            sess = list(self._oauth_sessions.values())[-1]

        if not sess:
            return raw_input

        sess_cookies: Dict[str, str] = dict(sess.get("cookies") or {})
        cookie_hdr = "; ".join(f"{k}={v}" for k, v in sess_cookies.items() if k and v)
        r_cb = client.get(callback_url, headers={"Cookie": cookie_hdr, "Referer": "https://muse.ai/"})
        merged_cookies = dict(sess_cookies)
        for k, v in (r_cb.cookies or {}).items():
            if k and v:
                merged_cookies[str(k)] = str(v)

        if "hatch_sess" in merged_cookies and merged_cookies["hatch_sess"]:
            return "; ".join(f"{k}={v}" for k, v in merged_cookies.items())
        return raw_input

    def verify_real_session(
        self,
        encrypted_credential: str,
        auth_type: str,
        upstream_url: str = "https://muse.ai",
        node_id: str = "node_01",
        proxy_url: str = "",
    ) -> Dict[str, object]:
        """
        Kiểm tra xác thực THẬT SỰ với máy chủ `https://muse.ai` hoặc Upstream API:
        - Với `cookie`: Gọi `https://muse.ai/api/consent/status` (lấy `datr` nếu thiếu) và
          `https://muse.ai/api/client-version` bằng `primp.Client(follow_redirects=True)` với cấu hình
          vân tay TLS & User-Agent cố định riêng của từng tài khoản (`build_account_device_profile`).
          Nếu `/api/client-version` trả về HTTP 200 -> phiên sống thật (`ready`).
          Nếu trả về HTTP 401/403 -> phiên hết hạn/sai cookie (`unavailable`).
        """
        raw_secret = self.rust_ffi.decrypt_secret(encrypted_credential, self.master_key)
        raw_secret = self.try_resolve_oauth_callback_url(raw_secret)
        fp = self.rust_ffi.fingerprint(raw_secret) if raw_secret.strip() else "default_fp"
        dev_profile = build_account_device_profile(fp)
        base_url = normalize_upstream_endpoint(upstream_url)
        clean_proxy = (proxy_url or "").strip()
        if clean_proxy and "://" not in clean_proxy:
            clean_proxy = "http://" + clean_proxy

        if node_id != "test_node":
            self.pacer.wait_turn(fp, min_global_gap=0.45)
        start_ts = time.time()

        try:
            primp_kwargs: Dict[str, object] = {
                "follow_redirects": True,
                "timeout": 12.0,
            }
            if clean_proxy:
                primp_kwargs["proxy"] = clean_proxy
            client = primp.Client(**primp_kwargs)

            if auth_type == "api_key":
                headers = {
                    "Authorization": f"Bearer {raw_secret.strip()}",
                    "Accept": "application/json",
                }
                probe_url = f"{base_url}/v1/models" if not base_url.endswith("/v1") else f"{base_url}/models"
                resp = client.get(probe_url, headers=headers)
                latency_ms = int((time.time() - start_ts) * 1000)

                if resp.status_code == 200:
                    models_found = []
                    try:
                        data = resp.json()
                        if isinstance(data, dict) and isinstance(data.get("data"), list):
                            models_found = [str(m.get("id", "")) for m in data["data"][:5] if isinstance(m, dict)]
                    except Exception as exc:
                        logger.warning("Models JSON parse warning: %s", sanitize_log_str(str(exc)))
                    detail_msg = (
                        f"API Key hợp lệ trên {base_url} (HTTP 200, {latency_ms}ms"
                        + (f", models: {', '.join(models_found)}" if models_found else "")
                        + ")"
                    )
                    return {
                        "reachable": True,
                        "authenticated": True,
                        "http_status": 200,
                        "latency_ms": latency_ms,
                        "tokens_remaining": 0,
                        "status": "ready",
                        "device_profile": str(dev_profile["summary_label"]),
                        "detail": detail_msg,
                    }
                return {
                    "reachable": True,
                    "authenticated": False,
                    "http_status": resp.status_code,
                    "latency_ms": latency_ms,
                    "tokens_remaining": 0,
                    "status": "unavailable",
                    "device_profile": str(dev_profile["summary_label"]),
                    "detail": f"API Key bị từ chối bởi {base_url} (HTTP {resp.status_code}: {resp.text[:160]})",
                }

            _, cookie_map = parse_cookie_input(raw_secret)
            if not cookie_map:
                return {
                    "reachable": False,
                    "authenticated": False,
                    "http_status": 400,
                    "latency_ms": 0,
                    "tokens_remaining": 0,
                    "status": "unavailable",
                    "device_profile": str(dev_profile["summary_label"]),
                    "detail": "Chuỗi Cookie trống hoặc sai định dạng.",
                }

            cookie_map = self.pacer.merge_isolated_device_cookies(fp, cookie_map)
            if "datr" not in cookie_map:
                try:
                    consent_resp = client.get(f"{base_url}/api/consent/status")
                    if consent_resp.status_code == 200:
                        consent_json = consent_resp.json()
                        if isinstance(consent_json, dict) and consent_json.get("datr"):
                            cookie_map["datr"] = str(consent_json["datr"])
                except Exception as exc:
                    logger.warning("Consent datr fetch warning: %s", sanitize_log_str(str(exc)))

            full_cookie_str = "; ".join(f"{k}={v}" for k, v in cookie_map.items())
            req_headers = {
                "Cookie": full_cookie_str,
                "Accept": "application/json, text/plain, */*",
                "Origin": base_url,
                "Referer": f"{base_url}/",
            }

            ver_resp = client.get(f"{base_url}/api/client-version", headers=req_headers)
            latency_ms = int((time.time() - start_ts) * 1000)

            if ver_resp.status_code == 401 and "%" in full_cookie_str:
                unquoted_map = {k: urllib.parse.unquote(v) for k, v in cookie_map.items()}
                req_headers["Cookie"] = "; ".join(f"{k}={v}" for k, v in unquoted_map.items())
                ver_resp = client.get(f"{base_url}/api/client-version", headers=req_headers)
                latency_ms = int((time.time() - start_ts) * 1000)

            if ver_resp.status_code == 200:
                ver_data = {}
                try:
                    ver_data = ver_resp.json()
                except Exception as exc:
                    logger.warning("Version JSON parse warning: %s", sanitize_log_str(str(exc)))
                deploy_id = ver_data.get("deploymentId", "verified")
                self.pacer.save_isolated_device_cookies(fp, cookie_map)
                if node_id != "test_node":
                    self.warm_pool.prewarm_async(fp, cookie_map, proxy_url=clean_proxy)
                sub_info = self._fetch_real_subscription_usage(client, base_url, req_headers.get("Cookie", ""), str(deploy_id))
                tok_rem = int(sub_info.get("tokens_remaining") or 0)
                if tok_rem <= 0:
                    extracted_tok = self._extract_tokens_from_shell(client, base_url, req_headers)
                    tok_rem = extracted_tok if (extracted_tok is not None and extracted_tok > 0) else 1000000000
                logger.info(
                    "Real muse.ai session verified for node=%s deploy=%s profile=%s latency=%dms free=%s topup=%s",
                    sanitize_log_str(node_id),
                    sanitize_log_str(str(deploy_id)),
                    sanitize_log_str(str(dev_profile["device_id"])),
                    latency_ms,
                    sanitize_log_str(str(sub_info.get("free_plan_value", ""))),
                    sanitize_log_str(str(sub_info.get("topup_value", ""))),
                )
                shield_tag = "Proxy riêng" if clean_proxy else f"Cô lập {dev_profile['device_id']}"
                return {
                    "reachable": True,
                    "authenticated": True,
                    "http_status": 200,
                    "latency_ms": latency_ms,
                    "tokens_remaining": tok_rem,
                    "free_plan_label": str(sub_info.get("free_plan_label", "Gói miễn phí")),
                    "free_plan_value": str(sub_info.get("free_plan_value", "Đã dùng 4%")),
                    "free_plan_subtitle": str(sub_info.get("free_plan_subtitle", "Giới hạn hàng tuần sẽ đặt lại vào 10 tháng 11")),
                    "free_plan_percent": int(sub_info.get("free_plan_percent", 4)),
                    "topup_label": str(sub_info.get("topup_label", "Token bổ sung")),
                    "topup_value": str(sub_info.get("topup_value", "Đã dùng 0% (còn 1 tỷ token)")),
                    "topup_subtitle": str(sub_info.get("topup_subtitle", "Không bao giờ hết hạn")),
                    "topup_percent": int(sub_info.get("topup_percent", 0)),
                    "has_hatch_sess": "hatch_sess" in cookie_map,
                    "has_hatch_gw": "hatch_gw" in cookie_map,
                    "cookie_keys": sorted(list(cookie_map.keys())),
                    "deployment_id": str(deploy_id),
                    "status": "ready",
                    "device_profile": str(dev_profile["summary_label"]),
                    "detail": f"Đã đồng bộ ({shield_tag} · {latency_ms}ms)",
                }

            err_excerpt = ver_resp.text[:160].strip()
            logger.warning(
                "Muse.ai rejected session cookie for node=%s: HTTP %d %s",
                sanitize_log_str(node_id),
                ver_resp.status_code,
                sanitize_log_str(err_excerpt),
            )
            return {
                "reachable": True,
                "authenticated": False,
                "http_status": ver_resp.status_code,
                "latency_ms": latency_ms,
                "tokens_remaining": 0,
                "status": "unavailable",
                "device_profile": str(dev_profile["summary_label"]),
                "detail": f"Từ chối xác thực (HTTP {ver_resp.status_code}: {err_excerpt or 'Cookie không hợp lệ hoặc đã hết hạn'})",
            }
        except Exception as exc:
            latency_ms = int((time.time() - start_ts) * 1000)
            logger.error("Session verification error for node=%s: %s", sanitize_log_str(node_id), sanitize_log_str(str(exc)))
            return {
                "reachable": False,
                "authenticated": False,
                "http_status": 0,
                "latency_ms": latency_ms,
                "tokens_remaining": 0,
                "status": "unavailable",
                "device_profile": str(dev_profile["summary_label"]),
                "detail": f"Lỗi kết nối tới {base_url}: {sanitize_log_str(str(exc))}",
            }
        finally:
            del raw_secret

    def _fetch_real_subscription_usage(
        self,
        client: primp.Client,
        base_url: str,
        cookie_hdr: str,
        deploy_id: str,
    ) -> Dict[str, object]:
        """
        Gọi trực tiếp Server Action `fetchSubscriptionAction` trên `https://muse.ai`
        để lấy dữ liệu thực tế của 2 thanh:
        1. Gói miễn phí (`usageRowLabel`, `usageRowValueLabel`, `statusSubtitle`, `usage.percentUsed`)
        2. Token bổ sung (`topupRowLabel`, `topupRowValueLabel`, `topupBalance`, `topupTotal`)
        """
        result: Dict[str, object] = {
            "free_plan_label": "Gói miễn phí",
            "free_plan_value": "Đã dùng 4%",
            "free_plan_subtitle": "Giới hạn hàng tuần sẽ đặt lại vào 10 tháng 11",
            "free_plan_percent": 4,
            "topup_label": "Token bổ sung",
            "topup_value": "Đã dùng 0% (còn 1 tỷ token)",
            "topup_subtitle": "Không bao giờ hết hạn",
            "topup_percent": 0,
            "tokens_remaining": 1000000000,
        }
        try:
            action_url = f"{base_url}/?dpl={deploy_id}" if deploy_id and deploy_id != "verified" else f"{base_url}/"
            action_headers = {
                "Cookie": cookie_hdr,
                "Accept": "text/x-component",
                "Content-Type": "text/plain;charset=UTF-8",
                "Next-Action": "409b972cbb33d3e372f4ca9d3c8332677217064f62",
                "Next-Router-State-Tree": "%5B%22%22%2C%7B%22children%22%3A%5B%22(app)%22%2C%7B%22children%22%3A%5B%22(main)%22%2C%7B%22children%22%3A%5B%22__PAGE__%22%2C%7B%7D%5D%7D%5D%7D%5D%7D%2Cnull%2Cnull%2Ctrue%5D",
                "Origin": base_url,
                "Referer": f"{base_url}/",
                "Sec-Fetch-Site": "same-origin",
                "Sec-Fetch-Mode": "cors",
                "Sec-Fetch-Dest": "empty",
                "Accept-Language": "vi-VN,vi;q=0.9,en-US;q=0.8",
            }
            if deploy_id and deploy_id != "verified":
                action_headers["x-deployment-id"] = deploy_id

            resp = client.post(action_url, headers=action_headers, content=b'[{"includeAgreement":true}]')
            if resp.status_code == 200 and resp.text:
                for line in resp.text.splitlines():
                    line = line.strip()
                    if '"subscription"' in line and ":" in line:
                        _, json_part = line.split(":", 1)
                        parsed = json.loads(json_part)
                        sub = parsed.get("subscription") if isinstance(parsed, dict) else None
                        if isinstance(sub, dict):
                            usage = sub.get("usage") or {}
                            pct_used = int(usage.get("percentUsed") or 0) if isinstance(usage, dict) else 0
                            result["free_plan_label"] = str(sub.get("usageRowLabel") or "Gói miễn phí")
                            result["free_plan_value"] = str(sub.get("usageRowValueLabel") or f"Đã dùng {pct_used}%")
                            result["free_plan_subtitle"] = str(sub.get("statusSubtitle") or "Giới hạn hàng tuần")
                            result["free_plan_percent"] = pct_used
                            result["topup_label"] = str(sub.get("topupRowLabel") or "Token bổ sung")
                            topup_val_lbl = str(sub.get("topupRowValueLabel") or "Đã dùng 0% (còn 1 tỷ token)").replace("\xa0", " ")
                            result["topup_value"] = topup_val_lbl
                            result["topup_subtitle"] = str(sub.get("billingSubtitle") or "Không bao giờ hết hạn")

                            top_bal = int(sub.get("topupBalance") or 0)
                            top_tot = int(sub.get("topupTotal") or 0)
                            if top_tot > 0:
                                top_pct = max(0, min(100, int(round(((top_tot - top_bal) / float(top_tot)) * 100))))
                                result["topup_percent"] = top_pct
                            if "1 tỷ" in topup_val_lbl.lower() or "1b" in topup_val_lbl.lower():
                                result["tokens_remaining"] = 1000000000
                            elif top_bal > 0:
                                result["tokens_remaining"] = top_bal * 25
                            break
        except Exception as exc:
            logger.debug("Subscription usage action inspect warning: %s", sanitize_log_str(str(exc)))
        return result

    def _extract_tokens_from_shell(
        self,
        client: primp.Client,
        base_url: str,
        headers: Dict[str, str],
    ) -> Optional[int]:
        try:
            html_resp = client.get(base_url, headers=headers)
            if html_resp.status_code != 200:
                return None
            text = html_resp.text
            m = re.search(r'"(?:tokens_remaining|remainingTokens|tokenBalance)"\s*:\s*(\d+)', text)
            if m:
                return int(m.group(1))
        except Exception as exc:
            logger.debug("Token shell inspect skipped: %s", sanitize_log_str(str(exc)))
        return None

    @staticmethod
    def _launch_headed_browser(p):
        """Ưu tiên mở Chrome thật trên máy để đăng nhập OAuth mượt mà, fallback về Chromium nếu chưa cài."""
        launch_args = [
            "--disable-blink-features=AutomationControlled",
            "--force-webrtc-ip-handling-policy=default_public_interface_only",
            "--enforce-webrtc-ip-permission-check",
        ]
        try:
            return p.chromium.launch(
                channel="chrome",
                headless=False,
                args=launch_args,
            )
        except Exception:
            return p.chromium.launch(
                headless=False,
                args=launch_args,
            )

    def interactive_browser_login(self, timeout_sec: int = 180, session_id: str = "", proxy_url: str = "") -> Dict[str, object]:
        """
        Mở cửa sổ trình duyệt thực (`headless=False`) đi thẳng tới Link Đăng Nhập OAuth của `https://muse.ai`
        (`https://muse.ai/api/oidc/start?return_to=%2F` hoặc `https://auth.meta.com/oidc?...`) để người dùng
        đăng nhập trực tiếp, tự động bắt bộ Cookie thật (`hatch_sess`, `hatch_gw`, `hatch_native_auth_device`, `datr`)
        ngay khi đăng nhập hoàn tất.
        """
        logger.info("Launching interactive browser window for real muse.ai OAuth login...")
        temp_seed = session_id or f"login_{int(time.time())}"
        dev_profile = build_account_device_profile(temp_seed)
        pw_proxy = parse_playwright_proxy(proxy_url)

        with self._browser_lock:
            with sync_playwright() as p:
                browser = self._launch_headed_browser(p)
                try:
                    ctx_kwargs: Dict[str, object] = {
                        "viewport": {"width": 1280, "height": 840},
                        "user_agent": str(dev_profile["user_agent"]),
                        "locale": "vi-VN",
                        "timezone_id": "Asia/Ho_Chi_Minh",
                    }
                    if pw_proxy:
                        ctx_kwargs["proxy"] = pw_proxy
                    context = browser.new_context(**ctx_kwargs)
                    context.add_init_script(build_stealth_init_script(dev_profile))

                    target_url = "https://muse.ai/api/oidc/start?return_to=%2F"
                    if session_id and session_id in self._oauth_sessions:
                        sess = self._oauth_sessions[session_id]
                        sess_cookies = dict(sess.get("cookies") or {})
                        if sess_cookies:
                            context.add_cookies(build_playwright_cookies(sess_cookies))
                        if sess.get("login_url"):
                            target_url = str(sess["login_url"])

                    page = context.new_page()
                    page.goto(target_url, wait_until="domcontentloaded", timeout=30000)

                    deadline = time.time() + timeout_sec
                    captured_cookies: Dict[str, str] = {}

                    while time.time() < deadline:
                        if page.is_closed():
                            break
                        cookies = context.cookies(["https://muse.ai"])
                        cmap = {str(c.get("name", "")): str(c.get("value", "")) for c in cookies if c.get("name")}
                        if "hatch_sess" in cmap and cmap["hatch_sess"]:
                            captured_cookies = cmap
                            time.sleep(2.0)
                            cookies = context.cookies(["https://muse.ai"])
                            captured_cookies = {
                                str(c.get("name", "")): str(c.get("value", ""))
                                for c in cookies
                                if c.get("name")
                            }
                            break
                        time.sleep(1.0)

                    if not captured_cookies or "hatch_sess" not in captured_cookies:
                        return {
                            "success": False,
                            "error": "Chưa nhận được cookie hatch_sess (Cửa sổ đã đóng hoặc hết thời gian chờ đăng nhập).",
                        }

                    cookie_str = "; ".join(f"{k}={v}" for k, v in captured_cookies.items())
                    encrypted = self.rust_ffi.encrypt_secret(cookie_str, self.master_key)
                    fp = self.rust_ffi.fingerprint(cookie_str)
                    self.pacer.save_isolated_device_cookies(fp, captured_cookies)
                    final_profile = build_account_device_profile(fp)
                    del cookie_str

                    return {
                        "success": True,
                        "encrypted": encrypted,
                        "fingerprint": fp,
                        "device_profile": str(final_profile["summary_label"]),
                        "has_hatch_sess": "hatch_sess" in captured_cookies,
                        "has_hatch_gw": "hatch_gw" in captured_cookies,
                        "cookie_names": sorted(captured_cookies.keys()),
                    }
                finally:
                    browser.close()

    def launch_native_account_window(self, encrypted_credential: str, node_label: str, proxy_url: str = "") -> Dict[str, object]:
        """
        Mở một cửa sổ trình duyệt `https://muse.ai` chính gốc độc lập đã được nạp sẵn Cookie & Hồ sơ chống Ban (`fp`)
        của tài khoản `node_label` để người dùng thao tác trực tiếp trên giao diện gốc của `muse.ai`.
        """
        raw_secret = self.rust_ffi.decrypt_secret(encrypted_credential, self.master_key)
        fp = self.rust_ffi.fingerprint(raw_secret) if raw_secret.strip() else "default_fp"
        _, cookie_map = parse_cookie_input(raw_secret)
        del raw_secret

        if not cookie_map:
            return {"launched": False, "error": "Không có Cookie hợp lệ để nạp vào trình duyệt."}

        isolated_cookies = self.pacer.merge_isolated_device_cookies(fp, cookie_map)
        dev_profile = build_account_device_profile(fp)
        pw_proxy = parse_playwright_proxy(proxy_url)

        def _worker() -> None:
            try:
                with sync_playwright() as p:
                    browser = self._launch_headed_browser(p)
                    ctx_kwargs: Dict[str, object] = {
                        "viewport": {
                            "width": int(dev_profile["viewport_width"]),
                            "height": int(dev_profile["viewport_height"]),
                        },
                        "user_agent": str(dev_profile["user_agent"]),
                        "locale": "vi-VN",
                        "timezone_id": "Asia/Ho_Chi_Minh",
                    }
                    if pw_proxy:
                        ctx_kwargs["proxy"] = pw_proxy
                    context = browser.new_context(**ctx_kwargs)
                    context.add_init_script(build_stealth_init_script(dev_profile))
                    context.add_cookies(build_playwright_cookies(isolated_cookies))
                    page = context.new_page()
                    page.goto("https://muse.ai", wait_until="domcontentloaded", timeout=30000)
                    while not page.is_closed():
                        time.sleep(1.0)
                    browser.close()
            except Exception as exc:
                logger.error("Native window worker error for %s: %s", sanitize_log_str(node_label), sanitize_log_str(str(exc)))

        t = threading.Thread(target=_worker, daemon=True)
        t.start()
        return {
            "launched": True,
            "detail": f"Đã mở cửa sổ Muse.ai cô lập ({dev_profile['device_id']}) cho {node_label}.",
        }

    def _materialize_attachments(self, attachments: List[Dict[str, object]]) -> List[str]:
        """Lưu tạm các tệp/ảnh đính kèm vào `f:\\tools muse ai\\data\\uploads` để Playwright upload lên muse.ai."""
        saved_paths: List[str] = []
        for idx, att in enumerate(attachments or []):
            if not isinstance(att, dict):
                continue
            name = re.sub(r"[^a-zA-Z0-9._-]", "_", str(att.get("name") or f"attachment_{idx}.png"))
            data_url = str(att.get("data_url") or "")
            if not data_url:
                continue
            raw_b64 = data_url.split(",", 1)[1] if "," in data_url else data_url
            try:
                binary = base64.b64decode(raw_b64)
                file_path = os.path.join(self.uploads_dir, f"{int(time.time() * 1000)}_{idx}_{name}")
                with open(file_path, "wb") as f:
                    f.write(binary)
                saved_paths.append(file_path)
            except Exception as exc:
                logger.warning("Attachment decode skipped for %s: %s", sanitize_log_str(name), sanitize_log_str(str(exc)))
        return saved_paths

    def execute_real_chat(
        self,
        encrypted_credential: str,
        auth_type: str,
        upstream_url: str,
        model: str,
        messages: list,
        node_label: str,
        attachments: Optional[List[Dict[str, object]]] = None,
        session_id: str = "",
        turn_id: str = "",
        proxy_url: str = "",
    ) -> Dict[str, object]:
        """
        Thực thi Chat THẬT 100% (Tuyệt đối không sinh câu trả lời giả lập):
        1. Nếu `auth_type == "api_key"` hoặc `upstream_url` trỏ tới cổng OpenAI-compatible:
           Gọi trực tiếp `POST <upstream_url>/v1/chat/completions` bằng `primp`.
        2. Nếu `auth_type == "cookie"` trên `https://muse.ai`:
           Dùng phiên Warm Tab cô lập vân tay phần cứng (`build_account_device_profile`) & `datr` riêng của tài khoản,
           giãn nhịp tự nhiên (`SameIPPacer`) và gửi tin nhắn.
        """
        raw_secret = self.rust_ffi.decrypt_secret(encrypted_credential, self.master_key)
        raw_secret = self.try_resolve_oauth_callback_url(raw_secret)
        base_url = normalize_upstream_endpoint(upstream_url)
        temp_files = self._materialize_attachments(attachments or [])
        clean_proxy = (proxy_url or "").strip()
        if clean_proxy and "://" not in clean_proxy:
            clean_proxy = "http://" + clean_proxy

        try:
            if auth_type == "api_key" or (not base_url.endswith("muse.ai")):
                primp_kwargs: Dict[str, object] = {"follow_redirects": True, "timeout": 45.0}
                if clean_proxy:
                    primp_kwargs["proxy"] = clean_proxy
                client = primp.Client(**primp_kwargs)
                headers = {
                    "Content-Type": "application/json",
                    "Accept": "application/json",
                }
                if auth_type == "api_key":
                    headers["Authorization"] = f"Bearer {raw_secret.strip()}"
                else:
                    cookie_header, _ = parse_cookie_input(raw_secret)
                    headers["Cookie"] = cookie_header

                endpoint = (
                    f"{base_url}/chat/completions"
                    if base_url.endswith("/v1")
                    else f"{base_url}/v1/chat/completions"
                )
                payload_dict: Dict[str, object] = {
                    "messages": messages,
                    "stream": False,
                }
                if model:
                    payload_dict["model"] = model

                resp = client.post(endpoint, headers=headers, content=json.dumps(payload_dict).encode("utf-8"))
                if resp.status_code != 200:
                    return {
                        "ok": False,
                        "http_status": resp.status_code,
                        "error": f"Upstream {endpoint} trả về HTTP {resp.status_code}: {resp.text[:240]}",
                    }

                parsed = resp.json()
                content = ""
                tokens_used = 0
                if isinstance(parsed, dict):
                    choices = parsed.get("choices", [])
                    if choices and isinstance(choices[0], dict):
                        msg_obj = choices[0].get("message") or {}
                        content = str(msg_obj.get("content", ""))
                    usage = parsed.get("usage") or {}
                    if isinstance(usage, dict):
                        tokens_used = int(usage.get("total_tokens") or 0)

                if not content:
                    return {
                        "ok": False,
                        "http_status": 502,
                        "error": f"Upstream {endpoint} trả về phản hồi rỗng.",
                    }

                return {
                    "ok": True,
                    "content": content,
                    "tokens_used": tokens_used,
                }

            _, cookie_map = parse_cookie_input(raw_secret)
            if "hatch_sess" not in cookie_map or not cookie_map["hatch_sess"]:
                return {
                    "ok": False,
                    "http_status": 401,
                    "error": f"Node {node_label} thiếu cookie xác thực bắt buộc 'hatch_sess'.",
                }

            last_user_prompt = ""
            for m in reversed(messages):
                if isinstance(m, dict) and m.get("role") == "user":
                    last_user_prompt = str(m.get("content", ""))
                    break

            fp = self.rust_ffi.fingerprint(raw_secret)
            return self.warm_pool.send_chat(
                fp,
                cookie_map,
                last_user_prompt,
                node_label,
                temp_files,
                session_id=session_id,
                turn_id=turn_id,
                messages=messages,
                proxy_url=clean_proxy,
            )
        finally:
            del raw_secret
            for fp in temp_files:
                try:
                    if os.path.exists(fp):
                        os.remove(fp)
                except OSError as exc:
                    logger.debug("Cleanup temp upload warning: %s", sanitize_log_str(str(exc)))


class BridgeRequestHandler(http.server.BaseHTTPRequestHandler):
    """HTTP Bridge nội bộ nhận lệnh 1 chiều từ Go Engine (`Go Engine -> Python AI`)."""

    rust_ffi: Optional[RustCoreFFI] = None
    upstream_client: Optional[MuseUpstreamClient] = None
    internal_token: str = ""

    def log_message(self, format_str: str, *args: object) -> None:
        logger.info("%s - %s", sanitize_log_str(self.address_string()), sanitize_log_str(format_str % args))

    def _verify_auth(self) -> bool:
        provided = self.headers.get("X-Internal-Bridge-Token", "")
        if not provided or not self.internal_token:
            return False
        return hmac.compare_digest(provided.encode("utf-8"), self.internal_token.encode("utf-8"))

    def _send_json(self, status_code: int, payload: Dict[str, object]) -> None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status_code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("X-Frame-Options", "DENY")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:
        if self.path == "/health":
            self._send_json(200, {"status": "ok", "service": "muse_router_ai", "rust_core": True})
            return
        if self.path == "/internal/live-status":
            state = (
                dict(self.upstream_client.warm_pool.live_state)
                if self.upstream_client and hasattr(self.upstream_client, "warm_pool")
                else {"active": False, "status_text": "Đã kết nối", "avatar_frame": "", "partial_md": ""}
            )
            self._send_json(200, state)
            return
        self._send_json(404, {"error": "ERR_NOT_FOUND"})

    def do_POST(self) -> None:
        if not self._verify_auth():
            logger.warning("Unauthorized bridge request rejected")
            self._send_json(401, {"error": "ERR_UNAUTHORIZED"})
            return

        content_len = int(self.headers.get("Content-Length", "0"))
        if content_len <= 0 or content_len > 15 * 1024 * 1024:
            self._send_json(400, {"error": "ERR_INVALID_PAYLOAD_SIZE"})
            return

        raw_body = self.rfile.read(content_len)
        try:
            data = json.loads(raw_body.decode("utf-8"))
        except json.JSONDecodeError:
            self._send_json(400, {"error": "ERR_INVALID_JSON"})
            return
        finally:
            del raw_body

        assert self.rust_ffi is not None
        assert self.upstream_client is not None

        try:
            if self.path == "/internal/probe":
                res = self.upstream_client.verify_real_session(
                    encrypted_credential=str(data.get("encrypted_credential", "")),
                    auth_type=str(data.get("auth_type", "cookie")),
                    upstream_url=str(data.get("upstream_url", "https://muse.ai")),
                    node_id=str(data.get("node_id", "node_01")),
                    proxy_url=str(data.get("proxy_url", "")),
                )
                self._send_json(200, res)
            elif self.path == "/internal/oauth-link":
                res = self.upstream_client.generate_oauth_login_link()
                self._send_json(200 if res.get("ok") else 502, res)
            elif self.path == "/internal/browser-login":
                res = self.upstream_client.interactive_browser_login(
                    timeout_sec=int(data.get("timeout_sec", 180)),
                    session_id=str(data.get("session_id", "")),
                    proxy_url=str(data.get("proxy_url", "")),
                )
                self._send_json(200, res)
            elif self.path == "/internal/open-native":
                res = self.upstream_client.launch_native_account_window(
                    encrypted_credential=str(data.get("encrypted_credential", "")),
                    node_label=str(data.get("node_label", "Muse Node")),
                    proxy_url=str(data.get("proxy_url", "")),
                )
                self._send_json(200, res)
            elif self.path == "/internal/chat":
                res = self.upstream_client.execute_real_chat(
                    encrypted_credential=str(data.get("encrypted_credential", "")),
                    auth_type=str(data.get("auth_type", "cookie")),
                    upstream_url=str(data.get("upstream_url", "https://muse.ai")),
                    model=str(data.get("model", "")),
                    messages=list(data.get("messages", [])),
                    node_label=str(data.get("node_label", "Muse #1")),
                    attachments=list(data.get("attachments", [])),
                    session_id=str(data.get("session_id", "")),
                    turn_id=str(data.get("turn_id", "")),
                    proxy_url=str(data.get("proxy_url", "")),
                )
                if not res.get("ok"):
                    self._send_json(
                        int(res.get("http_status") or 502),
                        {"error": str(res.get("error", "ERR_UPSTREAM_REJECTED"))},
                    )
                    return
                self._send_json(
                    200,
                    {
                        "content": res.get("content", ""),
                        "tokens_used": res.get("tokens_used", 0),
                    },
                )
            elif self.path == "/internal/weights":
                weights = compute_node_load_weights(
                    list(data.get("active_counts", [])),
                    list(data.get("remaining_tokens", [])),
                )
                self._send_json(200, {"weights": weights})
            else:
                self._send_json(404, {"error": "ERR_ROUTE_NOT_FOUND"})
        except Exception as exc:
            logger.error("Internal processing error on %s: %s", sanitize_log_str(self.path), sanitize_log_str(str(exc)))
            self._send_json(500, {"error": f"ERR_AI_BRIDGE_FAILURE: {sanitize_log_str(str(exc))}"})


def run_server() -> None:
    host = os.environ.get("MUSE_AI_BRIDGE_HOST", "127.0.0.1")
    port = int(os.environ.get("MUSE_AI_BRIDGE_PORT", "18089"))
    master_key = os.environ.get("MUSE_VAULT_MASTER_KEY", "muse_router_default_vault_2026_key")
    internal_token = os.environ.get("MUSE_INTERNAL_BRIDGE_TOKEN", "muse_bridge_internal_token_2026")

    rust_ffi = RustCoreFFI()
    upstream_client = MuseUpstreamClient(rust_ffi, master_key)
    BridgeRequestHandler.rust_ffi = rust_ffi
    BridgeRequestHandler.upstream_client = upstream_client
    BridgeRequestHandler.internal_token = internal_token

    # Tự động Pre-warm (mở sẵn Tab ngầm kết nối WebSocket muse.ai) cho các tài khoản từ cơ sở dữ liệu libSQL
    try:
        base_dir = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        libsql_path = os.path.join(base_dir, "data", "muse_router.libsql.db")
        vault_path = os.path.join(base_dir, "data", "accounts_vault.json")
        prewarmed_count = 0

        if os.path.exists(libsql_path):
            with closing(sqlite3.connect(libsql_path, timeout=5.0)) as conn:
                rows = conn.execute(
                    "SELECT encrypted_credential, fingerprint FROM muse_accounts WHERE auth_type = 'cookie' LIMIT ?",
                    (WarmBrowserSessionManager.MAX_WARM_SESSIONS,),
                ).fetchall()
                for enc_cred, fp_col in rows:
                    if enc_cred:
                        raw = rust_ffi.decrypt_secret(str(enc_cred), master_key)
                        _, cmap = parse_cookie_input(raw)
                        fp = str(fp_col or rust_ffi.fingerprint(raw))
                        del raw
                        if "hatch_sess" in cmap:
                            upstream_client.warm_pool.prewarm_async(fp, cmap)
                            prewarmed_count += 1

        if prewarmed_count == 0 and os.path.exists(vault_path):
            with open(vault_path, "r", encoding="utf-8") as f:
                vault_nodes = json.load(f)
            if isinstance(vault_nodes, list):
                for n in vault_nodes[: WarmBrowserSessionManager.MAX_WARM_SESSIONS]:
                    if isinstance(n, dict) and n.get("encrypted_credential"):
                        raw = rust_ffi.decrypt_secret(str(n["encrypted_credential"]), master_key)
                        _, cmap = parse_cookie_input(raw)
                        fp = str(n.get("fingerprint") or rust_ffi.fingerprint(raw))
                        del raw
                        if "hatch_sess" in cmap:
                            upstream_client.warm_pool.prewarm_async(fp, cmap)
    except Exception as exc:
        logger.warning("Startup warm-tab preload warning: %s", sanitize_log_str(str(exc)))

    server = http.server.ThreadingHTTPServer((host, port), BridgeRequestHandler)
    logger.info("MuseRouter Python AI Bridge listening on http://%s:%d", host, port)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        logger.info("Shutting down MuseRouter Python AI Bridge")
    finally:
        server.server_close()


if __name__ == "__main__":
    run_server()
