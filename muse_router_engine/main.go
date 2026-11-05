package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed all:web
var embeddedWebFS embed.FS

type ServerApp struct {
	pool       *MuseRouterPool
	jwtMgr     *RS256JWTManager
	cache      *TTLCache
	rustBridge *RustCoreBridge
}

func NewServerApp(dataDir string) (*ServerApp, error) {
	// [FIX #2] MUSE_VAULT_MASTER_KEY bat buoc — khong con hard-code fallback
	masterKey := os.Getenv("MUSE_VAULT_MASTER_KEY")
	if masterKey == "" {
		// Sinh key ngau nhien an toan cho session nay (khong luu disk)
		// Canh bao: moi lan khoi dong lai se doi key -> vault encrypt lai tu dong
		LogWarn("MUSE_VAULT_MASTER_KEY chua duoc dat — dang sinh key ngau nhien cho session nay. Dat bien nay de Vault du lieu on dinh giua cac lan khoi dong.")
		keyBytes := make([]byte, 32)
		if _, err := rand.Read(keyBytes); err != nil {
			return nil, fmt.Errorf("khong the sinh session master key: %w", err)
		}
		masterKey = hex.EncodeToString(keyBytes)
	}
	aiBridgeURL := os.Getenv("MUSE_AI_BRIDGE_URL")
	if aiBridgeURL == "" {
		aiBridgeURL = "http://127.0.0.1:18089"
	}
	// [FIX #2] aiBridgeToken — khong con hard-code; sinh ngau nhien neu thieu
	aiBridgeToken := os.Getenv("MUSE_INTERNAL_BRIDGE_TOKEN")
	if aiBridgeToken == "" {
		LogWarn("MUSE_INTERNAL_BRIDGE_TOKEN chua duoc dat — dang sinh token ngau nhien cho session nay.")
		tokenBytes := make([]byte, 24)
		if _, err := rand.Read(tokenBytes); err != nil {
			return nil, fmt.Errorf("khong the sinh bridge token: %w", err)
		}
		aiBridgeToken = hex.EncodeToString(tokenBytes)
	}

	cache := NewTTLCache()
	jwtMgr, err := NewRS256JWTManagerWithKeyFile(cache, filepath.Join(dataDir, "jwt_rs256.key.enc"), masterKey)
	if err != nil {
		return nil, err
	}

	rustBridge, err := NewRustCoreBridge(masterKey)
	if err != nil {
		return nil, err
	}

	pool, err := NewMuseRouterPool(dataDir, rustBridge, cache, aiBridgeURL, aiBridgeToken)
	if err != nil {
		return nil, err
	}
	jwtMgr.SyncWithSQLStore(pool.GetMySQLStore(), masterKey)

	app := &ServerApp{
		pool:       pool,
		jwtMgr:     jwtMgr,
		cache:      cache,
		rustBridge: rustBridge,
	}
	app.EnsureDefaultAdminAccount()

	return app, nil
}

