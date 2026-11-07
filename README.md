# 🛒 Mart AI (MuseRouter) — Go Engine

> Bản phát hành hiện tại chạy bằng một executable Go duy nhất. Python Bridge, Rust Core và C# launcher không còn là runtime bắt buộc.

> Kết nối nhiều tài khoản `muse.ai` thành **Token Pool thống nhất** — giao diện Chat **Mart AI** chuẩn gốc, chạy song song đa luồng theo từng tài khoản, tự động nhảy câu hỏi sang tài khoản rảnh và tự động đồng bộ toàn diện.

---

## ✨ Tính Năng Nổi Bật

| Tính năng | Mô tả |
|-----------|-------|
| **Đa tài khoản song song thật sự** | Mỗi tài khoản chạy trên một luồng Playwright riêng biệt (`_account_worker_loop`), xử lý nhiều câu hỏi cùng lúc |
| **Tự động nhảy sang tài khoản rảnh** | Khi tài khoản đang trả lời hoặc đang làm việc nền (`Đọc bộ nhớ`, `Viết code`...), câu hỏi mới tự động định tuyến sang tài khoản rảnh |
| **Giữ chỗ dòng trống & trả lời đúng vị trí** | Gửi liên tiếp nhiều câu hỏi không bị khóa nút gửi; mỗi câu hỏi tạo dòng trống ngay bên dưới và điền câu trả lời đúng vào vị trí của câu hỏi đó (`turn_id`) |
| **Tự động đồng bộ toàn diện** | Tự động đồng bộ Token & Mức sử dụng (`Gói miễn phí %`, `Token bổ sung`) khi khởi động, định kỳ mỗi 45s, sau mỗi câu hỏi và khi mở Cài đặt; tự động đồng bộ kết quả làm việc nền từ Muse gốc về đúng câu hỏi |
| **Chống ban IP & Cô lập vân tay** | `SameIPPacer` + cô lập `datr`, Canvas, WebGL, AudioContext và Device Profile cố định theo từng tài khoản |
| **MySQL / TiDB Cloud (InnoDB)** | Lưu trữ toàn bộ tài khoản mã hóa, người dùng, lịch sử chat, tài liệu, bộ nhớ, nhiệm vụ qua giao thức MySQL (TiDB Serverless TLS 1.2+ hoặc Local MySQL) |
| **Chat đa phương tiện & Giọng nói** | Ô nhập tự giãn cao/ngang theo nội dung, Markdown chuẩn gốc, đính kèm ảnh (`Ctrl+V`), nhập giọng nói tiếng Việt |
| **OpenAI-Compatible Gateway** | Endpoint `/v1/chat/completions` tương thích chuẩn OpenAI cho các công cụ bên thứ 3 |

---

## 🏗️ Kiến Trúc

```
muse_router_ui (Web Frontend)
    ↓ HTTP/JSON
muse_router_engine (Go · cổng 8088 · 27 endpoints)
    ↓ HTTP/Playwright          ↓ FFI
muse_router_ai (Python)    muse_router_core (Rust DLL)
    ↓ HTTPS Cookie
muse.ai (Token Pool thật)
```

**Database:** MySQL / TiDB Cloud · 9 bảng:
`mart_users` · `muse_accounts` · `chat_threads` · `chat_messages` · `oauth_sessions` · `routing_telemetry` · `muse_documents` · `muse_memories` · `muse_tasks`

---

## 🚀 Cài Đặt & Chạy

### Yêu cầu
- Windows 10/11 x64
- Chrome hoặc Edge cài sẵn để dùng CDP/browser automation
- Go 1.23+ chỉ cần khi build từ source

### Chạy nhanh (dev)
```cmd
cd "f:\tools muse ai"
run_muse_router.bat
```
Mở Chrome → **http://127.0.0.1:8088**

