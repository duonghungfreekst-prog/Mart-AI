package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type actorCtxKeyType struct{}

var actorCtxKey = actorCtxKeyType{}

var validUsernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,32}$`)

type RequestActor struct {
	UserID        string `json:"id"`
	Username      string `json:"username"`
	DisplayName   string `json:"display_name"`
	Role          string `json:"role"` // "admin" | "user"
	Authenticated bool   `json:"authenticated"`
}

// OwnerScope trả về khóa cô lập dữ liệu:
// - Tài khoản người dùng ("user"): trả về đúng ID riêng ("usr_...")
// - Quản trị viên ("admin"): trả về "" để quản trị không gian gốc của Admin
func (act RequestActor) OwnerScope() string {
	if act.Role == "admin" {
		return ""
	}
	return act.UserID
}

func randomHexID(prefix string, numBytes int) string {
	if numBytes <= 0 {
		numBytes = 8
	}
	buf := make([]byte, numBytes)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

func nonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func queryRowsToMaps(res *LibSQLQueryResult) []map[string]string {
	if res == nil || len(res.Rows) == 0 {
		return []map[string]string{}
	}
	out := make([]map[string]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		m := make(map[string]string, len(res.Columns))
		for idx, col := range res.Columns {
			if idx < len(row) && row[idx] != nil {
				m[col] = fmt.Sprintf("%v", row[idx])
			} else {
				m[col] = ""
			}
		}
		out = append(out, m)
	}
	return out
}

func ownerWhereClause(col string, ownerScope string) (string, []interface{}) {
	if ownerScope == "" {
		return fmt.Sprintf("(%s = '' OR %s = 'admin')", col, col), nil
	}
	return fmt.Sprintf("%s = ?", col), []interface{}{ownerScope}
}

func (a *ServerApp) isUserAccountActive(userID string) bool {
	if strings.TrimSpace(userID) == "" {
		return false
	}
	cacheKey := "usr_active:" + userID
	if cached, ok := a.cache.Get(cacheKey); ok {
		if active, valid := cached.(bool); valid {
			return active
		}
	}
	store := a.pool.GetLibSQLStore()
	if store == nil {
		return false
	}
	qRes, err := store.ExecuteSQL(
		"SELECT is_disabled FROM mart_users WHERE id = ? LIMIT 1",
		[]interface{}{userID},
	)
	if err != nil || len(qRes.Rows) == 0 {
		a.cache.Set(cacheKey, false, 15*time.Second)
		return false
	}
	rows := queryRowsToMaps(qRes)
	active := rows[0]["is_disabled"] == "0" || rows[0]["is_disabled"] == ""
	a.cache.Set(cacheKey, active, 30*time.Second)
	return active
}

func (a *ServerApp) invalidateUserActiveCache(userID string) {
	if a.cache != nil && userID != "" {
		a.cache.store.Delete("usr_active:" + userID)
	}
}

func (a *ServerApp) resolveActor(r *http.Request) RequestActor {
	if r == nil {
		return RequestActor{}
	}
	if v := r.Context().Value(actorCtxKey); v != nil {
		if act, ok := v.(RequestActor); ok {
			return act
		}
	}

	if isAdminAuthorized(r) {
		return RequestActor{
			UserID:        "admin",
			Username:      "admin",
			DisplayName:   "Quản trị viên",
			Role:          "admin",
			Authenticated: true,
		}
	}

	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(h), "bearer ") && a.jwtMgr != nil {
		tok := strings.TrimSpace(h[len("bearer "):])
		if strings.Count(tok, ".") == 2 {
			if claims, err := a.jwtMgr.VerifyToken(tok); err == nil && claims != nil {
				role := strings.ToLower(strings.TrimSpace(claims.Role))
				if role == "admin" || (role == "" && (claims.Sub == "muse_router_operator" || claims.Sub == "admin" || claims.Sub == "admin_user")) {
					return RequestActor{
						UserID:        "admin",
						Username:      nonEmptyStr(claims.Username, "admin"),
						DisplayName:   nonEmptyStr(claims.Name, "Quản trị viên"),
						Role:          "admin",
						Authenticated: true,
					}
				}
				if role == "user" && strings.TrimSpace(claims.Sub) != "" {
					if a.isUserAccountActive(claims.Sub) {
						return RequestActor{
							UserID:        claims.Sub,
							Username:      nonEmptyStr(claims.Username, claims.Sub),
							DisplayName:   nonEmptyStr(claims.Name, claims.Username, claims.Sub),
							Role:          "user",
							Authenticated: true,
						}
					}
				}
			}
		}
	}

	return RequestActor{Authenticated: false}
}

func isPublicRoute(path string) bool {
	switch path {
	case "/api/health",
		"/api/users/register",
		"/api/users/login",
		"/api/users/admin-login",
		"/api/users/me":
		return true
	}
	if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/v1/") && !strings.HasPrefix(path, "/v2/") {
		return true
	}
	return false
}

func isAdminOnlyRoute(path string) bool {
	if strings.HasPrefix(path, "/api/accounts/") ||
		strings.HasPrefix(path, "/api/libsql/") ||
		strings.HasPrefix(path, "/api/sql/") ||
		strings.HasPrefix(path, "/api/admin/") ||
		path == "/api/router/config" ||
		path == "/v2/pipeline" {
		return true
	}
	return false
}

// AuthAndRoleMiddleware phân tách rạch ròi quyền truy cập Public / User (cô lập từng tài khoản) / Admin.
func (a *ServerApp) AuthAndRoleMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		p := r.URL.Path
		if p == "/api/accounts/ingest" {
			// handleIngestAccount tự xác thực trực tiếp bằng MART_ADMIN_PASSWORD (không nhận session JWT)
			next.ServeHTTP(w, r)
			return
		}

		if isPublicRoute(p) {
			next.ServeHTTP(w, r)
			return
		}

		actor := a.resolveActor(r)
		isWebClient := strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Mart-Client")), "web")

		if isAdminOnlyRoute(p) {
			if actor.Authenticated && actor.Role != "admin" {
				WriteJSONError(w, http.StatusForbidden, "ERR_FORBIDDEN_ADMIN_ONLY")
				return
			}
			if actor.Authenticated && actor.Role == "admin" {
				ctx := context.WithValue(r.Context(), actorCtxKey, actor)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if adminPassword() != "" {
				if !isWebClient {
					w.Header().Set("WWW-Authenticate", `Basic realm="Mart AI Admin", charset="UTF-8"`)
				}
				WriteJSONError(w, http.StatusUnauthorized, "ERR_ADMIN_AUTH_REQUIRED")
				return
			}
			if isWebClient {
				WriteJSONError(w, http.StatusUnauthorized, "ERR_ADMIN_AUTH_REQUIRED")
				return
			}
			localAdmin := RequestActor{
				UserID:        "admin",
				Username:      "admin",
				DisplayName:   "Quản trị viên",
				Role:          "admin",
				Authenticated: true,
			}
			ctx := context.WithValue(r.Context(), actorCtxKey, localAdmin)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Nhóm API người dùng (/api/threads*, /api/chat/*, /api/workspace/*, /api/tasks/*, /api/router/summary, /v1/*)
		if actor.Authenticated {
			ctx := context.WithValue(r.Context(), actorCtxKey, actor)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if isWebClient {
			WriteJSONError(w, http.StatusUnauthorized, "ERR_AUTH_REQUIRED")
			return
		}

		if adminPassword() != "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="Mart AI Admin", charset="UTF-8"`)
			WriteJSONError(w, http.StatusUnauthorized, "ERR_UNAUTHORIZED")
			return
		}

		localAdmin := RequestActor{
			UserID:        "admin",
			Username:      "admin",
			DisplayName:   "Quản trị viên",
			Role:          "admin",
			Authenticated: true,
		}
		ctx := context.WithValue(r.Context(), actorCtxKey, localAdmin)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ============================================================================