func (a *ServerApp) readJSONBody(r *http.Request, dst interface{}) error {
	defer r.Body.Close()
	const maxPayload = 15 * 1024 * 1024
	limited := io.LimitReader(r.Body, maxPayload)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if _, err := ValidateAndSanitizeInput(string(raw), maxPayload); err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func (a *ServerApp) Routes() http.Handler {
	mux := http.NewServeMux()

	// 1. Health & Auth Session (cấp phát JWT RS256 bất đối xứng)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"status":  "ok",
			"service": "muse_router_engine",
			"jwt_alg": "RS256",
		})
	})

	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, r *http.Request) {
		actor := a.resolveActor(r)
		sub := nonEmptyStr(actor.UserID, "muse_router_operator")
		uname := nonEmptyStr(actor.Username, "admin")
		dname := nonEmptyStr(actor.DisplayName, "Quản trị viên")
		role := nonEmptyStr(actor.Role, "admin")
		token, err := a.jwtMgr.IssueUserToken(sub, uname, dname, role, 12*time.Hour)
		if err != nil {
			LogError("JWT RS256 issue error: %v", err)
			WriteJSONError(w, http.StatusInternalServerError, "ERR_TOKEN_ISSUE_FAILED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"token": token,
			"alg":   "RS256",
		})
	})

	// 1.5. Quản lý Tài khoản Người dùng & Workspace Cô lập theo từng tài khoản
	mux.HandleFunc("/api/users/register", a.handleUserRegister)
	mux.HandleFunc("/api/users/login", a.handleUserLogin)
	mux.HandleFunc("/api/users/admin-login", a.handleAdminUnlockLogin)
	mux.HandleFunc("/api/users/me", a.handleUserMe)
	mux.HandleFunc("/api/users/password", a.handleUserChangePassword)

	mux.HandleFunc("/api/admin/users", a.handleAdminUsersList)
	mux.HandleFunc("/api/admin/users/toggle", a.handleAdminUserToggle)
	mux.HandleFunc("/api/admin/users/reset-password", a.handleAdminUserResetPassword)
	mux.HandleFunc("/api/admin/users/delete", a.handleAdminUserDelete)

	mux.HandleFunc("/api/workspace/search", a.handleWorkspaceSearch)
	mux.HandleFunc("/api/workspace/docs", a.handleWorkspaceDocs)
	mux.HandleFunc("/api/workspace/memories", a.handleWorkspaceMemories)
	mux.HandleFunc("/api/workspace/tasks", a.handleWorkspaceTasks)

	// 2. Router Summary & Config
	mux.HandleFunc("/api/router/summary", func(w http.ResponseWriter, r *http.Request) {
		actor := a.resolveActor(r)
		if actor.Role == "admin" {
			WriteJSONResponse(w, http.StatusOK, a.pool.GetSummary())
			return
		}
		WriteJSONResponse(w, http.StatusOK, a.pool.GetPublicSummary())
	})

	mux.HandleFunc("/api/router/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			RoutingStrategy string `json:"routing_strategy"`
			AutoFailover    bool   `json:"auto_failover"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		a.pool.UpdateRoutingConfig(req.RoutingStrategy, req.AutoFailover)
		WriteJSONResponse(w, http.StatusOK, a.pool.GetSummary())
	})

	// 3. Quản lý Pool Tài Khoản Muse AI THẬT (Không dữ liệu giả)
	mux.HandleFunc("/api/accounts/add", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			Label           string `json:"label"`
			PlanTier        string `json:"plan_tier"`
			AuthType        string `json:"auth_type"`
			UpstreamURL     string `json:"upstream_url"`
			ProxyURL        string `json:"proxy_url"`
			Credential      string `json:"credential"`
			TokensRemaining int64  `json:"tokens_remaining"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		node, err := a.pool.AddAccountNode(
			r.Context(),
			req.Label,
			req.PlanTier,
			req.AuthType,
			req.UpstreamURL,
			req.Credential,
			req.TokensRemaining,
			req.ProxyURL,
		)
		if err != nil {
			LogError("AddAccountNode failed: %v", err)
			WriteJSONError(w, http.StatusBadRequest, "ERR_ADD_ACCOUNT_FAILED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, node)
	})

	// Nhận cookie muse.ai từ extension trình duyệt (bắt buộc mật khẩu admin)
	mux.HandleFunc("/api/accounts/ingest", a.handleIngestAccount)
	mux.HandleFunc("/api/accounts/extension.zip", a.handleDownloadExtensionZip)

	mux.HandleFunc("/api/accounts/batch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			Lines            []string `json:"lines"`
			PlanTier         string   `json:"plan_tier"`
			AuthType         string   `json:"auth_type"`
			UpstreamURL      string   `json:"upstream_url"`
			ProxyURL         string   `json:"proxy_url"`
			TokensPerAccount int64    `json:"tokens_per_account"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		added, verifiedReady, err := a.pool.BatchImportAccounts(
			r.Context(),
			req.Lines,
			req.PlanTier,
			req.AuthType,
			req.UpstreamURL,
			req.TokensPerAccount,
			req.ProxyURL,
		)
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_BATCH_IMPORT_FAILED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"added":          added,
			"verified_ready": verifiedReady,
			"summary":        a.pool.GetSummary(),
		})
	})

	// Tự động lấy Link Đăng Nhập OAuth chính thức của muse.ai (https://auth.meta.com/oidc?...)
	mux.HandleFunc("/api/accounts/oauth-link", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		res, err := a.pool.GenerateOAuthLoginLink(r.Context())
		if err != nil {
			LogWarn("GenerateOAuthLoginLink error: %v", err)
			WriteJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		WriteJSONResponse(w, http.StatusOK, res)
	})

	// Đăng nhập trực tiếp trên trình duyệt thật để tự động bắt Cookie hatch_sess
	mux.HandleFunc("/api/accounts/browser-login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			Label           string `json:"label"`
			TokensRemaining int64  `json:"tokens_remaining"`
			SessionID       string `json:"session_id"`
			ProxyURL        string `json:"proxy_url"`
		}
		_ = a.readJSONBody(r, &req)
		node, err := a.pool.InteractiveBrowserLogin(r.Context(), req.Label, req.TokensRemaining, req.SessionID, req.ProxyURL)
		if err != nil {
			LogWarn("InteractiveBrowserLogin did not complete: %v", err)
			WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSONResponse(w, http.StatusOK, node)
	})

	// Mở cửa sổ trình duyệt Muse.ai chính gốc đã nạp sẵn Cookie của Node
	mux.HandleFunc("/api/accounts/open-native", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			NodeID string `json:"node_id"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		res, err := a.pool.LaunchNativeAccountWindow(r.Context(), req.NodeID)
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSONResponse(w, http.StatusOK, res)
	})

	mux.HandleFunc("/api/accounts/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			NodeID          string `json:"node_id"`
			Status          string `json:"status"`
			TokensRemaining int64  `json:"tokens_remaining"`
			Label           string `json:"label"`
			ProxyURL        string `json:"proxy_url"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		if err := a.pool.UpdateNodeStatus(req.NodeID, req.Status, req.TokensRemaining, req.Label, req.ProxyURL); err != nil {
			WriteJSONError(w, http.StatusNotFound, "ERR_NODE_NOT_FOUND")
			return
		}
		WriteJSONResponse(w, http.StatusOK, a.pool.GetSummary())
	})

	mux.HandleFunc("/api/accounts/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			NodeID string `json:"node_id"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		if err := a.pool.DeleteNode(req.NodeID); err != nil {
			WriteJSONError(w, http.StatusNotFound, "ERR_NODE_NOT_FOUND")
			return
		}
		WriteJSONResponse(w, http.StatusOK, a.pool.GetSummary())
	})

	mux.HandleFunc("/api/accounts/wake", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			NodeID string `json:"node_id"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		if req.NodeID == "all" || req.NodeID == "" {
			probed, readyCnt := probeAllNodesConcurrently(r.Context(), a.pool)
			WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
				"probed":  probed,
				"ready":   readyCnt,
				"summary": a.pool.GetSummary(),
			})
			return
		}
		res, err := a.pool.WakeAndProbeNode(r.Context(), req.NodeID)
		if err != nil {
			WriteJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		WriteJSONResponse(w, http.StatusOK, res)
	})

	mux.HandleFunc("/api/accounts/wake-all", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		probed, _ := probeAllNodesConcurrently(r.Context(), a.pool)
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"probed":  probed,
			"summary": a.pool.GetSummary(),
		})
	})

	mux.HandleFunc("/api/accounts/remote-login/prewarm", a.handleRemoteLoginPrewarm)
	mux.HandleFunc("/api/accounts/remote-login/start", a.handleRemoteLoginStart)
	mux.HandleFunc("/api/accounts/remote-login/state", a.handleRemoteLoginState)
	mux.HandleFunc("/api/accounts/remote-login/input", a.handleRemoteLoginInput)
	mux.HandleFunc("/api/accounts/remote-login/cancel", a.handleRemoteLoginCancel)
	// 4. Quản lý Phiên Hội Thoại (Threads) & Muse AI Chat thật (cô lập theo từng tài khoản trong libSQL)
	mux.HandleFunc("/api/threads", func(w http.ResponseWriter, r *http.Request) {
		actor := a.resolveActor(r)
		ownerScope := actor.OwnerScope()
		if r.Method == http.MethodPost {
			var req struct {
				Title     string `json:"title"`
				Model     string `json:"model"`
				Workspace string `json:"workspace"`
			}
			if err := a.readJSONBody(r, &req); err != nil {
				WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
				return
			}
			created := a.pool.CreateNewThreadForOwner(ownerScope, req.Title, req.Model, req.Workspace)
			WriteJSONResponse(w, http.StatusOK, created)
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"threads": a.pool.ListThreadsForOwner(ownerScope),
		})
	})

	mux.HandleFunc("/api/threads/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			Title     string `json:"title"`
			Model     string `json:"model"`
			Workspace string `json:"workspace"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		actor := a.resolveActor(r)
		created := a.pool.CreateNewThreadForOwner(actor.OwnerScope(), req.Title, req.Model, req.Workspace)
		WriteJSONResponse(w, http.StatusOK, created)
	})

	mux.HandleFunc("/api/threads/rename", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			ThreadID string `json:"thread_id"`
			Title    string `json:"title"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		actor := a.resolveActor(r)
		if err := a.pool.RenameThreadForOwner(actor.OwnerScope(), req.ThreadID, req.Title); err != nil {
			WriteJSONError(w, http.StatusNotFound, "ERR_THREAD_NOT_FOUND")
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"renamed": true})
	})

	mux.HandleFunc("/api/threads/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			ThreadID string `json:"thread_id"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		actor := a.resolveActor(r)
		if err := a.pool.DeleteThreadForOwner(actor.OwnerScope(), req.ThreadID); err != nil {
			WriteJSONError(w, http.StatusNotFound, "ERR_THREAD_NOT_FOUND")
			return
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{"deleted": true})
	})

	// /api/threads/messages — phân trang tin nhắn (limit/offset)
	mux.HandleFunc("/api/threads/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		store := a.pool.GetMySQLStore()
		if store == nil {
			WriteJSONError(w, http.StatusServiceUnavailable, "ERR_DATABASE_NOT_INITIALIZED")
			return
		}
		threadID := r.URL.Query().Get("thread_id")
		limitStr := r.URL.Query().Get("limit")
		offsetStr := r.URL.Query().Get("offset")
		if threadID == "" {
			WriteJSONError(w, http.StatusBadRequest, "ERR_MISSING_THREAD_ID")
			return
		}
		actor := a.resolveActor(r)
		if !a.pool.HasThreadAccess(actor.OwnerScope(), threadID) {
			WriteJSONError(w, http.StatusForbidden, "ERR_FORBIDDEN_THREAD")
			return
		}
		limit := int64(50)
		offset := int64(0)
		if limitStr != "" {
			if v, e := strconv.ParseInt(limitStr, 10, 64); e == nil && v > 0 && v <= 200 {
				limit = v
			}
		}
		if offsetStr != "" {
			if v, e := strconv.ParseInt(offsetStr, 10, 64); e == nil && v >= 0 {
				offset = v
			}
		}
		qRes, err := store.ExecuteSQL(
			`SELECT id, seq_num, role, content, attachments_json, node_label, model, latency_ms, tokens, timestamp
			 FROM chat_messages WHERE thread_id=? ORDER BY seq_num ASC LIMIT ? OFFSET ?`,
			[]interface{}{threadID, limit, offset},
		)
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Đếm tổng tin nhắn của thread
		cRes, _ := store.ExecuteSQL(
			"SELECT COUNT(1) as total FROM chat_messages WHERE thread_id=?",
			[]interface{}{threadID},
		)
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"thread_id": threadID,
			"limit":     limit,
			"offset":    offset,
			"messages":  qRes,
			"total":     cRes,
		})
	})

	mux.HandleFunc("/api/chat/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			ThreadID        string           `json:"thread_id"`
			PreferredNodeID string           `json:"preferred_node_id"`
			Model           string           `json:"model"`
			Workspace       string           `json:"workspace"`
			Prompt          string           `json:"prompt"`
			Attachments     []ChatAttachment `json:"attachments"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		actor := a.resolveActor(r)
		preferredNode := req.PreferredNodeID
		if actor.Role != "admin" {
			preferredNode = "auto"
		}
		idemKey := r.Header.Get("X-Idempotency-Key")
		res, err := a.pool.RouteAndExecuteChatForOwner(
			r.Context(),
			actor.OwnerScope(),
			req.ThreadID,
			preferredNode,
			req.Model,
			req.Workspace,
			req.Prompt,
			req.Attachments,
			idemKey,
		)
		if err != nil {
			LogError("RouteAndExecuteChat error: %v", err)
			WriteJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		if actor.Role == "admin" {
			res["summary"] = a.pool.GetSummary()
		} else {
			res["summary"] = a.pool.GetPublicSummary()
		}
		WriteJSONResponse(w, http.StatusOK, res)
	})

	mux.HandleFunc("/api/chat/relay-complete", a.handleChatRelayComplete)
	mux.HandleFunc("/api/chat/live-status", func(w http.ResponseWriter, r *http.Request) {
		actor := a.resolveActor(r)
		WriteJSONResponse(w, http.StatusOK, a.pool.GetLiveActivityStatusForOwner(r.Context(), actor.OwnerScope()))
	})

	// 4.5. Cơ sở dữ liệu chuẩn SQL40 trực tiếp cho hệ thống Web (Status / SQL Query / Config / Pipeline)
	handleSQLStatus := func(w http.ResponseWriter, r *http.Request) {
		store := a.pool.GetMySQLStore()
		if store == nil {
			WriteJSONError(w, http.StatusServiceUnavailable, "ERR_SQL_NOT_INITIALIZED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, store.GetStatus())
	}
	mux.HandleFunc("/api/sql/status", handleSQLStatus)
	mux.HandleFunc("/api/mysql/status", handleSQLStatus)
	mux.HandleFunc("/api/libsql/status", handleSQLStatus)

	handleSQLQuery := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			SQL  string        `json:"sql"`
			Args []interface{} `json:"args"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		store := a.pool.GetMySQLStore()
		if store == nil {
			WriteJSONError(w, http.StatusServiceUnavailable, "ERR_SQL_NOT_INITIALIZED")
			return
		}
		qRes, err := store.ExecuteSQL(req.SQL, req.Args)
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSONResponse(w, http.StatusOK, qRes)
	}
	mux.HandleFunc("/api/sql/query", handleSQLQuery)
	mux.HandleFunc("/api/mysql/query", handleSQLQuery)
	mux.HandleFunc("/api/libsql/query", handleSQLQuery)

	handleSQLConfig := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			DatabaseURL string `json:"database_url"`
			AuthToken   string `json:"auth_token"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		store := a.pool.GetMySQLStore()
		if store == nil {
			WriteJSONError(w, http.StatusServiceUnavailable, "ERR_SQL_NOT_INITIALIZED")
			return
		}
		if err := store.Reconfigure(req.DatabaseURL, req.AuthToken); err != nil {
			WriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = a.pool.loadVault()
		_ = a.pool.loadThreads()
		WriteJSONResponse(w, http.StatusOK, store.GetStatus())
	}
	mux.HandleFunc("/api/sql/config", handleSQLConfig)
	mux.HandleFunc("/api/mysql/config", handleSQLConfig)
	mux.HandleFunc("/api/libsql/config", handleSQLConfig)

	mux.HandleFunc("/api/admin/migrate-to-mysql", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		store := a.pool.GetMySQLStore()
		if store == nil || store.db == nil {
			WriteJSONError(w, http.StatusServiceUnavailable, "ERR_DATABASE_NOT_INITIALIZED")
			return
		}
		dataDir := a.pool.vaultPath
		if dataDir != "" {
			dataDir = filepath.Dir(dataDir)
		}
		store.MigrateLegacyJSONIfNeeded(
			filepath.Join(dataDir, "accounts_vault.json"),
			filepath.Join(dataDir, "sessions_store.json"),
			filepath.Join(dataDir, "threads.json"),
		)
		_ = a.pool.loadVault()
		_ = a.pool.loadThreads()
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"status":    "ok",
			"message":   "Đã di chuyển và đồng bộ toàn bộ dữ liệu vào MySQL / TiDB Cloud thành công",
			"db_status": store.GetStatus(),
		})
	})

	handleHranaPipeline := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req HranaPipelineRequest
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_HRANA_PIPELINE")
			return
		}
		store := a.pool.GetMySQLStore()
		if store == nil {
			WriteJSONError(w, http.StatusServiceUnavailable, "ERR_DATABASE_NOT_INITIALIZED")
			return
		}
		WriteJSONResponse(w, http.StatusOK, store.ExecuteHranaPipeline(req))
	}
	mux.HandleFunc("/v2/pipeline", handleHranaPipeline)
	mux.HandleFunc("/api/mysql/pipeline", handleHranaPipeline)
	mux.HandleFunc("/api/libsql/pipeline", handleHranaPipeline)

	// 4.6. Tasks API — chạy batch task độc lập qua Token Pool
	mux.HandleFunc("/api/tasks/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			TaskID string `json:"task_id"`
			Prompt string `json:"prompt"`
			Title  string `json:"title"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		if strings.TrimSpace(req.Prompt) == "" {
			WriteJSONError(w, http.StatusBadRequest, "ERR_EMPTY_PROMPT")
			return
		}
		store := a.pool.GetMySQLStore()
		if store == nil {
			WriteJSONError(w, http.StatusServiceUnavailable, "ERR_DATABASE_NOT_INITIALIZED")
			return
		}
		actor := a.resolveActor(r)
		ownerScope := actor.OwnerScope()
		whereSQL, whereArgs := ownerWhereClause("owner_id", ownerScope)

		// Cập nhật trạng thái running (chỉ cho phép trên task thuộc chủ sở hữu hiện tại)
		taskID := strings.TrimSpace(req.TaskID)
		if taskID != "" {
			args := append([]interface{}{taskID}, whereArgs...)
			_, _ = store.ExecuteSQL(
				"UPDATE muse_tasks SET status='running', updated_at=datetime('now') WHERE id=? AND "+whereSQL,
				args,
			)
		}
		// Thực thi chat thật qua Token Pool
		idemKey := "task:" + taskID
		res, err := a.pool.RouteAndExecuteChatForOwner(
			r.Context(),
			ownerScope,
			"", // thread_id rỗng = tạo thread tạm
			"auto",
			"",
			"task",
			req.Prompt,
			nil,
			idemKey,
		)
		if err != nil {
			LogError("tasks/run error: %v", err)
			if taskID != "" {
				args := append([]interface{}{err.Error(), taskID}, whereArgs...)
				_, _ = store.ExecuteSQL(
					"UPDATE muse_tasks SET status='error', result_summary=?, updated_at=datetime('now') WHERE id=? AND "+whereSQL,
					args,
				)
			}
			WriteJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		var outSummary RouterSummary
		if actor.Role == "admin" {
			outSummary = a.pool.GetSummary()
		} else {
			outSummary = a.pool.GetPublicSummary()
		}
		if isRelay, _ := res["browser_relay"].(bool); isRelay {
			res["task_id"] = taskID
			res["summary"] = outSummary
			WriteJSONResponse(w, http.StatusOK, res)
			return
		}
		// Lấy nội dung reply
		replyText := ""
		if v, ok := res["reply"]; ok {
			replyText = fmt.Sprintf("%v", v)
		} else if v, ok := res["content"]; ok {
			replyText = fmt.Sprintf("%v", v)
		}
		// Rút gọn result_summary tối đa 1000 ký tự
		summary := replyText
		if len(summary) > 1000 {
			summary = summary[:997] + "..."
		}
		if taskID != "" {
			args := append([]interface{}{summary, taskID}, whereArgs...)
			_, _ = store.ExecuteSQL(
				"UPDATE muse_tasks SET status='done', result_summary=?, updated_at=datetime('now') WHERE id=? AND "+whereSQL,
				args,
			)
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"task_id": taskID,
			"status":  "done",
			"reply":   replyText,
			"summary": outSummary,
		})
	})

	// /api/tasks/batch — chạy nhiều tasks song song bằng goroutines (tối đa 10 concurrent)
	mux.HandleFunc("/api/tasks/batch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		var req struct {
			Tasks []struct {
				TaskID string `json:"task_id"`
				Prompt string `json:"prompt"`
				Title  string `json:"title"`
			} `json:"tasks"`
		}
		if err := a.readJSONBody(r, &req); err != nil || len(req.Tasks) == 0 {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}
		if len(req.Tasks) > 10 {
			WriteJSONError(w, http.StatusBadRequest, "ERR_TOO_MANY_TASKS_MAX_10")
			return
		}
		store := a.pool.GetMySQLStore()
		if store == nil {
			WriteJSONError(w, http.StatusServiceUnavailable, "ERR_DATABASE_NOT_INITIALIZED")
			return
		}

		actor := a.resolveActor(r)
		ownerScope := actor.OwnerScope()
		whereSQL, whereArgs := ownerWhereClause("owner_id", ownerScope)

		type batchResult struct {
			TaskID string `json:"task_id"`
			Status string `json:"status"`
			Reply  string `json:"reply"`
			ErrMsg string `json:"error,omitempty"`
		}

		results := make([]batchResult, len(req.Tasks))
		sem := make(chan struct{}, 10) // giới hạn 10 goroutines concurrent

		var wg sync.WaitGroup
		for i, task := range req.Tasks {
			if strings.TrimSpace(task.Prompt) == "" {
				results[i] = batchResult{TaskID: task.TaskID, Status: "error", ErrMsg: "ERR_EMPTY_PROMPT"}
				continue
			}
			wg.Add(1)
			go func(idx int, t struct {
				TaskID string `json:"task_id"`
				Prompt string `json:"prompt"`
				Title  string `json:"title"`
			}) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				taskID := strings.TrimSpace(t.TaskID)
				if taskID != "" {
					args := append([]interface{}{taskID}, whereArgs...)
					_, _ = store.ExecuteSQL(
						"UPDATE muse_tasks SET status='running', updated_at=datetime('now') WHERE id=? AND "+whereSQL,
						args,
					)
				}
				res, err := a.pool.RouteAndExecuteChatForOwner(
					r.Context(), ownerScope, "", "auto", "", "task",
					t.Prompt, nil, "batch:"+taskID,
				)
				if err != nil {
					LogError("tasks/batch[%d] error: %v", idx, err)
					if taskID != "" {
						args := append([]interface{}{err.Error(), taskID}, whereArgs...)
						_, _ = store.ExecuteSQL(
							"UPDATE muse_tasks SET status='error', result_summary=?, updated_at=datetime('now') WHERE id=? AND "+whereSQL,
							args,
						)
					}
					results[idx] = batchResult{TaskID: taskID, Status: "error", ErrMsg: err.Error()}
					return
				}
				if a.pool.CancelIfBrowserRelay(res) {
					msg := "Cần chạy qua trình duyệt có Tiện ích Chrome Mart AI v1.1"
					if taskID != "" {
						args := append([]interface{}{msg, taskID}, whereArgs...)
						_, _ = store.ExecuteSQL("UPDATE muse_tasks SET status='error', result_summary=?, updated_at=datetime('now') WHERE id=? AND "+whereSQL, args)
					}
					results[idx] = batchResult{TaskID: taskID, Status: "error", ErrMsg: msg}
					return
				}
				replyText := ""
				if v, ok := res["reply"]; ok {
					replyText = fmt.Sprintf("%v", v)
				} else if v, ok := res["content"]; ok {
					replyText = fmt.Sprintf("%v", v)
				}
				summary := replyText
				if len(summary) > 1000 {
					summary = summary[:997] + "..."
				}
				if taskID != "" {
					args := append([]interface{}{summary, taskID}, whereArgs...)
					_, _ = store.ExecuteSQL(
						"UPDATE muse_tasks SET status='done', result_summary=?, updated_at=datetime('now') WHERE id=? AND "+whereSQL,
						args,
					)
				}
				results[idx] = batchResult{TaskID: taskID, Status: "done", Reply: replyText}
			}(i, task)
		}
		wg.Wait()
		var outSummary RouterSummary
		if actor.Role == "admin" {
			outSummary = a.pool.GetSummary()
		} else {
			outSummary = a.pool.GetPublicSummary()
		}
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"results": results,
			"total":   len(results),
			"summary": outSummary,
		})
	})

	// 5. Cổng OpenAI-Compatible Gateway (/v1/chat/completions)
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
			return
		}
		authHdr := r.Header.Get("Authorization")
		if strings.HasPrefix(authHdr, "Bearer ") {
			token := strings.TrimSpace(strings.TrimPrefix(authHdr, "Bearer "))
			if _, err := a.jwtMgr.VerifyToken(token); err != nil {
				WriteJSONError(w, http.StatusUnauthorized, "ERR_INVALID_TOKEN")
				return
			}
		}

		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := a.readJSONBody(r, &req); err != nil {
			WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
			return
		}

		userPrompt := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				userPrompt = req.Messages[i].Content
				break
			}
		}
		if userPrompt == "" {
			WriteJSONError(w, http.StatusBadRequest, "ERR_EMPTY_USER_PROMPT")
			return
		}

		actor := a.resolveActor(r)
		ownerScope := actor.OwnerScope()
		gwThreadID := "thread_api_gateway"
		if ownerScope != "" {
			gwThreadID = "thread_api_gateway_" + ownerScope
		}
		idemKey := r.Header.Get("X-Idempotency-Key")
		res, err := a.pool.RouteAndExecuteChatForOwner(
			r.Context(),
			ownerScope,
			gwThreadID,
			"auto",
			req.Model,
			"chat",
			userPrompt,
			nil,
			idemKey,
		)
		if err != nil {
			WriteJSONError(w, http.StatusBadGateway, err.Error())
			return
		}

		if a.pool.CancelIfBrowserRelay(res) {
			WriteJSONError(w, http.StatusServiceUnavailable, "Tài khoản Cookie muse.ai trên Cloud cần chạy qua trình duyệt có Tiện ích Chrome Mart AI (hoặc dùng API Key).")
			return
		}
		tokensUsed, _ := res["tokens_used"].(int64)
		WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
			"id":          fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
			"object":      "chat.completion",
			"created":     time.Now().Unix(),
			"model":       res["model"],
			"routed_node": res["routed_node"],
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"message": map[string]string{
						"role":    "assistant",
						"content": fmt.Sprintf("%v", res["reply"]),
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     0,
				"completion_tokens": tokensUsed,
				"total_tokens":      tokensUsed,
			},
		})
	})

	// 6. Phục vụ giao diện Web UI chính gốc (L1 RAM Pre-compressed Static Cache + chống cache trình duyệt cũ)
	webRoot, err := fs.Sub(embeddedWebFS, "web")
	if err != nil {
		LogError("Failed to mount embedded web directory: %v", err)
	}
	fileServer := http.FileServer(http.FS(webRoot))
	initPrecompressedWebAssets(webRoot)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if r.URL.Query().Get("v") != "" && (strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") || strings.HasSuffix(p, ".svg") || strings.HasSuffix(p, ".png") || strings.HasSuffix(p, ".ico")) {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		} else {
			w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
			w.Header().Set("Pragma", "no-cache")
		}
		targetAsset := strings.TrimPrefix(p, "/")
		if p == "/" || p == "/cai-dat" || p == "/admin" || p == "/library/artifacts" {
			targetAsset = "index.html"
		}
		if r.Method == http.MethodGet && servePrecompressedWebAsset(w, r, targetAsset) {
			return
		}
		if p == "/cai-dat" || p == "/admin" || p == "/library/artifacts" {
			r2 := new(http.Request)
			*r2 = *r
			u2 := *r.URL
			u2.Path = "/"
			r2.URL = &u2
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	return SecurityHeadersMiddleware(GzipMiddleware(a.AuthAndRoleMiddleware(mux)))
}

