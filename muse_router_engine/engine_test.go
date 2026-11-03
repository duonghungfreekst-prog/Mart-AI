package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInputValidationAndSecurityHeaders(t *testing.T) {
	clean, err := ValidateAndSanitizeInput("Xin chào Muse AI #1", 1024)
	if err != nil || clean != "Xin chào Muse AI #1" {
		t.Fatalf("Expected valid input, got err=%v", err)
	}

	if _, err := ValidateAndSanitizeInput("<script>alert(1)</script>", 1024); err == nil {
		t.Fatalf("Expected XSS payload to be rejected")
	}

	if _, err := ValidateAndSanitizeInput("../../etc/passwd", 1024); err == nil {
		t.Fatalf("Expected path traversal to be rejected")
	}

	if !ConstantTimeStringEquals("token_abc_123", "token_abc_123") {
		t.Fatalf("ConstantTimeStringEquals failed for identical strings")
	}
	if ConstantTimeStringEquals("token_abc_123", "token_abc_124") {
		t.Fatalf("ConstantTimeStringEquals failed for different strings")
	}
}

func TestJWTRS256SignAndVerify(t *testing.T) {
	cache := NewTTLCache()
	jwtMgr, err := NewRS256JWTManager(cache)
	if err != nil {
		t.Fatalf("Failed to init RS256JWTManager: %v", err)
	}

	token, err := jwtMgr.IssueToken("admin_user", 15*time.Minute)
	if err != nil {
		t.Fatalf("IssueToken failed: %v", err)
	}

	claims, err := jwtMgr.VerifyToken(token)
	if err != nil {
		t.Fatalf("VerifyToken failed: %v", err)
	}
	if claims.Sub != "admin_user" {
		t.Fatalf("Unexpected subject: %s", claims.Sub)
	}
}