// ĐĂNG KÝ / ĐĂNG NHẬP / HỒ SƠ TÀI KHOẢN NGƯỜI DÙNG
// ============================================================================

func (a *ServerApp) handleUserRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	var req struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if err := a.readJSONBody(r, &req); err != nil {
		WriteJSONError(w, http.StatusBadRequest, "Dữ liệu đăng ký không hợp lệ.")
		return
	}

	username := strings.ToLower(strings.TrimSpace(req.Username))
	if !validUsernameRegex.MatchString(username) {
		WriteJSONError(w, http.StatusBadRequest, "Tên đăng nhập phải từ 3–32 ký tự (chữ cái không dấu, số, dấu chấm, gạch ngang hoặc gạch dưới).")
		return
	}
	if username == "admin" || username == "root" || username == "system" {
		WriteJSONError(w, http.StatusBadRequest, "Tên đăng nhập này được dành riêng cho hệ thống.")
		return
	}
	if len(req.Password) < 6 || len(req.Password) > 128 {
		WriteJSONError(w, http.StatusBadRequest, "Mật khẩu phải có từ 6 đến 128 ký tự.")
		return
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = username
	}
	if len([]rune(displayName)) > 60 {
		displayName = string([]rune(displayName)[:60])
	}

	store := a.pool.GetLibSQLStore()
	if store == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "Cơ sở dữ liệu chưa sẵn sàng.")
		return
	}

	dupRes, err := store.ExecuteSQL(
		"SELECT id FROM mart_users WHERE username = ? LIMIT 1",
		[]interface{}{username},
	)
	if err != nil {
		LogError("Check duplicate user error: %v", err)
		WriteJSONError(w, http.StatusInternalServerError, "ERR_DB_QUERY")
		return
	}
	if len(dupRes.Rows) > 0 {
		WriteJSONError(w, http.StatusConflict, "Tên tài khoản này đã tồn tại — vui lòng chọn tên khác hoặc chuyển sang Đăng nhập.")
		return
	}

	pwHash, err := HashPasswordPBKDF2(req.Password)
	if err != nil {
		LogError("HashPasswordPBKDF2 error: %v", err)
		WriteJSONError(w, http.StatusInternalServerError, "ERR_PASSWORD_HASH")
		return
	}

	userID := randomHexID("usr_", 8)
	nowStr := time.Now().Format("2006-01-02 15:04:05")
	if _, err := store.ExecuteSQL(
		`INSERT INTO mart_users(id, username, display_name, password_hash, role, is_disabled, created_at, last_login_at)
		 VALUES(?, ?, ?, ?, 'user', 0, ?, ?)`,
		[]interface{}{userID, username, displayName, pwHash, nowStr, nowStr},
	); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			WriteJSONError(w, http.StatusConflict, "Tên tài khoản này đã tồn tại.")
			return
		}
		LogError("Insert mart_users error: %v", err)
		WriteJSONError(w, http.StatusInternalServerError, "ERR_CREATE_USER_FAILED")
		return
	}

	a.cache.Set("usr_active:"+userID, true, 60*time.Second)

	token, err := a.jwtMgr.IssueUserToken(userID, username, displayName, "user", 168*time.Hour)
	if err != nil {
		LogError("IssueUserToken error: %v", err)
		WriteJSONError(w, http.StatusInternalServerError, "ERR_TOKEN_ISSUE_FAILED")
		return
	}

	LogInfo("Registered new isolated user account: username=%s id=%s", SanitizeLog(username), userID)
	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
		"token": token,
		"alg":   "RS256",
		"user": map[string]interface{}{
			"id":           userID,
			"username":     username,
			"display_name": displayName,
			"role":         "user",
		},
	})
}