func probeAllNodesConcurrently(ctx context.Context, pool *MuseRouterPool) (int, int) {
	nodes := pool.GetSummary().Nodes
	if len(nodes) == 0 {
		return 0, 0
	}
	var probed int64
	var readyCnt int64
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, n := range nodes {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if res, err := pool.WakeAndProbeNode(ctx, nodeID); err == nil {
				atomic.AddInt64(&probed, 1)
				if st, _ := res["status"].(string); st == "ready" {
					atomic.AddInt64(&readyCnt, 1)
				}
			}
		}(n.ID)
	}
	wg.Wait()
	return int(probed), int(readyCnt)
}

var gzipWriterPool = sync.Pool{
	New: func() interface{} {
		gw, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return gw
	},
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
	passthrough bool
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	if code < 200 || code == http.StatusNoContent || code == http.StatusNotModified {
		g.passthrough = true
		g.ResponseWriter.WriteHeader(code)
		return
	}
	g.Header().Del("Content-Length")
	g.Header().Set("Content-Encoding", "gzip")
	g.Header().Add("Vary", "Accept-Encoding")
	gz := gzipWriterPool.Get().(*gzip.Writer)
	gz.Reset(g.ResponseWriter)
	g.gz = gz
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(b))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.passthrough || g.gz == nil {
		return g.ResponseWriter.Write(b)
	}
	return g.gz.Write(b)
}

