package main

import (
	"encoding/base64"
	"net/http"
	"os"
	"strings"
)

// adminPassword đọc mật khẩu admin từ biến môi trường MART_ADMIN_PASSWORD (không hard-code, không log).
func adminPassword() string {
	return strings.TrimSpace(os.Getenv("MART_ADMIN_PASSWORD"))
}

// extractAdminCredential lấy mật khẩu từ header Authorization:
//   - "Bearer <mật khẩu>" (extension / API client)
//   - "Basic base64(user:mật khẩu)" (trình duyệt tự hiện hộp thoại đăng nhập)
func extractAdminCredential(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return ""
	}
	lower := strings.ToLower(h)
	switch {
	case strings.HasPrefix(lower, "bearer "):
		return strings.TrimSpace(h[len("bearer "):])
	case strings.HasPrefix(lower, "basic "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len("basic "):]))
		if err != nil {
			return ""
		}
		parts := strings.SplitN(string(raw), ":", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	}
	return ""
}

// isAdminAuthorized so sánh hằng thời gian (chống timing attack) với mật khẩu admin.
func isAdminAuthorized(r *http.Request) bool {
	pw := adminPassword()
	if pw == "" {
		return false
	}
	given := extractAdminCredential(r)
	if given == "" {
		return false
	}
	return ConstantTimeStringEquals(given, pw)
}

// isAdminOrSessionJWTAuthorized kiểm tra mật khẩu admin trực tiếp HOẶC JWT RS256 hợp lệ
// (do /api/auth/session cấp sau khi trình duyệt đã vượt qua Basic Auth).
func isAdminOrSessionJWTAuthorized(r *http.Request, jwtMgr *RS256JWTManager) bool {
	if isAdminAuthorized(r) {
		return true
	}
	if jwtMgr == nil || r.URL.Path == "/api/accounts/ingest" {
		return false
	}
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		tok := strings.TrimSpace(h[len("bearer "):])
		if strings.Count(tok, ".") == 2 {
			if _, err := jwtMgr.VerifyToken(tok); err == nil {
				return true
			}
		}
	}
	return false
}

// AdminAuthMiddleware bảo vệ toàn bộ web khi đã đặt MART_ADMIN_PASSWORD.
// Khi chưa đặt biến này, hành vi giữ nguyên như cũ (mở), trừ API nhận cookie /api/accounts/ingest
// luôn bị từ chối để không ai thêm tài khoản vào Pool mà không có mật khẩu.
func AdminAuthMiddleware(next http.Handler) http.Handler {
	return AdminAuthMiddlewareWithJWT(next, nil)
}

func AdminAuthMiddlewareWithJWT(next http.Handler, jwtMgr *RS256JWTManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if adminPassword() == "" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		if !isAdminOrSessionJWTAuthorized(r, jwtMgr) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Mart AI Admin", charset="UTF-8"`)
			WriteJSONError(w, http.StatusUnauthorized, "ERR_UNAUTHORIZED")
			return
		}
		next.ServeHTTP(w, r)
	})
}