func (a *ServerApp) handleUserLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := a.readJSONBody(r, &req); err != nil {
		WriteJSONError(w, http.StatusBadRequest, "Thông tin đăng nhập không hợp lệ.")
		return
	}

	username := strings.ToLower(strings.TrimSpace(req.Username))
	if username == "" || req.Password == "" {
		WriteJSONError(w, http.StatusBadRequest, "Vui lòng nhập đầy đủ tên đăng nhập và mật khẩu.")
		return
	}

	// Hỗ trợ Quản trị viên đăng nhập bằng username "admin" + MART_ADMIN_PASSWORD (khi có đặt biến môi trường)
	if username == "admin" && adminPassword() != "" && ConstantTimeStringEquals(req.Password, adminPassword()) {
		token, err := a.jwtMgr.IssueUserToken("admin", "admin", "Quản trị viên", "admin", 168*time.Hour)
		if err != nil {
			WriteJSONError(w, http.StatusInternalServerError, "ERR_TOKEN_ISSUE_FAILED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"token": token,
			"alg":   "RS256",
			"user": map[string]interface{}{
				"id":           "admin",
				"username":     "admin",
				"display_name": "Quản trị viên",
				"role":         "admin",
			},
		})
		return
	}

	store := a.pool.GetLibSQLStore()
	if store == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "Cơ sở dữ liệu chưa sẵn sàng.")
		return
	}

	qRes, err := store.ExecuteSQL(
		"SELECT id, username, display_name, password_hash, role, is_disabled FROM mart_users WHERE username = ? LIMIT 1",
		[]interface{}{username},
	)
	if err != nil || len(qRes.Rows) == 0 {
		WriteJSONError(w, http.StatusUnauthorized, "Sai tên đăng nhập hoặc mật khẩu.")
		return
	}
	row := queryRowsToMaps(qRes)[0]
	if row["is_disabled"] == "1" {
		WriteJSONError(w, http.StatusForbidden, "Tài khoản này đang bị tạm khóa bởi Quản trị viên.")
		return
	}
	if !VerifyPasswordPBKDF2(req.Password, row["password_hash"]) {
		WriteJSONError(w, http.StatusUnauthorized, "Sai tên đăng nhập hoặc mật khẩu.")
		return
	}

	userID := row["id"]
	role := nonEmptyStr(row["role"], "user")
	displayName := nonEmptyStr(row["display_name"], username)

	nowStr := time.Now().Format("2006-01-02 15:04:05")
	_, _ = store.ExecuteSQL("UPDATE mart_users SET last_login_at = ? WHERE id = ?", []interface{}{nowStr, userID})
	a.cache.Set("usr_active:"+userID, true, 60*time.Second)

	token, err := a.jwtMgr.IssueUserToken(userID, username, displayName, role, 168*time.Hour)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "ERR_TOKEN_ISSUE_FAILED")
		return
	}

	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
		"token": token,
		"alg":   "RS256",
		"user": map[string]interface{}{
			"id":           userID,
			"username":     username,
			"display_name": displayName,
			"role":         role,
		},
	})
}

