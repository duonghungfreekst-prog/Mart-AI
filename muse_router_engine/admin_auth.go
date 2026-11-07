package main

import (
	"encoding/base64"
	"net/http"
	"os"
	"strings"
)

// adminPassword Ä‘á»c máº­t kháº©u admin tá»« biáº¿n mÃ´i trÆ°á»ng MART_ADMIN_PASSWORD (khÃ´ng hard-code, khÃ´ng log).
func adminPassword() string {
	return strings.TrimSpace(os.Getenv("MART_ADMIN_PASSWORD"))
}

// extractAdminCredential láº¥y máº­t kháº©u tá»« header Authorization:
//   - "Bearer <máº­t kháº©u>" (extension / API client)
//   - "Basic base64(user:máº­t kháº©u)" (trÃ¬nh duyá»‡t tá»± hiá»‡n há»™p thoáº¡i Ä‘Äƒng nháº­p)
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

// isAdminAuthorized so sÃ¡nh háº±ng thá»i gian (chá»‘ng timing attack) vá»›i máº­t kháº©u admin.
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

// isAdminOrSessionJWTAuthorized kiá»ƒm tra máº­t kháº©u admin trá»±c tiáº¿p HOáº¶C JWT RS256 há»£p lá»‡
// (do /api/auth/session cáº¥p sau khi trÃ¬nh duyá»‡t Ä‘Ã£ vÆ°á»£t qua Basic Auth).
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

// AdminAuthMiddleware báº£o vá»‡ toÃ n bá»™ web khi Ä‘Ã£ Ä‘áº·t MART_ADMIN_PASSWORD.
// Khi chÆ°a Ä‘áº·t biáº¿n nÃ y, hÃ nh vi giá»¯ nguyÃªn nhÆ° cÅ© (má»Ÿ), trá»« API nháº­n cookie /api/accounts/ingest
// luÃ´n bá»‹ tá»« chá»‘i Ä‘á»ƒ khÃ´ng ai thÃªm tÃ i khoáº£n vÃ o Pool mÃ  khÃ´ng cÃ³ máº­t kháº©u.
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