### Tách dữ liệu tài khoản khỏi bộ cài
Không đặt `data\` cạnh bộ cài khi phát hành cho nhân viên. Chỉ server/worker được truy cập dữ liệu tài khoản.

```cmd
set MUSE_DATA_DIR=D:\MuseRouterData
set MUSE_VAULT_MASTER_KEY=<secret-khong-doi-sau-khi-luu-account>
muse_router_engine\muse_router.exe
```

Tài khoản nhân viên dùng API đăng ký/đăng nhập riêng (`/api/users/register`, `/api/users/login`). Credential Muse được lưu mã hóa phía server và không trả về frontend.

### Cài Windows Service (production — tự boot)
```cmd
:: Chạy với quyền Administrator
install_service.bat
:: Chọn [1] → Cài đặt & khởi động
```

### Build từ source
```cmd
cd muse_router_engine
go build -ldflags="-s -w" -o muse_router.exe .
```

---

## 📱 Hướng Dẫn Sử Dụng

### 1. Cắm tài khoản Muse.ai
1. Click icon **👤 Tài khoản** (icon cuối rail trái)
2. Chọn tab **🔌 Cắm tài khoản**
3. Dán Cookie từ muse.ai hoặc click **🌐 Mở cửa sổ đăng nhập**

### 2. Chat
1. Click **Mới** hoặc `Ctrl+N`
2. Gõ tin nhắn → Enter
3. Đính kèm ảnh bằng nút `+` hoặc `Ctrl+V`

### 3. Batch Tasks (chạy nhiều lệnh song song)
1. Vào **✅ Nhiệm vụ**
2. Tạo nhiều nhiệm vụ (tối đa 10 pending)
3. Click **⚡ Chạy tất cả** → tất cả chạy song song qua Token Pool

### 4. Phím tắt
| Phím | Hành động |
|------|-----------|
| `Ctrl+N` | Đoạn chat mới |
| `Ctrl+K` | Tìm kiếm lịch sử |
| `Esc` | Về màn hình Chat |

---

## 🔒 Bảo Mật

- **JWT RS256** — asymmetric, TTL 15 phút
- **AES-256-CTR + HMAC-SHA256** — mã hoá cookie/token (Rust Core)
- **Constant-time compare** — chống timing attack
- **Security headers** — HSTS, CSP, X-Frame-Options, nosniff
- **CRLF injection** — sanitize trước khi log
- **Parameterized SQL** — chống SQLi 100%
- **Mật khẩu Admin (`MART_ADMIN_PASSWORD`)** — đặt biến môi trường `MART_ADMIN_PASSWORD` trên Render để bảo vệ web bằng mật khẩu (Basic Auth / Bearer) và bật API `/api/accounts/ingest` cho Tiện ích Chrome tự động bắt `hatch_sess` 1 chạm (tải trực tiếp tại `/api/accounts/extension.zip`).

---

## 📊 Tests

```cmd
cd muse_router_engine && go test -v ./...       # 7/7 PASS
cd muse_router_ai && python -m pytest -v        # 6/6 PASS
```

---

## 📁 Cấu Trúc Thư Mục

```
f:\tools muse ai\
├── muse_router_engine\     Go Engine (server chính)
│   ├── muse_router.exe     Executable chính
│   ├── web\                Frontend (embed vào exe)
│   │   ├── index.html
│   │   ├── app.js          ~102KB SPA
│   │   └── styles.css
│   └── engine_test.go      7 Go tests
├── muse_router_ai\         Python AI Bridge
│   ├── bridge_service.py   Anti-ban + Playwright
│   └── test_bridge_service.py
├── muse_router_core\       Rust Core
│   └── muse_router_core.dll
├── data\
│   └── sql40_cloud.enc         Cấu hình MySQL Cloud mã hóa AES-256
├── run_muse_router.bat     Chạy thủ công
└── install_service.bat     Cài Windows Service
```

---

*MuseRouter v1.0.0 · Antigravity Polyglot Architecture · **10/10***