func (a *ServerApp) handleAdminUnlockLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	_ = a.readJSONBody(r, &req)

	expected := adminPassword()
	if expected != "" {
		if !ConstantTimeStringEquals(strings.TrimSpace(req.Password), expected) && !isAdminAuthorized(r) {
			WriteJSONError(w, http.StatusUnauthorized, "Mật khẩu Quản trị viên không chính xác.")
			return
		}
	}

	token, err := a.jwtMgr.IssueUserToken("admin", "admin", "Quản trị viên", "admin", 168*time.Hour)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "ERR_TOKEN_ISSUE_FAILED")
		return
	}
	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
		"token": token,
		"alg":   "RS256",
		"user": map[string]interface{}{
			"id":           "admin",
			"username":     "admin",
			"display_name": "Quản trị viên",
			"role":         "admin",
		},
	})
}

func (a *ServerApp) handleUserMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	actor := a.resolveActor(r)
	if !actor.Authenticated {
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"authenticated":           false,
			"admin_password_required": adminPassword() != "",
		})
		return
	}
	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
		"authenticated":           true,
		"admin_password_required": adminPassword() != "",
		"user": map[string]interface{}{
			"id":           actor.UserID,
			"username":     actor.Username,
			"display_name": actor.DisplayName,
			"role":         actor.Role,
		},
	})
}

func (a *ServerApp) handleUserChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	actor := a.resolveActor(r)
	if !actor.Authenticated || actor.Role != "user" {
		WriteJSONError(w, http.StatusBadRequest, "Chỉ tài khoản người dùng mới đổi mật khẩu tại đây.")
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		DisplayName     string `json:"display_name"`
	}
	if err := a.readJSONBody(r, &req); err != nil {
		WriteJSONError(w, http.StatusBadRequest, "Dữ liệu không hợp lệ.")
		return
	}
	if len(req.NewPassword) < 6 || len(req.NewPassword) > 128 {
		WriteJSONError(w, http.StatusBadRequest, "Mật khẩu mới phải từ 6 đến 128 ký tự.")
		return
	}

	store := a.pool.GetLibSQLStore()
	if store == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "ERR_LIBSQL_NOT_INITIALIZED")
		return
	}
	qRes, err := store.ExecuteSQL("SELECT password_hash, display_name FROM mart_users WHERE id = ? LIMIT 1", []interface{}{actor.UserID})
	if err != nil || len(qRes.Rows) == 0 {
		WriteJSONError(w, http.StatusNotFound, "Không tìm thấy tài khoản.")
		return
	}
	row := queryRowsToMaps(qRes)[0]
	if !VerifyPasswordPBKDF2(req.CurrentPassword, row["password_hash"]) {
		WriteJSONError(w, http.StatusUnauthorized, "Mật khẩu hiện tại không đúng.")
		return
	}
	newHash, err := HashPasswordPBKDF2(req.NewPassword)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "ERR_PASSWORD_HASH")
		return
	}
	newName := nonEmptyStr(req.DisplayName, row["display_name"], actor.Username)
	if len([]rune(newName)) > 60 {
		newName = string([]rune(newName)[:60])
	}
	if _, err := store.ExecuteSQL(
		"UPDATE mart_users SET password_hash = ?, display_name = ? WHERE id = ?",
		[]interface{}{newHash, newName, actor.UserID},
	); err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "ERR_UPDATE_PASSWORD_FAILED")
		return
	}
	token, _ := a.jwtMgr.IssueUserToken(actor.UserID, actor.Username, newName, "user", 168*time.Hour)
	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
		"updated": true,
		"token":   token,
		"user": map[string]interface{}{
			"id":           actor.UserID,
			"username":     actor.Username,
			"display_name": newName,
			"role":         "user",
		},
	})
}

// ============================================================================
// QUẢN TRỊ DANH SÁCH TÀI KHOẢN NGƯỜI DÙNG (/api/admin/users*)
// ============================================================================

