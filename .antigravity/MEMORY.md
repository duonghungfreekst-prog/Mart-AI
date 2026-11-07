# Persistent Memory - MuseRouter

- **[2026-11-07] [architecture]** Dự án đã chuyển sang Go-Native Single Binary. `muse_router_engine` là runtime và build target duy nhất; web frontend được embed trong Go executable.
- Python Bridge, Rust Core và C# WPF đã bị loại khỏi runtime/build chính. Lớp `Vault` native Go giữ format mã hóa tương thích dữ liệu cũ.
- Dữ liệu cần bảo toàn nằm trong `data\`. Không reset, truncate hoặc xóa dữ liệu production khi test.
- Build: `cd muse_router_engine && go build -trimpath -ldflags="-s -w" -o muse_router.exe .`
- Kiểm tra bắt buộc: `go test ./...` và `go vet ./...`.
- Render cần các secret environment: `DATABASE_URL`, `MUSE_VAULT_MASTER_KEY`, `MART_ADMIN_PASSWORD`, `MUSE_INTERNAL_BRIDGE_TOKEN`. Chromium chỉ cần trong worker/browser deployment.
- Các file Go lớn cần refactor dần, ưu tiên tách `router_pool.go`, `cdp_browser.go`, `mysql_store.go`, `user_auth.go` thành package nội bộ sau khi có test regression đầy đủ.
