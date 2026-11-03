# -*- coding: utf-8 -*-
"""
Unit Tests thực tế cho `muse_router_ai` (PHAN 9 & PHAN 4).
Kiểm tra:
1. Kết nối FFI thực tế tới `muse_router_core.dll` (encrypt/decrypt/fingerprint/constant_time_compare).
2. Phân tích Cookie `hatch_sess`, `hatch_gw` và `build_playwright_cookies` cho dual-domain `.muse.ai` & `muse.ai`.
3. Xác thực thực tế với máy chủ `https://muse.ai/api/client-version`: một cookie không hợp lệ BẮT BUỘC phải bị `https://muse.ai` trả về HTTP 401 (`status == "unavailable"`, `authenticated == False`), tuyệt đối không được giả lập là `ready`.
"""

import unittest
from bridge_service import (
    MuseUpstreamClient,
    RustCoreFFI,
    build_account_device_profile,
    build_playwright_cookies,
    build_stealth_init_script,
    compute_node_load_weights,
    parse_cookie_input,
    parse_playwright_proxy,
)


class TestMuseRouterAIBridge(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.ffi = RustCoreFFI()
        cls.master_key = "unit_test_master_vault_key_2026"
        cls.client = MuseUpstreamClient(cls.ffi, cls.master_key)

    def test_rust_ffi_encrypt_decrypt_and_fingerprint(self) -> None:
        raw_cookie = "hatch_sess=sess_abc123xyz; hatch_gw=gw_ap_southeast_1; datr=meta_datr_01"
        encrypted = self.ffi.encrypt_secret(raw_cookie, self.master_key)
        self.assertTrue(encrypted.startswith("v1:"))
        decrypted = self.ffi.decrypt_secret(encrypted, self.master_key)
        self.assertEqual(decrypted, raw_cookie)

        fp = self.ffi.fingerprint(raw_cookie)
        self.assertTrue(fp.startswith("fp_"))
        self.assertEqual(len(fp), 19)

    def test_parse_cookie_and_playwright_dual_domain(self) -> None:
        json_input = '[{"name": "hatch_sess", "value": "sess%3A123"}, {"name": "hatch_gw", "value": "gw_01"}]'
        _, cookie_map = parse_cookie_input(json_input)
        pw_cookies = build_playwright_cookies(cookie_map)
        self.assertEqual(len(pw_cookies), 4)  # 2 cookies x 2 domains (.muse.ai & muse.ai)
        vals = {c["value"] for c in pw_cookies if c["name"] == "hatch_sess"}
        self.assertIn("sess:123", vals)  # Đã unquote %3A -> :

    def test_real_muse_ai_rejects_invalid_cookie(self) -> None:
        enc = self.ffi.encrypt_secret("hatch_sess=invalid_expired_token_test", self.master_key)
        res = self.client.verify_real_session(enc, "cookie", "https://muse.ai", "test_node")
        # Máy chủ thật https://muse.ai bắt buộc phải trả về 401 -> unavailable
        self.assertFalse(res.get("authenticated"))
        self.assertEqual(res.get("status"), "unavailable")
        self.assertEqual(res.get("http_status"), 401)

    def test_load_balancing_weights(self) -> None:
        weights = compute_node_load_weights([0, 2, 5], [5000000000, 5000000000, 1000000000])
        self.assertEqual(len(weights), 3)
        self.assertGreater(weights[0], weights[1])

    def test_generate_real_oauth_login_link(self) -> None:
        res = self.client.generate_oauth_login_link()
        self.assertTrue(res.get("ok"))
        login_url = str(res.get("login_url") or "")
        self.assertTrue(login_url.startswith("https://auth.meta.com/oidc"))
        self.assertIn("app_id=1302642031697155", login_url)
        self.assertTrue(bool(res.get("session_id")))

    def test_anti_detect_device_profile_isolation(self) -> None:
        prof1_a = build_account_device_profile("fp_account_alpha_001")
        prof1_b = build_account_device_profile("fp_account_alpha_001")
        prof2 = build_account_device_profile("fp_account_beta_002")

        # Cùng 1 tài khoản phải luôn giữ cố định 1 hồ sơ thiết bị qua mọi lần khởi động
        self.assertEqual(prof1_a["device_id"], prof1_b["device_id"])
        self.assertEqual(prof1_a["summary_label"], prof1_b["summary_label"])

        # Hai tài khoản khác nhau trên cùng 1 IP bắt buộc có hồ sơ thiết bị khác nhau
        self.assertNotEqual(prof1_a["device_id"], prof2["device_id"])
        script = build_stealth_init_script(prof1_a)
        self.assertIn("WebGLRenderingContext", script)
        self.assertIn("CanvasRenderingContext2D", script)

        proxy_cfg = parse_playwright_proxy("http://user01:pass02@192.168.1.50:8080")
        self.assertIsNotNone(proxy_cfg)
        self.assertEqual(proxy_cfg["server"], "http://192.168.1.50:8080")
        self.assertEqual(proxy_cfg["username"], "user01")


if __name__ == "__main__":
    unittest.main()