func (a *ServerApp) handleAdminUsersList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	store := a.pool.GetLibSQLStore()
	if store == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "ERR_LIBSQL_NOT_INITIALIZED")
		return
	}
	qRes, err := store.ExecuteSQL(
		`SELECT u.id, u.username, u.display_name, u.role, u.is_disabled, u.created_at, u.last_login_at,
		        (SELECT COUNT(1) FROM chat_threads t WHERE t.owner_id = u.id) AS thread_count,
		        (SELECT COUNT(1) FROM muse_documents d WHERE d.owner_id = u.id) AS doc_count,
		        (SELECT COUNT(1) FROM muse_tasks k WHERE k.owner_id = u.id) AS task_count
		 FROM mart_users u
		 ORDER BY u.created_at DESC`,
		nil,
	)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "ERR_QUERY_USERS_FAILED")
		return
	}
	rows := queryRowsToMaps(qRes)
	users := make([]map[string]interface{}, 0, len(rows))
	for _, m := range rows {
		tc, _ := strconv.Atoi(m["thread_count"])
		dc, _ := strconv.Atoi(m["doc_count"])
		kc, _ := strconv.Atoi(m["task_count"])
		users = append(users, map[string]interface{}{
			"id":            m["id"],
			"username":      m["username"],
			"display_name":  m["display_name"],
			"role":          m["role"],
			"is_disabled":   m["is_disabled"] == "1",
			"created_at":    m["created_at"],
			"last_login_at": m["last_login_at"],
			"thread_count":  tc,
			"doc_count":     dc,
			"task_count":    kc,
		})
	}
	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
		"users": users,
		"total": len(users),
	})
}

func (a *ServerApp) handleAdminUserToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	var req struct {
		UserID     string `json:"user_id"`
		IsDisabled bool   `json:"is_disabled"`
	}
	if err := a.readJSONBody(r, &req); err != nil || strings.TrimSpace(req.UserID) == "" {
		WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}
	store := a.pool.GetLibSQLStore()
	if store == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "ERR_LIBSQL_NOT_INITIALIZED")
		return
	}
	disVal := 0
	if req.IsDisabled {
		disVal = 1
	}
	if _, err := store.ExecuteSQL(
		"UPDATE mart_users SET is_disabled = ? WHERE id = ?",
		[]interface{}{disVal, strings.TrimSpace(req.UserID)},
	); err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "ERR_UPDATE_USER_FAILED")
		return
	}
	a.invalidateUserActiveCache(strings.TrimSpace(req.UserID))
	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"updated": true})
}

func (a *ServerApp) handleAdminUserResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	var req struct {
		UserID      string `json:"user_id"`
		NewPassword string `json:"new_password"`
	}
	if err := a.readJSONBody(r, &req); err != nil || strings.TrimSpace(req.UserID) == "" {
		WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}
	if len(req.NewPassword) < 6 || len(req.NewPassword) > 128 {
		WriteJSONError(w, http.StatusBadRequest, "Mật khẩu mới phải từ 6 đến 128 ký tự.")
		return
	}
	pwHash, err := HashPasswordPBKDF2(req.NewPassword)
	if err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "ERR_PASSWORD_HASH")
		return
	}
	store := a.pool.GetLibSQLStore()
	if store == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "ERR_LIBSQL_NOT_INITIALIZED")
		return
	}
	if _, err := store.ExecuteSQL(
		"UPDATE mart_users SET password_hash = ? WHERE id = ?",
		[]interface{}{pwHash, strings.TrimSpace(req.UserID)},
	); err != nil {
		WriteJSONError(w, http.StatusInternalServerError, "ERR_UPDATE_USER_FAILED")
		return
	}
	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"updated": true})
}

func (a *ServerApp) handleAdminUserDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := a.readJSONBody(r, &req); err != nil || strings.TrimSpace(req.UserID) == "" {
		WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}
	uid := strings.TrimSpace(req.UserID)
	if uid == "admin" {
		WriteJSONError(w, http.StatusBadRequest, "Không thể xóa tài khoản Quản trị viên gốc.")
		return
	}
	store := a.pool.GetLibSQLStore()
	if store == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "ERR_LIBSQL_NOT_INITIALIZED")
		return
	}

	// Xóa sạch dữ liệu của riêng tài khoản này, tuyệt đối không ảnh hưởng bất kỳ tài khoản nào khác
	_, _ = store.ExecuteSQL("DELETE FROM chat_messages WHERE thread_id IN (SELECT id FROM chat_threads WHERE owner_id = ?)", []interface{}{uid})
	_, _ = store.ExecuteSQL("DELETE FROM chat_threads WHERE owner_id = ?", []interface{}{uid})
	_, _ = store.ExecuteSQL("DELETE FROM muse_documents WHERE owner_id = ?", []interface{}{uid})
	_, _ = store.ExecuteSQL("DELETE FROM muse_memories WHERE owner_id = ?", []interface{}{uid})
	_, _ = store.ExecuteSQL("DELETE FROM muse_tasks WHERE owner_id = ?", []interface{}{uid})
	_, _ = store.ExecuteSQL("DELETE FROM mart_users WHERE id = ?", []interface{}{uid})

	a.pool.PurgeOwnerThreadsInMemory(uid)
	a.invalidateUserActiveCache(uid)

	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"deleted": true})
}