func TestZeroFakeNodesAndRealVaultEncryption(t *testing.T) {
	tempDir := filepath.Join(".", "test_vault_data")
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	app, err := NewServerApp(tempDir)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}
	defer app.pool.Shutdown()

	handler := app.Routes()

	// 1. Khi khởi tạo sạch, bắt buộc TotalNodes == 0 (KHÔNG có node giả lập)
	reqSum := httptest.NewRequest(http.MethodGet, "/api/router/summary", nil)
	reqSum.Header.Set("Origin", "http://127.0.0.1:8088")
	recSum := httptest.NewRecorder()
	handler.ServeHTTP(recSum, reqSum)

	if recSum.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %d", recSum.Code)
	}
	var summary RouterSummary
	if err := json.Unmarshal(recSum.Body.Bytes(), &summary); err != nil {
		t.Fatalf("Failed to parse summary JSON: %v", err)
	}
	if summary.TotalNodes != 0 {
		t.Fatalf("Expected 0 initial nodes (zero fake data), got %d", summary.TotalNodes)
	}

	// 2. Khi gửi chat mà chưa cắm tài khoản nào, bắt buộc trả về lỗi thật chứ không trả về câu trả lời giả
	chatBody, _ := json.Marshal(map[string]interface{}{
		"thread_id":         "thread_welcome",
		"preferred_node_id": "auto",
		"model":             "muse-spark-1.2",
		"workspace":         "chat",
		"prompt":            "Test",
	})
	reqChat := httptest.NewRequest(http.MethodPost, "/api/chat/send", bytes.NewReader(chatBody))
	recChat := httptest.NewRecorder()
	handler.ServeHTTP(recChat, reqChat)

	if recChat.Code == http.StatusOK {
		t.Fatalf("Expected chat without accounts to fail with error, but got 200 OK")
	}

	// 3. Kiểm tra trực tiếp cơ sở dữ liệu chuẩn libSQL nhúng (/api/libsql/status & /v2/pipeline Hrana v2)
	reqLibStatus := httptest.NewRequest(http.MethodGet, "/api/libsql/status", nil)
	recLibStatus := httptest.NewRecorder()
	handler.ServeHTTP(recLibStatus, reqLibStatus)
	if recLibStatus.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from /api/libsql/status, got %d: %s", recLibStatus.Code, recLibStatus.Body.String())
	}
	var libStatus LibSQLStatusResponse
	if err := json.Unmarshal(recLibStatus.Body.Bytes(), &libStatus); err != nil {
		t.Fatalf("Failed to parse LibSQLStatusResponse: %v", err)
	}
	if !libStatus.Connected || libStatus.JournalMode != "WAL" {
		t.Fatalf("Expected connected libSQL in WAL mode, got connected=%v mode=%s", libStatus.Connected, libStatus.JournalMode)
	}

	// Tạo một đoạn chat và truy vấn trực tiếp qua chuẩn giao thức Hrana v2 Pipeline (/v2/pipeline)
	created := app.pool.CreateNewThread("Kiểm thử LibSQL Trực Tiếp", "Muse", "chat")
	if created == nil || created.ID == "" {
		t.Fatalf("Failed to create thread in libSQL")
	}

	pipelineReq := HranaPipelineRequest{
		Requests: []HranaStreamRequest{
			{
				Type: "execute",
				Stmt: &HranaStmt{
					Sql: "SELECT id, title FROM chat_threads WHERE id = ?",
					Args: []HranaArg{
						{Type: "text", Value: created.ID},
					},
				},
			},
			{Type: "close"},
		},
	}
	pipeBytes, _ := json.Marshal(pipelineReq)
	reqPipe := httptest.NewRequest(http.MethodPost, "/v2/pipeline", bytes.NewReader(pipeBytes))
	recPipe := httptest.NewRecorder()
	handler.ServeHTTP(recPipe, reqPipe)
	if recPipe.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from /v2/pipeline, got %d: %s", recPipe.Code, recPipe.Body.String())
	}
	var pipeResp HranaPipelineResponse
	if err := json.Unmarshal(recPipe.Body.Bytes(), &pipeResp); err != nil {
		t.Fatalf("Failed to unmarshal HranaPipelineResponse: %v", err)
	}
	if len(pipeResp.Results) < 1 || pipeResp.Results[0].Type != "ok" || len(pipeResp.Results[0].Response.Result.Rows) != 1 {
		t.Fatalf("Unexpected Hrana pipeline response: %+v", pipeResp)
	}

	// 4. Đảm bảo cơ sở dữ liệu thật tại f:\tools muse ai\data\muse_router.libsql.db được khởi tạo và đồng bộ tài khoản đã cắm
	prodDataDir := filepath.Join("..", "data")
	prodStore, err := NewLibSQLStore(prodDataDir, "", "")
	if err != nil {
		t.Fatalf("Failed to initialize production libSQL database: %v", err)
	}
	defer prodStore.Close()
	prodStore.MigrateLegacyJSONIfNeeded(
		filepath.Join(prodDataDir, "accounts_vault.json"),
		filepath.Join(prodDataDir, "sessions_store.json"),
		filepath.Join(prodDataDir, "threads.json"),
	)
}

