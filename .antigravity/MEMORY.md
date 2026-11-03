# Persistent Memory - MuseRouter (`muse_router`)

- **[2026-11-03] [architecture]** Khởi tạo dự án `muse_router` (`MuseRouter`) tại `f:\tools muse ai` theo chuẩn Antigravity Polyglot 4 module:
  - `muse_router_ui`: C# WPF Desktop Launcher & Controller (`MuseRouter.exe`).
  - `muse_router_engine`: Go Engine (`muse_router.exe`) phục vụ Web UI chuẩn `Chat — Muse` + `Muse Router` Topology Graph + Multi-Account Pool Load Balancer + OpenAI-compatible Gateway (`/v1/chat/completions`).
  - `muse_router_ai`: Python AI Bridge kết nối thực tế tới `https://muse.ai` qua `primp` (TLS Impersonation vượt `?aymh_complete=1`) và `playwright` (`chromium`) kiểm tra xác thực tại `/api/consent/status` & `/api/client-version`, đăng nhập bắt Cookie thật (`/internal/browser-login`), mở cửa sổ gốc cô lập (`/internal/open-native`) và thực thi chat kèm đính kèm ảnh/tệp (`/internal/chat`).
  - `muse_router_core`: Rust Core DLL (`muse_router_core.dll`) mã hóa AES-256-CTR + HMAC-SHA256 cho toàn bộ Cookie/Secret trước khi lưu xuống đĩa, kèm `free_rust_string` / `free_rust_bytes`.
- **[2026-11-03] [zero-fake-data]** Loại bỏ triệt để 100% dữ liệu ảo và thông tin giả lập trên toàn bộ 4 module:
  - Xóa bỏ mọi số liệu cố định (`≈84.192B`, `8`, `8`, `20` trong `MainWindow.xaml`; `100000000` tokens mặc định; `LatencyMs: 45`; `tokensUsed = 64`; `prompt_tokens: 32`) và danh sách model tự đặt (`muse-spark-1.2`, `muse-wonder-1.0`, `muse-code-1.0`).
  - Thiết kế lại giao diện `Chat — Muse` (`web/index.html`, `web/styles.css`, `web/app.js`) khớp với giao diện `https://muse.ai` thực tế của người dùng (hỗ trợ đính kèm ảnh/tệp bằng nút `+` hoặc dán ảnh `Ctrl+V`, bong bóng tin nhắn xanh/xám, cột phải `Muse` hiển thị trạng thái kết nối thực tế và lịch sử `Hôm nay`), kèm header `Cache-Control: no-store` chống lưu cache cũ.