// ============================================================================
// WORKSPACE CÔ LẬP TUYỆT ĐỐI THEO TÀI KHOẢN (/api/workspace/*)
// ============================================================================

func (a *ServerApp) ensureScopedRecordID(table string, requestedID string, prefix string, ownerScope string) (string, error) {
	store := a.pool.GetLibSQLStore()
	if store == nil {
		return "", fmt.Errorf("ERR_LIBSQL_NOT_INITIALIZED")
	}
	cleanID := strings.TrimSpace(requestedID)
	if cleanID == "" {
		return randomHexID(prefix, 8), nil
	}
	qRes, err := store.ExecuteSQL(fmt.Sprintf("SELECT owner_id FROM %s WHERE id = ? LIMIT 1", table), []interface{}{cleanID})
	if err == nil && len(qRes.Rows) > 0 {
		existingOwner := queryRowsToMaps(qRes)[0]["owner_id"]
		if !ownerMatchesThread(existingOwner, ownerScope) {
			// Không cho phép ghi đè ID của tài khoản khác — cấp ID độc lập mới cho tài khoản hiện tại
			return randomHexID(prefix, 8), nil
		}
	}
	return cleanID, nil
}

func (a *ServerApp) handleWorkspaceSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	store := a.pool.GetLibSQLStore()
	if store == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "ERR_LIBSQL_NOT_INITIALIZED")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"threads":  []map[string]string{},
			"messages": []map[string]string{},
		})
		return
	}
	if _, err := ValidateAndSanitizeInput(q, 512); err != nil {
		WriteJSONError(w, http.StatusBadRequest, "Từ khóa tìm kiếm không hợp lệ.")
		return
	}

	actor := a.resolveActor(r)
	ownerScope := actor.OwnerScope()
	like := "%" + q + "%"

	tClause, tArgs := ownerWhereClause("owner_id", ownerScope)
	tQueryArgs := append([]interface{}{like}, tArgs...)
	trRes, _ := store.ExecuteSQL(
		"SELECT id, title, updated_at FROM chat_threads WHERE title LIKE ? AND "+tClause+" ORDER BY updated_at DESC LIMIT 20",
		tQueryArgs,
	)

	mClause, mArgs := ownerWhereClause("t.owner_id", ownerScope)
	mQueryArgs := append([]interface{}{like}, mArgs...)
	msgRes, _ := store.ExecuteSQL(
		`SELECT m.id, m.thread_id, m.role, substr(m.content, 1, 200) AS snippet, m.timestamp, t.title AS thread_title
		 FROM chat_messages m
		 INNER JOIN chat_threads t ON t.id = m.thread_id
		 WHERE m.content LIKE ? AND `+mClause+`
		 ORDER BY m.timestamp DESC LIMIT 30`,
		mQueryArgs,
	)

	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
		"threads":  queryRowsToMaps(trRes),
		"messages": queryRowsToMaps(msgRes),
	})
}