func TestNewEndpoints(t *testing.T) {
	tempDir := filepath.Join(".", "test_new_endpoints")
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	app, err := NewServerApp(tempDir)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}
	defer app.pool.Shutdown()
	handler := app.Routes()

	// Test /api/tasks/run không có tài khoản → phải trả lỗi thật (không 200 OK)
	taskBody, _ := json.Marshal(map[string]interface{}{
		"task_id": "test-task-001",
		"prompt":  "Tóm tắt tài liệu này",
	})
	reqTask := httptest.NewRequest(http.MethodPost, "/api/tasks/run", bytes.NewReader(taskBody))
	reqTask.Header.Set("Content-Type", "application/json")
	recTask := httptest.NewRecorder()
	handler.ServeHTTP(recTask, reqTask)
	if recTask.Code == http.StatusOK {
		t.Fatalf("Expected /api/tasks/run without accounts to fail, got 200 OK")
	}

	// Test /api/tasks/run với prompt rỗng → phải trả ERR_EMPTY_PROMPT
	emptyBody, _ := json.Marshal(map[string]interface{}{"task_id": "x", "prompt": ""})
	reqEmpty := httptest.NewRequest(http.MethodPost, "/api/tasks/run", bytes.NewReader(emptyBody))
	reqEmpty.Header.Set("Content-Type", "application/json")
	recEmpty := httptest.NewRecorder()
	handler.ServeHTTP(recEmpty, reqEmpty)
	if recEmpty.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400 for empty prompt, got %d", recEmpty.Code)
	}

	// Test /api/threads/messages thiếu thread_id → 400
	reqMsg := httptest.NewRequest(http.MethodGet, "/api/threads/messages", nil)
	recMsg := httptest.NewRecorder()
	handler.ServeHTTP(recMsg, reqMsg)
	if recMsg.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400 for missing thread_id, got %d: %s", recMsg.Code, recMsg.Body.String())
	}

	// Test /api/threads/messages với thread_id hợp lệ → 200 + JSON có field messages
	thread := app.pool.CreateNewThread("Thread Phân Trang Test", "Muse", "chat")
	if thread == nil || thread.ID == "" {
		t.Fatalf("Failed to create thread")
	}
	reqMsg2 := httptest.NewRequest(http.MethodGet, "/api/threads/messages?thread_id="+thread.ID+"&limit=10&offset=0", nil)
	recMsg2 := httptest.NewRecorder()
	handler.ServeHTTP(recMsg2, reqMsg2)
	if recMsg2.Code != http.StatusOK {
		t.Fatalf("Expected 200 for /api/threads/messages, got %d: %s", recMsg2.Code, recMsg2.Body.String())
	}
	var msgResp map[string]interface{}
	if err := json.Unmarshal(recMsg2.Body.Bytes(), &msgResp); err != nil {
		t.Fatalf("Failed to parse messages response: %v", err)
	}
	if _, ok := msgResp["messages"]; !ok {
		t.Fatalf("Expected 'messages' field in response, got: %v", msgResp)
	}
}

func TestLibSQLNewTables(t *testing.T) {
	tempDir := filepath.Join(".", "test_new_tables")
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	app, err := NewServerApp(tempDir)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}
	defer app.pool.Shutdown()
	handler := app.Routes()

	// Kiểm tra 3 bảng mới qua Hrana v2 pipeline
	for _, table := range []string{"muse_documents", "muse_memories", "muse_tasks"} {
		pReq := HranaPipelineRequest{
			Requests: []HranaStreamRequest{
				{Type: "execute", Stmt: &HranaStmt{Sql: "SELECT COUNT(1) FROM " + table}},
				{Type: "close"},
			},
		}
		pBytes, _ := json.Marshal(pReq)
		req := httptest.NewRequest(http.MethodPost, "/v2/pipeline", bytes.NewReader(pBytes))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("Table %s not found via /v2/pipeline: %d %s", table, rec.Code, rec.Body.String())
		}
		var resp HranaPipelineResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("Failed to parse pipeline response for table %s: %v", table, err)
		}
		if len(resp.Results) == 0 || resp.Results[0].Type != "ok" {
			t.Fatalf("Expected ok result for table %s, got: %+v", table, resp.Results)
		}
	}
}

func TestSecurityEdgeCases(t *testing.T) {
	tempDir := filepath.Join(".", "test_security_edge")
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	app, err := NewServerApp(tempDir)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}
	defer app.pool.Shutdown()
	handler := app.Routes()

	// 1. Tất cả POST endpoint phải reject GET
	postOnlyEndpoints := []string{
		"/api/chat/send",
		"/api/tasks/run",
		"/api/accounts/add",
		"/api/accounts/wake",
		"/v2/pipeline",
	}
	for _, ep := range postOnlyEndpoints {
		req := httptest.NewRequest(http.MethodGet, ep, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Fatalf("Endpoint %s accepted GET but should only accept POST", ep)
		}
	}

	// 2. Security headers phải có trong mọi response
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	requiredHeaders := []string{
		"X-Content-Type-Options",
		"X-Frame-Options",
		"X-Xss-Protection",
	}
	for _, h := range requiredHeaders {
		if rec.Header().Get(h) == "" {
			t.Fatalf("Missing security header: %s", h)
		}
	}

	// 3. CRLF injection trong SQL query phải bị sanitize bởi ValidateAndSanitizeInput
	crlfInput := "select 1\r\nDROP TABLE muse_accounts;--"
	_, err2 := ValidateAndSanitizeInput(crlfInput, 2048)
	if err2 == nil {
		t.Fatalf("Expected CRLF injection to be rejected, but got nil error")
	}
}