func (g *gzipResponseWriter) Flush() {
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipResponseWriter) Close() {
	if g.gz != nil {
		_ = g.gz.Close()
		g.gz.Reset(io.Discard)
		gzipWriterPool.Put(g.gz)
		g.gz = nil
	}
}

type precompressedAsset struct {
	rawBytes    []byte
	gzipBytes   []byte
	contentType string
}

var (
	precompressedWebOnce   sync.Once
	precompressedWebAssets = make(map[string]precompressedAsset)
)

func initPrecompressedWebAssets(webRoot fs.FS) {
	if webRoot == nil {
		return
	}
	precompressedWebOnce.Do(func() {
		targets := map[string]string{
			"index.html":  "text/html; charset=utf-8",
			"app.js":      "application/javascript; charset=utf-8",
			"styles.css":  "text/css; charset=utf-8",
			"favicon.svg": "image/svg+xml",
		}
		for name, ctype := range targets {
			raw, err := fs.ReadFile(webRoot, name)
			if err != nil || len(raw) == 0 {
				continue
			}
			var buf bytes.Buffer
			gw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			_, _ = gw.Write(raw)
			_ = gw.Close()
			precompressedWebAssets[name] = precompressedAsset{
				rawBytes:    raw,
				gzipBytes:   buf.Bytes(),
				contentType: ctype,
			}
		}
	})
}