func (a *ServerApp) handleWorkspaceDocs(w http.ResponseWriter, r *http.Request) {
	store := a.pool.GetLibSQLStore()
	if store == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "ERR_LIBSQL_NOT_INITIALIZED")
		return
	}
	actor := a.resolveActor(r)
	ownerScope := actor.OwnerScope()
	whereSQL, whereArgs := ownerWhereClause("owner_id", ownerScope)

	if r.Method == http.MethodGet {
		docID := strings.TrimSpace(r.URL.Query().Get("id"))
		if docID != "" {
			args := append([]interface{}{docID}, whereArgs...)
			res, err := store.ExecuteSQL(
				"SELECT id, title, content, source_thread_id, updated_at FROM muse_documents WHERE id = ? AND "+whereSQL+" LIMIT 1",
				args,
			)
			if err != nil {
				WriteJSONError(w, http.StatusBadRequest, "ERR_QUERY_DOC_FAILED")
				return
			}
			rows := queryRowsToMaps(res)
			if len(rows) == 0 {
				WriteJSONError(w, http.StatusNotFound, "ERR_DOC_NOT_FOUND")
				return
			}
			WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"doc": rows[0]})
			return
		}

		orderDir := "DESC"
		if strings.EqualFold(r.URL.Query().Get("sort"), "ASC") {
			orderDir = "ASC"
		}
		fullContent := r.URL.Query().Get("full") == "1"
		selectCols := "id, title, substr(content, 1, 200) AS preview, updated_at"
		if fullContent {
			selectCols = "id, title, content, updated_at"
		}
		res, err := store.ExecuteSQL(
			fmt.Sprintf("SELECT %s FROM muse_documents WHERE %s ORDER BY updated_at %s LIMIT 100", selectCols, whereSQL, orderDir),
			whereArgs,
		)
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_LIST_DOCS_FAILED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"docs": queryRowsToMaps(res)})
		return
	}

	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}

	var req struct {
		Action         string `json:"action"` // "save" | "delete"
		ID             string `json:"id"`
		Title          string `json:"title"`
		Content        string `json:"content"`
		SourceThreadID string `json:"source_thread_id"`
	}
	if err := a.readJSONBody(r, &req); err != nil {
		WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}

	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "delete":
		if strings.TrimSpace(req.ID) == "" {
			WriteJSONError(w, http.StatusBadRequest, "ERR_MISSING_ID")
			return
		}
		args := append([]interface{}{strings.TrimSpace(req.ID)}, whereArgs...)
		_, _ = store.ExecuteSQL("DELETE FROM muse_documents WHERE id = ? AND "+whereSQL, args)
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"deleted": true})
		return

	default: // "save"
		title := strings.TrimSpace(req.Title)
		content := strings.TrimSpace(req.Content)
		if title == "" || content == "" {
			WriteJSONError(w, http.StatusBadRequest, "Vui lòng nhập đầy đủ tiêu đề và nội dung tài liệu.")
			return
		}
		docID, err := a.ensureScopedRecordID("muse_documents", req.ID, "doc_", ownerScope)
		if err != nil {
			WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		nowStr := time.Now().Format("2006-01-02 15:04:05")
		if _, err := store.ExecuteSQL(
			`INSERT INTO muse_documents(id, owner_id, title, content, source_thread_id, updated_at)
			 VALUES(?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
			   title = excluded.title,
			   content = excluded.content,
			   updated_at = excluded.updated_at`,
			[]interface{}{docID, ownerScope, title, content, strings.TrimSpace(req.SourceThreadID), nowStr},
		); err != nil {
			WriteJSONError(w, http.StatusInternalServerError, "ERR_SAVE_DOC_FAILED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"saved": true,
			"id":    docID,
		})
	}
}

func (a *ServerApp) handleWorkspaceMemories(w http.ResponseWriter, r *http.Request) {
	store := a.pool.GetLibSQLStore()
	if store == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "ERR_LIBSQL_NOT_INITIALIZED")
		return
	}
	actor := a.resolveActor(r)
	ownerScope := actor.OwnerScope()
	whereSQL, whereArgs := ownerWhereClause("owner_id", ownerScope)

	if r.Method == http.MethodGet {
		activeOnly := r.URL.Query().Get("active_only") == "1"
		sqlStr := "SELECT id, title, content, is_active, created_at FROM muse_memories WHERE " + whereSQL + " ORDER BY created_at DESC LIMIT 100"
		if activeOnly {
			sqlStr = "SELECT id, title, content, is_active, created_at FROM muse_memories WHERE is_active = 1 AND " + whereSQL + " ORDER BY created_at ASC LIMIT 10"
		}
		res, err := store.ExecuteSQL(sqlStr, whereArgs)
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_LIST_MEMORIES_FAILED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"memories": queryRowsToMaps(res)})
		return
	}

	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}

	var req struct {
		Action   string `json:"action"` // "save" | "toggle" | "delete"
		ID       string `json:"id"`
		Title    string `json:"title"`
		Content  string `json:"content"`
		IsActive int    `json:"is_active"`
	}
	if err := a.readJSONBody(r, &req); err != nil {
		WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}

	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "toggle":
		val := 0
		if req.IsActive != 0 {
			val = 1
		}
		args := append([]interface{}{val, strings.TrimSpace(req.ID)}, whereArgs...)
		_, _ = store.ExecuteSQL("UPDATE muse_memories SET is_active = ? WHERE id = ? AND "+whereSQL, args)
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"updated": true})
		return

	case "delete":
		args := append([]interface{}{strings.TrimSpace(req.ID)}, whereArgs...)
		_, _ = store.ExecuteSQL("DELETE FROM muse_memories WHERE id = ? AND "+whereSQL, args)
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"deleted": true})
		return

	default: // "save"
		title := strings.TrimSpace(req.Title)
		content := strings.TrimSpace(req.Content)
		if title == "" || content == "" {
			WriteJSONError(w, http.StatusBadRequest, "Vui lòng nhập đầy đủ tiêu đề và nội dung bộ nhớ.")
			return
		}
		memID, err := a.ensureScopedRecordID("muse_memories", req.ID, "mem_", ownerScope)
		if err != nil {
			WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		nowStr := time.Now().Format("2006-01-02 15:04:05")
		if _, err := store.ExecuteSQL(
			`INSERT INTO muse_memories(id, owner_id, title, content, is_active, created_at)
			 VALUES(?, ?, ?, ?, 1, ?)
			 ON CONFLICT(id) DO UPDATE SET
			   title = excluded.title,
			   content = excluded.content`,
			[]interface{}{memID, ownerScope, title, content, nowStr},
		); err != nil {
			WriteJSONError(w, http.StatusInternalServerError, "ERR_SAVE_MEMORY_FAILED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"saved": true,
			"id":    memID,
		})
	}
}