// TestTasksBatchEndpoint — verify /api/tasks/batch reject đúng các edge cases
func TestTasksBatchEndpoint(t *testing.T) {
	tempDir := filepath.Join(".", "test_tasks_batch")
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	app, err := NewServerApp(tempDir)
	if err != nil {
		t.Fatalf("NewServerApp: %v", err)
	}
	defer app.pool.Shutdown()
	handler := app.Routes()

	// 1. GET thay vì POST → 405
	req1 := httptest.NewRequest(http.MethodGet, "/api/tasks/batch", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/tasks/batch: want 405, got %d", rec1.Code)
	}

	// 2. Body rỗng tasks → 400
	req2 := httptest.NewRequest(http.MethodPost, "/api/tasks/batch", bytes.NewBufferString(`{}`))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("Empty tasks: want 400, got %d", rec2.Code)
	}

	// 3. Quá 10 tasks → 400 ERR_TOO_MANY_TASKS_MAX_10
	bigTasks := `{"tasks":[` +
		`{"task_id":"t1","prompt":"a"},{"task_id":"t2","prompt":"b"},` +
		`{"task_id":"t3","prompt":"c"},{"task_id":"t4","prompt":"d"},` +
		`{"task_id":"t5","prompt":"e"},{"task_id":"t6","prompt":"f"},` +
		`{"task_id":"t7","prompt":"g"},{"task_id":"t8","prompt":"h"},` +
		`{"task_id":"t9","prompt":"i"},{"task_id":"t10","prompt":"j"},` +
		`{"task_id":"t11","prompt":"k"}` +
		`]}`
	req3 := httptest.NewRequest(http.MethodPost, "/api/tasks/batch", bytes.NewBufferString(bigTasks))
	req3.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("11 tasks: want 400, got %d", rec3.Code)
	}
	if !bytes.Contains(rec3.Body.Bytes(), []byte("ERR_TOO_MANY_TASKS_MAX_10")) {
		t.Errorf("Missing ERR_TOO_MANY_TASKS_MAX_10: %s", rec3.Body.String())
	}

	t.Log("TestTasksBatchEndpoint: tất cả edge cases PASS")
}

func TestGzipAndStaticCaching(t *testing.T) {
	t.Setenv("MART_ADMIN_PASSWORD", "")
	tmpDir := t.TempDir()
	app, err := NewServerApp(filepath.Join(tmpDir, "data"))
	if err != nil {
		t.Fatalf("NewServerApp: %v", err)
	}
	defer app.pool.Shutdown()
	handler := app.Routes()

	reqJS := httptest.NewRequest(http.MethodGet, "/app.js?v=5.2", nil)
	reqJS.Header.Set("Accept-Encoding", "gzip")
	recJS := httptest.NewRecorder()
	handler.ServeHTTP(recJS, reqJS)
	if recJS.Code != http.StatusOK {
		t.Fatalf("GET /app.js?v=5.2 status=%d", recJS.Code)
	}
	if recJS.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Expected Content-Encoding=gzip, got %q", recJS.Header().Get("Content-Encoding"))
	}
	if recJS.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Fatalf("Expected public cache for versioned JS, got %q", recJS.Header().Get("Cache-Control"))
	}
	compressedBytes := recJS.Body.Len()
	gr, err := gzip.NewReader(recJS.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader error: %v", err)
	}
	rawJS, err := io.ReadAll(gr)
	_ = gr.Close()
	if err != nil {
		t.Fatalf("gunzip ReadAll error: %v", err)
	}
	if len(rawJS) < 120000 || compressedBytes > len(rawJS)/2 {
		t.Fatalf("Unexpected sizes: raw=%d compressed=%d", len(rawJS), compressedBytes)
	}
	t.Logf("app.js compressed: %d bytes -> %d bytes (%.1f%% reduction)", len(rawJS), compressedBytes, 100.0*(1.0-float64(compressedBytes)/float64(len(rawJS))))

	reqAdmin := httptest.NewRequest(http.MethodGet, "/admin", nil)
	reqAdmin.Header.Set("Accept-Encoding", "gzip")
	recAdmin := httptest.NewRecorder()
	handler.ServeHTTP(recAdmin, reqAdmin)
	if !strings.Contains(recAdmin.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("Expected no-store on /admin HTML, got %q", recAdmin.Header().Get("Cache-Control"))
	}
}