func servePrecompressedWebAsset(w http.ResponseWriter, r *http.Request, assetName string) bool {
	asset, ok := precompressedWebAssets[assetName]
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", asset.contentType)
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") && len(asset.gzipBytes) > 0 {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.Header().Set("Content-Length", strconv.Itoa(len(asset.gzipBytes)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(asset.gzipBytes)
		return true
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(asset.rawBytes)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(asset.rawBytes)
	return true
}

func shouldSkipGzip(path string) bool {
	switch path {
	case "/", "/index.html", "/cai-dat", "/admin", "/library/artifacts", "/app.js", "/styles.css", "/favicon.svg":
		return true
	}
	if path == "/api/accounts/extension.zip" ||
		path == "/api/accounts/remote-login/state" ||
		path == "/api/accounts/remote-login/input" {
		return true
	}
	lower := strings.ToLower(path)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".woff2", ".zip"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func GzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead ||
			r.Method == http.MethodOptions ||
			r.Header.Get("Upgrade") != "" ||
			!strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") ||
			shouldSkipGzip(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		r.Header.Del("Range")
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.Close()
		next.ServeHTTP(gw, r)
	})
}

func tryStartLocalPythonBridge(rootDir string) {
	if os.Getenv("RENDER") != "" {
		return
	}
	bridgeURL := os.Getenv("MUSE_AI_BRIDGE_URL")
	if bridgeURL == "" {
		bridgeURL = "http://127.0.0.1:18089"
	}
	client := &http.Client{Timeout: 800 * time.Millisecond}
	if resp, err := client.Get(bridgeURL + "/internal/live-status"); err == nil {
		resp.Body.Close()
		return
	}

	scriptPath := filepath.Join(rootDir, "muse_router_ai", "bridge_service.py")
	if _, err := os.Stat(scriptPath); err != nil {
		return
	}

	for _, pyBin := range []string{"python3", "python"} {
		if path, err := exec.LookPath(pyBin); err == nil {
			cmd := exec.Command(path, scriptPath)
			cmd.Dir = rootDir
			cmd.Env = os.Environ()
			if errStart := cmd.Start(); errStart == nil {
				LogInfo("Auto-started AI Bridge sidecar (%s %s)", pyBin, scriptPath)
				return
			}
		}
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = os.Getenv("MUSE_ENGINE_PORT")
	}
	if port == "" {
		port = "8088"
	}

	host := os.Getenv("MUSE_ENGINE_HOST")
	if host == "" {
		if os.Getenv("PORT") != "" || os.Getenv("RENDER") != "" {
			host = "0.0.0.0"
		} else {
			host = "127.0.0.1"
		}
	}

	baseDir, err := os.Getwd()
	if err != nil {
		baseDir = "."
	}
	rootDir := baseDir
	dataDir := filepath.Join(baseDir, "data")
	if filepath.Base(baseDir) == "muse_router_engine" {
		rootDir = filepath.Dir(baseDir)
		dataDir = filepath.Join(rootDir, "data")
	}

	tryStartLocalPythonBridge(rootDir)

	app, err := NewServerApp(dataDir)
	if err != nil {
		LogError("Failed to initialize MuseRouter Engine: %v", err)
		os.Exit(1)
	}
	defer app.pool.Shutdown()

	addr := host + ":" + port
	// [FIX #5] Canh bao ro rang khi MART_ADMIN_PASSWORD trong - bat ky moi truong nao
	if adminPassword() == "" {
		if os.Getenv("PORT") != "" || os.Getenv("RENDER") != "" {
			LogWarn("!!!NGUY HIEM!!! MART_ADMIN_PASSWORD chua duoc dat: web dang mo cong khai!")
		} else {
			LogWarn("MART_ADMIN_PASSWORD chua duoc dat: hien dang chay khong xac thuc. Dat bien nay de bao ve.")
		}
		// block cu khong ap dung nua
		LogWarn("MART_ADMIN_PASSWORD chưa được đặt: web đang mở công khai và API /api/accounts/ingest bị tắt")
	}
	LogInfo("MuseRouter Engine & Web Studio started at http://%s", addr)

	srv := &http.Server{
		Addr:              addr,
		Handler:           app.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       210 * time.Second, // [FIX #4] tuong minh 210s cho chat stream
		WriteTimeout:      210 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20, // [FIX #4] gioi han header 1MB
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		LogError("Server exited with error: %v", err)
		os.Exit(1)
	}
}