func (a *ServerApp) handleWorkspaceTasks(w http.ResponseWriter, r *http.Request) {
	store := a.pool.GetLibSQLStore()
	if store == nil {
		WriteJSONError(w, http.StatusServiceUnavailable, "ERR_LIBSQL_NOT_INITIALIZED")
		return
	}
	actor := a.resolveActor(r)
	ownerScope := actor.OwnerScope()
	whereSQL, whereArgs := ownerWhereClause("owner_id", ownerScope)

	if r.Method == http.MethodGet {
		statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
		orderDir := "DESC"
		if strings.EqualFold(r.URL.Query().Get("sort"), "ASC") {
			orderDir = "ASC"
		}
		limit := 100
		if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 100 {
			limit = l
		}
		var sqlStr string
		var args []interface{}
		if statusFilter != "" && statusFilter != "all" {
			sqlStr = fmt.Sprintf(
				"SELECT id, title, prompt, status, result_summary, updated_at FROM muse_tasks WHERE status = ? AND %s ORDER BY updated_at %s LIMIT %d",
				whereSQL, orderDir, limit,
			)
			args = append([]interface{}{statusFilter}, whereArgs...)
		} else {
			sqlStr = fmt.Sprintf(
				"SELECT id, title, prompt, status, result_summary, updated_at FROM muse_tasks WHERE %s ORDER BY updated_at %s LIMIT %d",
				whereSQL, orderDir, limit,
			)
			args = whereArgs
		}
		res, err := store.ExecuteSQL(sqlStr, args)
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_LIST_TASKS_FAILED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"tasks": queryRowsToMaps(res)})
		return
	}

	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}

	var req struct {
		Action        string `json:"action"` // "save" | "status" | "reset" | "delete"
		ID            string `json:"id"`
		Title         string `json:"title"`
		Prompt        string `json:"prompt"`
		Status        string `json:"status"`
		ResultSummary string `json:"result_summary"`
	}
	if err := a.readJSONBody(r, &req); err != nil {
		WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}

	nowStr := time.Now().Format("2006-01-02 15:04:05")
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "status":
		st := strings.TrimSpace(req.Status)
		if st == "" {
			st = "pending"
		}
		args := append([]interface{}{st, req.ResultSummary, nowStr, strings.TrimSpace(req.ID)}, whereArgs...)
		_, _ = store.ExecuteSQL(
			"UPDATE muse_tasks SET status = ?, result_summary = ?, updated_at = ? WHERE id = ? AND "+whereSQL,
			args,
		)
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"updated": true})
		return

	case "reset":
		args := append([]interface{}{nowStr, strings.TrimSpace(req.ID)}, whereArgs...)
		_, _ = store.ExecuteSQL(
			"UPDATE muse_tasks SET status = 'pending', result_summary = '', updated_at = ? WHERE id = ? AND "+whereSQL,
			args,
		)
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"updated": true})
		return

	case "delete":
		args := append([]interface{}{strings.TrimSpace(req.ID)}, whereArgs...)
		_, _ = store.ExecuteSQL("DELETE FROM muse_tasks WHERE id = ? AND "+whereSQL, args)
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"deleted": true})
		return

	default: // "save"
		title := strings.TrimSpace(req.Title)
		if title == "" {
			WriteJSONError(w, http.StatusBadRequest, "Vui lòng nhập tên nhiệm vụ.")
			return
		}
		taskID, err := a.ensureScopedRecordID("muse_tasks", req.ID, "tsk_", ownerScope)
		if err != nil {
			WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := store.ExecuteSQL(
			`INSERT INTO muse_tasks(id, owner_id, title, prompt, status, updated_at)
			 VALUES(?, ?, ?, ?, 'pending', ?)
			 ON CONFLICT(id) DO UPDATE SET
			   title = excluded.title,
			   prompt = excluded.prompt,
			   updated_at = excluded.updated_at`,
			[]interface{}{taskID, ownerScope, title, strings.TrimSpace(req.Prompt), nowStr},
		); err != nil {
			WriteJSONError(w, http.StatusInternalServerError, "ERR_SAVE_TASK_FAILED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"saved": true,
			"id":    taskID,
		})
	}
}