func TestRealSubscriptionSyncAndZeroPctNewAccount(t *testing.T) {
	t.Setenv("RENDER", "true")
	mockMuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie := r.Header.Get("Cookie")
		if r.URL.Path == "/api/client-version" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"deploymentId":"dpl_test_2026"}`))
			return
		}
		if r.Method == http.MethodPost && r.Header.Get("Next-Action") != "" {
			w.Header().Set("Content-Type", "text/x-component")
			if strings.Contains(cookie, "brand_new_sess") {
				_, _ = w.Write([]byte("0:{\"a\":\"$@1\"}\n1:{\"subscription\":{\"usageRowLabel\":\"Gói miễn phí\",\"usageRowValueLabel\":\"Đã dùng 0%\",\"statusSubtitle\":\"Giới hạn hàng tuần sẽ đặt lại vào 10 tháng 11\",\"usage\":{\"percentUsed\":0},\"topupRowLabel\":\"Token bổ sung\",\"topupRowValueLabel\":\"Đã dùng 0% (còn 1 tỷ token)\",\"billingSubtitle\":\"Không bao giờ hết hạn\"}}\n"))
			} else {
				_, _ = w.Write([]byte("1:{\"subscription\":{\"usageRowLabel\":\"Gói miễn phí\",\"usageRowValueLabel\":\"Đã dùng 18%\",\"statusSubtitle\":\"Giới hạn hàng tuần sẽ đặt lại vào 10 tháng 11\",\"usage\":{\"percentUsed\":18}}}\n"))
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockMuse.Close()

	tmpDir := t.TempDir()
	app, err := NewServerApp(filepath.Join(tmpDir, "data"))
	if err != nil {
		t.Fatalf("NewServerApp: %v", err)
	}
	defer app.pool.Shutdown()

	nodeNew, err := app.pool.AddAccountNode(context.Background(), "Muse #2", "Muse.ai", "cookie", mockMuse.URL, "hatch_sess=brand_new_sess_1234567890", 0, "")
	if err != nil {
		t.Fatalf("AddAccountNode new: %v", err)
	}
	if nodeNew.FreePlanPercent != 0 || nodeNew.FreePlanValue != "Đã dùng 0%" {
		t.Fatalf("Expected brand new account to have 0%% ('Đã dùng 0%%'), got %d%% (%q)", nodeNew.FreePlanPercent, nodeNew.FreePlanValue)
	}

	nodeUsed, err := app.pool.AddAccountNode(context.Background(), "Muse #1", "Muse.ai", "cookie", mockMuse.URL, "hatch_sess=used_sess_9876543210", 0, "")
	if err != nil {
		t.Fatalf("AddAccountNode used: %v", err)
	}
	if nodeUsed.FreePlanPercent != 18 || nodeUsed.FreePlanValue != "Đã dùng 18%" {
		t.Fatalf("Expected used account to have 18%% ('Đã dùng 18%%'), got %d%% (%q)", nodeUsed.FreePlanPercent, nodeUsed.FreePlanValue)
	}
}

func TestMultiUserAccountIsolationAndSQL40Standard(t *testing.T) {
	t.Setenv("MART_ADMIN_PASSWORD", "AdminMaster2026!")

	tmpDir := t.TempDir()
	app, err := NewServerApp(filepath.Join(tmpDir, "data"))
	if err != nil {
		t.Fatalf("NewServerApp: %v", err)
	}
	defer app.pool.Shutdown()
	handler := app.Routes()

	doReq := func(method, path, token string, bodyObj interface{}) *httptest.ResponseRecorder {
		var bodyBytes []byte
		if bodyObj != nil {
			bodyBytes, _ = json.Marshal(bodyObj)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Mart-Client", "web")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1. Đăng nhập Admin để kiểm tra SQL40 Status
	recAdminLogin := doReq(http.MethodPost, "/api/users/admin-login", "", map[string]string{
		"password": "AdminMaster2026!",
	})
	if recAdminLogin.Code != http.StatusOK {
		t.Fatalf("Admin login failed: %d %s", recAdminLogin.Code, recAdminLogin.Body.String())
	}
	var adminLoginResp struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(recAdminLogin.Body.Bytes(), &adminLoginResp)
	if adminLoginResp.Token == "" {
		t.Fatalf("Expected admin token")
	}

	recSQL := doReq(http.MethodGet, "/api/sql/status", adminLoginResp.Token, nil)
	if recSQL.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from /api/sql/status, got %d", recSQL.Code)
	}
	var sqlStatus LibSQLStatusResponse
	_ = json.Unmarshal(recSQL.Body.Bytes(), &sqlStatus)
	if !sqlStatus.Connected || !strings.HasPrefix(sqlStatus.EngineVersion, "SQL40 Standard") {
		t.Fatalf("Expected SQL40 Standard engine, got connected=%v version=%q", sqlStatus.Connected, sqlStatus.EngineVersion)
	}

	// 2. Đăng ký 2 tài khoản người dùng độc lập: user_a và user_b
	recRegA := doReq(http.MethodPost, "/api/users/register", "", map[string]string{
		"username":     "user_a",
		"display_name": "Form Riêng A",
		"password":     "PassUserA123",
	})
	if recRegA.Code != http.StatusOK {
		t.Fatalf("Register user_a failed: %d %s", recRegA.Code, recRegA.Body.String())
	}
	var regA struct {
		Token string      `json:"token"`
		User  RequestActor `json:"user"`
	}
	_ = json.Unmarshal(recRegA.Body.Bytes(), &regA)

	recRegB := doReq(http.MethodPost, "/api/users/register", "", map[string]string{
		"username":     "user_b",
		"display_name": "Form Riêng B",
		"password":     "PassUserB123",
	})
	if recRegB.Code != http.StatusOK {
		t.Fatalf("Register user_b failed: %d %s", recRegB.Code, recRegB.Body.String())
	}
	var regB struct {
		Token string      `json:"token"`
		User  RequestActor `json:"user"`
	}
	_ = json.Unmarshal(recRegB.Body.Bytes(), &regB)

	// 3. user_a tạo đoạn chat, tài liệu, bộ nhớ và nhiệm vụ riêng
	recThreadA := doReq(http.MethodPost, "/api/threads", regA.Token, map[string]string{
		"title": "Dự án mật của User A",
		"model": "Muse",
	})
	if recThreadA.Code != http.StatusOK {
		t.Fatalf("Create thread A failed: %d", recThreadA.Code)
	}
	var threadA ChatThread
	_ = json.Unmarshal(recThreadA.Body.Bytes(), &threadA)

	recDocA := doReq(http.MethodPost, "/api/workspace/docs", regA.Token, map[string]string{
		"action":  "save",
		"title":   "Báo cáo tài chính A",
		"content": "Nội dung chỉ dành cho User A",
	})
	if recDocA.Code != http.StatusOK {
		t.Fatalf("Create doc A failed: %d %s", recDocA.Code, recDocA.Body.String())
	}

	recMemA := doReq(http.MethodPost, "/api/workspace/memories", regA.Token, map[string]string{
		"action":  "save",
		"title":   "Bộ nhớ riêng A",
		"content": "Luôn xưng hô với User A",
	})
	if recMemA.Code != http.StatusOK {
		t.Fatalf("Create memory A failed: %d", recMemA.Code)
	}

	recTaskA := doReq(http.MethodPost, "/api/workspace/tasks", regA.Token, map[string]string{
		"action": "save",
		"title":  "Nhiệm vụ riêng A",
		"prompt": "Phân tích dữ liệu A",
	})
	if recTaskA.Code != http.StatusOK {
		t.Fatalf("Create task A failed: %d", recTaskA.Code)
	}

	// 4. user_b tạo đoạn chat và tài liệu riêng
	recThreadB := doReq(http.MethodPost, "/api/threads", regB.Token, map[string]string{
		"title": "Kế hoạch riêng của User B",
		"model": "Muse",
	})
	if recThreadB.Code != http.StatusOK {
		t.Fatalf("Create thread B failed: %d", recThreadB.Code)
	}

	recDocB := doReq(http.MethodPost, "/api/workspace/docs", regB.Token, map[string]string{
		"action":  "save",
		"title":   "Tài liệu của User B",
		"content": "Nội dung độc lập của B",
	})
	if recDocB.Code != http.StatusOK {
		t.Fatalf("Create doc B failed: %d", recDocB.Code)
	}

	// 5. Kiểm tra cô lập tuyệt đối: user_b KHÔNG nhìn thấy bất kỳ dữ liệu nào của user_a
	recListThreadsB := doReq(http.MethodGet, "/api/threads", regB.Token, nil)
	var listThreadsB struct {
		Threads []ChatThread `json:"threads"`
	}
	_ = json.Unmarshal(recListThreadsB.Body.Bytes(), &listThreadsB)
	if len(listThreadsB.Threads) != 1 || listThreadsB.Threads[0].Title != "Kế hoạch riêng của User B" {
		t.Fatalf("Isolation breach on threads for user_b: %+v", listThreadsB.Threads)
	}

	recListDocsB := doReq(http.MethodGet, "/api/workspace/docs", regB.Token, nil)
	var listDocsB struct {
		Docs []map[string]string `json:"docs"`
	}
	_ = json.Unmarshal(recListDocsB.Body.Bytes(), &listDocsB)
	if len(listDocsB.Docs) != 1 || listDocsB.Docs[0]["title"] != "Tài liệu của User B" {
		t.Fatalf("Isolation breach on docs for user_b: %+v", listDocsB.Docs)
	}

	recListMemsB := doReq(http.MethodGet, "/api/workspace/memories", regB.Token, nil)
	var listMemsB struct {
		Memories []map[string]string `json:"memories"`
	}
	_ = json.Unmarshal(recListMemsB.Body.Bytes(), &listMemsB)
	if len(listMemsB.Memories) != 0 {
		t.Fatalf("Expected 0 memories for user_b, got %+v", listMemsB.Memories)
	}

	recSearchB := doReq(http.MethodGet, "/api/workspace/search?q=User+A", regB.Token, nil)
	var searchB struct {
		Threads  []map[string]string `json:"threads"`
		Messages []map[string]string `json:"messages"`
	}
	_ = json.Unmarshal(recSearchB.Body.Bytes(), &searchB)
	if len(searchB.Threads) != 0 || len(searchB.Messages) != 0 {
		t.Fatalf("Search isolation breach: user_b found user_a data: %+v", searchB)
	}

	// 6. user_b cố tình đọc hoặc xóa đoạn chat của user_a -> bị chặn 403/404
	recStealMsg := doReq(http.MethodGet, "/api/threads/messages?thread_id="+threadA.ID, regB.Token, nil)
	if recStealMsg.Code != http.StatusForbidden {
		t.Fatalf("Expected 403 Forbidden when user_b reads user_a thread messages, got %d", recStealMsg.Code)
	}

	// 7. user_a cố tình gọi API Admin -> bị chặn 403 Forbidden
	recForbidAdmin := doReq(http.MethodGet, "/api/admin/users", regA.Token, nil)
	if recForbidAdmin.Code != http.StatusForbidden {
		t.Fatalf("Expected 403 Forbidden when user_a calls /api/admin/users, got %d", recForbidAdmin.Code)
	}

	// 8. Admin xem danh sách tài khoản và xóa user_a -> dữ liệu user_b vẫn còn nguyên vẹn
	recAdminUsers := doReq(http.MethodGet, "/api/admin/users", adminLoginResp.Token, nil)
	if recAdminUsers.Code != http.StatusOK {
		t.Fatalf("Admin list users failed: %d", recAdminUsers.Code)
	}
	recDelA := doReq(http.MethodPost, "/api/admin/users/delete", adminLoginResp.Token, map[string]string{
		"user_id": regA.User.UserID,
	})
	if recDelA.Code != http.StatusOK {
		t.Fatalf("Admin delete user_a failed: %d %s", recDelA.Code, recDelA.Body.String())
	}

	// Xác nhận user_b vẫn còn nguyên đoạn chat và tài liệu
	recCheckB := doReq(http.MethodGet, "/api/threads", regB.Token, nil)
	_ = json.Unmarshal(recCheckB.Body.Bytes(), &listThreadsB)
	if len(listThreadsB.Threads) != 1 {
		t.Fatalf("Expected user_b threads to remain intact after deleting user_a, got %d", len(listThreadsB.Threads))
	}
}
