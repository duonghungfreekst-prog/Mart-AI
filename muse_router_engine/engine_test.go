package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInputValidationAndSecurityHeaders(t *testing.T) {
	clean, err := ValidateAndSanitizeInput("Xin chÃƒÂ o Muse AI #1", 1024)
	if err != nil || clean != "Xin chÃƒÂ o Muse AI #1" {
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

const TestBootstrapPassword = "Admin@123456"

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

	// 1. Khi khÃ¡Â»Å¸i tÃ¡ÂºÂ¡o sÃ¡ÂºÂ¡ch, bÃ¡ÂºÂ¯t buÃ¡Â»â„¢c TotalNodes == 0 (KHÃƒâ€NG cÃƒÂ³ node giÃ¡ÂºÂ£ lÃ¡ÂºÂ­p)
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

	// 2. Khi gÃ¡Â»Â­i chat mÃƒÂ  chÃ†Â°a cÃ¡ÂºÂ¯m tÃƒÂ i khoÃ¡ÂºÂ£n nÃƒÂ o, bÃ¡ÂºÂ¯t buÃ¡Â»â„¢c trÃ¡ÂºÂ£ vÃ¡Â»Â lÃ¡Â»â€”i thÃ¡ÂºÂ­t chÃ¡Â»Â© khÃƒÂ´ng trÃ¡ÂºÂ£ vÃ¡Â»Â cÃƒÂ¢u trÃ¡ÂºÂ£ lÃ¡Â»Âi giÃ¡ÂºÂ£
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

	// 3. KiÃ¡Â»Æ’m tra trÃ¡Â»Â±c tiÃ¡ÂºÂ¿p cÃ†Â¡ sÃ¡Â»Å¸ dÃ¡Â»Â¯ liÃ¡Â»â€¡u chuÃ¡ÂºÂ©n MySQL (/api/mysql/status & /v2/pipeline Hrana v2)
	reqLibStatus := httptest.NewRequest(http.MethodGet, "/api/mysql/status", nil)
	recLibStatus := httptest.NewRecorder()
	handler.ServeHTTP(recLibStatus, reqLibStatus)
	if recLibStatus.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK from /api/mysql/status, got %d: %s", recLibStatus.Code, recLibStatus.Body.String())
	}
	var libStatus MySQLStatusResponse
	if err := json.Unmarshal(recLibStatus.Body.Bytes(), &libStatus); err != nil {
		t.Fatalf("Failed to parse MySQLStatusResponse: %v", err)
	}
	if !libStatus.Connected || (libStatus.JournalMode != "WAL" && libStatus.JournalMode != "InnoDB (ACID)") {
		t.Fatalf("Expected connected database, got connected=%v mode=%s", libStatus.Connected, libStatus.JournalMode)
	}

	// TÃ¡ÂºÂ¡o mÃ¡Â»â„¢t Ã„â€˜oÃ¡ÂºÂ¡n chat vÃƒÂ  truy vÃ¡ÂºÂ¥n trÃ¡Â»Â±c tiÃ¡ÂºÂ¿p qua chuÃ¡ÂºÂ©n giao thÃ¡Â»Â©c Hrana v2 Pipeline (/v2/pipeline)
	created := app.pool.CreateNewThread("KiÃ¡Â»Æ’m thÃ¡Â»Â­ MySQL TrÃ¡Â»Â±c TiÃ¡ÂºÂ¿p", "Muse", "chat")
	if created == nil || created.ID == "" {
		t.Fatalf("Failed to create thread in MySQL")
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

	// 4. Ã„ÂÃ¡ÂºÂ£m bÃ¡ÂºÂ£o cÃ†Â¡ sÃ¡Â»Å¸ dÃ¡Â»Â¯ liÃ¡Â»â€¡u thÃ¡ÂºÂ­t MySQL Ã„â€˜Ã†Â°Ã¡Â»Â£c khÃ¡Â»Å¸i tÃ¡ÂºÂ¡o vÃƒÂ  Ã„â€˜Ã¡Â»â€œng bÃ¡Â»â„¢ tÃƒÂ i khoÃ¡ÂºÂ£n Ã„â€˜ÃƒÂ£ cÃ¡ÂºÂ¯m
	prodDataDir := filepath.Join("..", "data")
	prodStore, err := NewMySQLStore(prodDataDir, "", "")
	if err != nil {
		t.Fatalf("Failed to initialize production MySQL database: %v", err)
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

	// Test /api/tasks/run khÃƒÂ´ng cÃƒÂ³ tÃƒÂ i khoÃ¡ÂºÂ£n Ã¢â€ â€™ phÃ¡ÂºÂ£i trÃ¡ÂºÂ£ lÃ¡Â»â€”i thÃ¡ÂºÂ­t (khÃƒÂ´ng 200 OK)
	taskBody, _ := json.Marshal(map[string]interface{}{
		"task_id": "test-task-001",
		"prompt":  "TÃƒÂ³m tÃ¡ÂºÂ¯t tÃƒÂ i liÃ¡Â»â€¡u nÃƒÂ y",
	})
	reqTask := httptest.NewRequest(http.MethodPost, "/api/tasks/run", bytes.NewReader(taskBody))
	reqTask.Header.Set("Content-Type", "application/json")
	recTask := httptest.NewRecorder()
	handler.ServeHTTP(recTask, reqTask)
	if recTask.Code == http.StatusOK {
		t.Fatalf("Expected /api/tasks/run without accounts to fail, got 200 OK")
	}

	// Test /api/tasks/run vÃ¡Â»â€ºi prompt rÃ¡Â»â€”ng Ã¢â€ â€™ phÃ¡ÂºÂ£i trÃ¡ÂºÂ£ ERR_EMPTY_PROMPT
	emptyBody, _ := json.Marshal(map[string]interface{}{"task_id": "x", "prompt": ""})
	reqEmpty := httptest.NewRequest(http.MethodPost, "/api/tasks/run", bytes.NewReader(emptyBody))
	reqEmpty.Header.Set("Content-Type", "application/json")
	recEmpty := httptest.NewRecorder()
	handler.ServeHTTP(recEmpty, reqEmpty)
	if recEmpty.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400 for empty prompt, got %d", recEmpty.Code)
	}

	// Test /api/threads/messages thiÃ¡ÂºÂ¿u thread_id Ã¢â€ â€™ 400
	reqMsg := httptest.NewRequest(http.MethodGet, "/api/threads/messages", nil)
	recMsg := httptest.NewRecorder()
	handler.ServeHTTP(recMsg, reqMsg)
	if recMsg.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400 for missing thread_id, got %d: %s", recMsg.Code, recMsg.Body.String())
	}

	// Test /api/threads/messages vÃ¡Â»â€ºi thread_id hÃ¡Â»Â£p lÃ¡Â»â€¡ Ã¢â€ â€™ 200 + JSON cÃƒÂ³ field messages
	thread := app.pool.CreateNewThread("Thread PhÃƒÂ¢n Trang Test", "Muse", "chat")
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

func TestMySQLNewTables(t *testing.T) {
	tempDir := filepath.Join(".", "test_new_tables")
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	app, err := NewServerApp(tempDir)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}
	defer app.pool.Shutdown()
	handler := app.Routes()

	// KiÃ¡Â»Æ’m tra 3 bÃ¡ÂºÂ£ng mÃ¡Â»â€ºi qua Hrana v2 pipeline
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

	// 1. TÃ¡ÂºÂ¥t cÃ¡ÂºÂ£ POST endpoint phÃ¡ÂºÂ£i reject GET
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

	// 2. Security headers phÃ¡ÂºÂ£i cÃƒÂ³ trong mÃ¡Â»Âi response
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

	// 3. CRLF injection trong SQL query phÃ¡ÂºÂ£i bÃ¡Â»â€¹ sanitize bÃ¡Â»Å¸i ValidateAndSanitizeInput
	crlfInput := "select 1\r\nDROP TABLE muse_accounts;--"
	_, err2 := ValidateAndSanitizeInput(crlfInput, 2048)
	if err2 == nil {
		t.Fatalf("Expected CRLF injection to be rejected, but got nil error")
	}
}

// TestTasksBatchEndpoint Ã¢â‚¬â€ verify /api/tasks/batch reject Ã„â€˜ÃƒÂºng cÃƒÂ¡c edge cases
func TestTasksBatchEndpoint(t *testing.T) {
	tempDir := filepath.Join(".", "test_tasks_batch")
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	app, err := NewServerApp(tempDir)
	if err != nil {
		t.Fatalf("NewServerApp: %v", err)
	}
	defer app.pool.Shutdown()
	handler := app.Routes()

	// 1. GET thay vÃƒÂ¬ POST Ã¢â€ â€™ 405
	req1 := httptest.NewRequest(http.MethodGet, "/api/tasks/batch", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/tasks/batch: want 405, got %d", rec1.Code)
	}

	// 2. Body rÃ¡Â»â€”ng tasks Ã¢â€ â€™ 400
	req2 := httptest.NewRequest(http.MethodPost, "/api/tasks/batch", bytes.NewBufferString(`{}`))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("Empty tasks: want 400, got %d", rec2.Code)
	}

	// 3. QuÃƒÂ¡ 10 tasks Ã¢â€ â€™ 400 ERR_TOO_MANY_TASKS_MAX_10
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

	t.Log("TestTasksBatchEndpoint: tÃ¡ÂºÂ¥t cÃ¡ÂºÂ£ edge cases PASS")
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
		if r.URL.Path == "/api/session" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"vm_state":"ACTIVE"}`))
			return
		}
		if r.Method == http.MethodPost && r.Header.Get("Next-Action") != "" {
			w.Header().Set("Content-Type", "text/x-component")
			if strings.Contains(cookie, "brand_new_sess") {
				_, _ = w.Write([]byte("0:{\"a\":\"$@1\"}\n1:{\"subscription\":{\"usageRowLabel\":\"GÃƒÂ³i miÃ¡Â»â€¦n phÃƒÂ­\",\"usageRowValueLabel\":\"Ã„ÂÃƒÂ£ dÃƒÂ¹ng 0%\",\"statusSubtitle\":\"GiÃ¡Â»â€ºi hÃ¡ÂºÂ¡n hÃƒÂ ng tuÃ¡ÂºÂ§n sÃ¡ÂºÂ½ Ã„â€˜Ã¡ÂºÂ·t lÃ¡ÂºÂ¡i vÃƒÂ o 10 thÃƒÂ¡ng 11\",\"usage\":{\"percentUsed\":0},\"topupRowLabel\":\"Token bÃ¡Â»â€¢ sung\",\"topupRowValueLabel\":\"Ã„ÂÃƒÂ£ dÃƒÂ¹ng 0% (cÃƒÂ²n 1 tÃ¡Â»Â· token)\",\"billingSubtitle\":\"KhÃƒÂ´ng bao giÃ¡Â»Â hÃ¡ÂºÂ¿t hÃ¡ÂºÂ¡n\"}}\n"))
			} else {
				_, _ = w.Write([]byte("1:{\"subscription\":{\"usageRowLabel\":\"GÃƒÂ³i miÃ¡Â»â€¦n phÃƒÂ­\",\"usageRowValueLabel\":\"Ã„ÂÃƒÂ£ dÃƒÂ¹ng 18%\",\"statusSubtitle\":\"GiÃ¡Â»â€ºi hÃ¡ÂºÂ¡n hÃƒÂ ng tuÃ¡ÂºÂ§n sÃ¡ÂºÂ½ Ã„â€˜Ã¡ÂºÂ·t lÃ¡ÂºÂ¡i vÃƒÂ o 10 thÃƒÂ¡ng 11\",\"usage\":{\"percentUsed\":18}}}\n"))
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
	if nodeNew.FreePlanPercent != 0 || nodeNew.FreePlanValue != "Ã„ÂÃƒÂ£ dÃƒÂ¹ng 0%" {
		t.Fatalf("Expected brand new account to have 0%% ('Ã„ÂÃƒÂ£ dÃƒÂ¹ng 0%%'), got %d%% (%q)", nodeNew.FreePlanPercent, nodeNew.FreePlanValue)
	}

	nodeUsed, err := app.pool.AddAccountNode(context.Background(), "Muse #1", "Muse.ai", "cookie", mockMuse.URL, "hatch_sess=used_sess_9876543210", 0, "")
	if err != nil {
		t.Fatalf("AddAccountNode used: %v", err)
	}
	if nodeUsed.FreePlanPercent != 18 || nodeUsed.FreePlanValue != "Ã„ÂÃƒÂ£ dÃƒÂ¹ng 18%" {
		t.Fatalf("Expected used account to have 18%% ('Ã„ÂÃƒÂ£ dÃƒÂ¹ng 18%%'), got %d%% (%q)", nodeUsed.FreePlanPercent, nodeUsed.FreePlanValue)
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

	// 1. Ã„ÂÃ„Æ’ng nhÃ¡ÂºÂ­p Admin Ã„â€˜Ã¡Â»Æ’ kiÃ¡Â»Æ’m tra SQL40 Status
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
	var sqlStatus MySQLStatusResponse
	_ = json.Unmarshal(recSQL.Body.Bytes(), &sqlStatus)
	if !sqlStatus.Connected || !strings.HasPrefix(sqlStatus.EngineVersion, "SQL40 Standard") {
		t.Fatalf("Expected SQL40 Standard engine, got connected=%v version=%q", sqlStatus.Connected, sqlStatus.EngineVersion)
	}

	// 2. Ã„ÂÃ„Æ’ng kÃƒÂ½ 2 tÃƒÂ i khoÃ¡ÂºÂ£n ngÃ†Â°Ã¡Â»Âi dÃƒÂ¹ng Ã„â€˜Ã¡Â»â„¢c lÃ¡ÂºÂ­p: user_a vÃƒÂ  user_b
	recRegA := doReq(http.MethodPost, "/api/users/register", "", map[string]string{
		"username":     "user_a",
		"display_name": "Form RiÃƒÂªng A",
		"password":     "PassUserA123",
	})
	if recRegA.Code != http.StatusOK {
		t.Fatalf("Register user_a failed: %d %s", recRegA.Code, recRegA.Body.String())
	}
	var regA struct {
		Token string       `json:"token"`
		User  RequestActor `json:"user"`
	}
	_ = json.Unmarshal(recRegA.Body.Bytes(), &regA)

	recRegB := doReq(http.MethodPost, "/api/users/register", "", map[string]string{
		"username":     "user_b",
		"display_name": "Form RiÃƒÂªng B",
		"password":     "PassUserB123",
	})
	if recRegB.Code != http.StatusOK {
		t.Fatalf("Register user_b failed: %d %s", recRegB.Code, recRegB.Body.String())
	}
	var regB struct {
		Token string       `json:"token"`
		User  RequestActor `json:"user"`
	}
	_ = json.Unmarshal(recRegB.Body.Bytes(), &regB)

	// 3. user_a tÃ¡ÂºÂ¡o Ã„â€˜oÃ¡ÂºÂ¡n chat, tÃƒÂ i liÃ¡Â»â€¡u, bÃ¡Â»â„¢ nhÃ¡Â»â€º vÃƒÂ  nhiÃ¡Â»â€¡m vÃ¡Â»Â¥ riÃƒÂªng
	recThreadA := doReq(http.MethodPost, "/api/threads", regA.Token, map[string]string{
		"title": "DÃ¡Â»Â± ÃƒÂ¡n mÃ¡ÂºÂ­t cÃ¡Â»Â§a User A",
		"model": "Muse",
	})
	if recThreadA.Code != http.StatusOK {
		t.Fatalf("Create thread A failed: %d", recThreadA.Code)
	}
	var threadA ChatThread
	_ = json.Unmarshal(recThreadA.Body.Bytes(), &threadA)

	recDocA := doReq(http.MethodPost, "/api/workspace/docs", regA.Token, map[string]string{
		"action":  "save",
		"title":   "BÃƒÂ¡o cÃƒÂ¡o tÃƒÂ i chÃƒÂ­nh A",
		"content": "NÃ¡Â»â„¢i dung chÃ¡Â»â€° dÃƒÂ nh cho User A",
	})
	if recDocA.Code != http.StatusOK {
		t.Fatalf("Create doc A failed: %d %s", recDocA.Code, recDocA.Body.String())
	}

	recMemA := doReq(http.MethodPost, "/api/workspace/memories", regA.Token, map[string]string{
		"action":  "save",
		"title":   "BÃ¡Â»â„¢ nhÃ¡Â»â€º riÃƒÂªng A",
		"content": "LuÃƒÂ´n xÃ†Â°ng hÃƒÂ´ vÃ¡Â»â€ºi User A",
	})
	if recMemA.Code != http.StatusOK {
		t.Fatalf("Create memory A failed: %d", recMemA.Code)
	}

	recTaskA := doReq(http.MethodPost, "/api/workspace/tasks", regA.Token, map[string]string{
		"action": "save",
		"title":  "NhiÃ¡Â»â€¡m vÃ¡Â»Â¥ riÃƒÂªng A",
		"prompt": "PhÃƒÂ¢n tÃƒÂ­ch dÃ¡Â»Â¯ liÃ¡Â»â€¡u A",
	})
	if recTaskA.Code != http.StatusOK {
		t.Fatalf("Create task A failed: %d", recTaskA.Code)
	}

	// 4. user_b tÃ¡ÂºÂ¡o Ã„â€˜oÃ¡ÂºÂ¡n chat vÃƒÂ  tÃƒÂ i liÃ¡Â»â€¡u riÃƒÂªng
	recThreadB := doReq(http.MethodPost, "/api/threads", regB.Token, map[string]string{
		"title": "KÃ¡ÂºÂ¿ hoÃ¡ÂºÂ¡ch riÃƒÂªng cÃ¡Â»Â§a User B",
		"model": "Muse",
	})
	if recThreadB.Code != http.StatusOK {
		t.Fatalf("Create thread B failed: %d", recThreadB.Code)
	}

	recDocB := doReq(http.MethodPost, "/api/workspace/docs", regB.Token, map[string]string{
		"action":  "save",
		"title":   "TÃƒÂ i liÃ¡Â»â€¡u cÃ¡Â»Â§a User B",
		"content": "NÃ¡Â»â„¢i dung Ã„â€˜Ã¡Â»â„¢c lÃ¡ÂºÂ­p cÃ¡Â»Â§a B",
	})
	if recDocB.Code != http.StatusOK {
		t.Fatalf("Create doc B failed: %d", recDocB.Code)
	}

	// 5. KiÃ¡Â»Æ’m tra cÃƒÂ´ lÃ¡ÂºÂ­p tuyÃ¡Â»â€¡t Ã„â€˜Ã¡Â»â€˜i: user_b KHÃƒâ€NG nhÃƒÂ¬n thÃ¡ÂºÂ¥y bÃ¡ÂºÂ¥t kÃ¡Â»Â³ dÃ¡Â»Â¯ liÃ¡Â»â€¡u nÃƒÂ o cÃ¡Â»Â§a user_a
	recListThreadsB := doReq(http.MethodGet, "/api/threads", regB.Token, nil)
	var listThreadsB struct {
		Threads []ChatThread `json:"threads"`
	}
	_ = json.Unmarshal(recListThreadsB.Body.Bytes(), &listThreadsB)
	if len(listThreadsB.Threads) != 1 || listThreadsB.Threads[0].Title != "KÃ¡ÂºÂ¿ hoÃ¡ÂºÂ¡ch riÃƒÂªng cÃ¡Â»Â§a User B" {
		t.Fatalf("Isolation breach on threads for user_b: %+v", listThreadsB.Threads)
	}

	recListDocsB := doReq(http.MethodGet, "/api/workspace/docs", regB.Token, nil)
	var listDocsB struct {
		Docs []map[string]string `json:"docs"`
	}
	_ = json.Unmarshal(recListDocsB.Body.Bytes(), &listDocsB)
	if len(listDocsB.Docs) != 1 || listDocsB.Docs[0]["title"] != "TÃƒÂ i liÃ¡Â»â€¡u cÃ¡Â»Â§a User B" {
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

	// 6. user_b cÃ¡Â»â€˜ tÃƒÂ¬nh Ã„â€˜Ã¡Â»Âc hoÃ¡ÂºÂ·c xÃƒÂ³a Ã„â€˜oÃ¡ÂºÂ¡n chat cÃ¡Â»Â§a user_a -> bÃ¡Â»â€¹ chÃ¡ÂºÂ·n 403/404
	recStealMsg := doReq(http.MethodGet, "/api/threads/messages?thread_id="+threadA.ID, regB.Token, nil)
	if recStealMsg.Code != http.StatusForbidden {
		t.Fatalf("Expected 403 Forbidden when user_b reads user_a thread messages, got %d", recStealMsg.Code)
	}

	// 7. user_a cÃ¡Â»â€˜ tÃƒÂ¬nh gÃ¡Â»Âi API Admin -> bÃ¡Â»â€¹ chÃ¡ÂºÂ·n 403 Forbidden
	recForbidAdmin := doReq(http.MethodGet, "/api/admin/users", regA.Token, nil)
	if recForbidAdmin.Code != http.StatusForbidden {
		t.Fatalf("Expected 403 Forbidden when user_a calls /api/admin/users, got %d", recForbidAdmin.Code)
	}

	// 8. Admin xem danh sÃƒÂ¡ch tÃƒÂ i khoÃ¡ÂºÂ£n vÃƒÂ  xÃƒÂ³a user_a -> dÃ¡Â»Â¯ liÃ¡Â»â€¡u user_b vÃ¡ÂºÂ«n cÃƒÂ²n nguyÃƒÂªn vÃ¡ÂºÂ¹n
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

	// XÃƒÂ¡c nhÃ¡ÂºÂ­n user_b vÃ¡ÂºÂ«n cÃƒÂ²n nguyÃƒÂªn Ã„â€˜oÃ¡ÂºÂ¡n chat vÃƒÂ  tÃƒÂ i liÃ¡Â»â€¡u
	recCheckB := doReq(http.MethodGet, "/api/threads", regB.Token, nil)
	_ = json.Unmarshal(recCheckB.Body.Bytes(), &listThreadsB)
	if len(listThreadsB.Threads) != 1 {
		t.Fatalf("Expected user_b threads to remain intact after deleting user_a, got %d", len(listThreadsB.Threads))
	}
}

func TestAivenMySQLPushAndLiveSync(t *testing.T) {
	mysqlURL := strings.TrimSpace(os.Getenv("MUSE_PUSH_MYSQL_URL"))
	if mysqlURL == "" {
		mysqlURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	prodDataDir := filepath.Join("..", "data")
	if mysqlURL == "" {
		enc := LoadEncryptedCloudSQLConfig(prodDataDir)
		if enc != "" && !strings.Contains(enc, "aivencloud.com") {
			mysqlURL = enc
		}
	}
	if mysqlURL == "" {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:3306", 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			mysqlURL = "mysql://root:@127.0.0.1:3306/muse_router"
		}
	}
	if mysqlURL == "" {
		t.Skip("Skipping live MySQL sync test (no DATABASE_URL or local MySQL)")
	}

	if err := SaveEncryptedCloudSQLConfig(prodDataDir, mysqlURL); err != nil {
		t.Fatalf("SaveEncryptedCloudSQLConfig failed: %v", err)
	}

	// MÃ¡Â»Å¸ kho MySQL / TiDB Cloud trÃ¡Â»Â±c tiÃ¡ÂºÂ¿p qua TLS 1.2+ hoÃ¡ÂºÂ·c Local MySQL
	remoteStore, err := NewMySQLStore(prodDataDir, mysqlURL, "")
	if err != nil {
		t.Fatalf("Connect to MySQL failed: %v", err)
	}
	defer remoteStore.Close()

	if !remoteStore.isMySQL {
		t.Fatalf("Expected remoteStore.isMySQL == true")
	}

	// Ã„ÂÃ¡ÂºÂ£m bÃ¡ÂºÂ£o tÃƒÂ i khoÃ¡ÂºÂ£n QuÃ¡ÂºÂ£n trÃ¡Â»â€¹ viÃƒÂªn (admin) cÃƒÂ³ mÃ¡ÂºÂ·t trong Aiven MySQL Cloud
	ensureAdminInStore := func(st *MySQLStore) {
		qRes, err := st.ExecuteSQL("SELECT id FROM mart_users WHERE id = 'admin' OR username = 'admin' LIMIT 1", nil)
		if err == nil && len(qRes.Rows) > 0 {
			return
		}
		pwHash, err := HashPasswordPBKDF2(TestBootstrapPassword)
		if err != nil {
			t.Fatalf("HashPasswordPBKDF2 admin failed: %v", err)
		}
		nowStr := time.Now().Format("2006-01-02 15:04:05")
		if _, err := st.ExecuteSQL(
			`INSERT INTO mart_users(id, username, display_name, password_hash, role, is_disabled, created_at, last_login_at)
			 VALUES('admin', 'admin', 'QuÃ¡ÂºÂ£n trÃ¡Â»â€¹ viÃƒÂªn (Admin)', ?, 'admin', 0, ?, ?)`,
			[]interface{}{pwHash, nowStr, nowStr},
		); err != nil {
			t.Fatalf("Insert default admin into store failed: %v", err)
		}
	}
	ensureAdminInStore(remoteStore)

	st := remoteStore.GetStatus()
	if !st.Connected {
		t.Fatalf("Expected Aiven MySQL Connected == true")
	}
	t.Logf("Aiven MySQL Status: Engine=%s URL=%s Tables=%+v", st.EngineVersion, st.DatabaseURL, st.Tables)

	if szRes, errSz := remoteStore.ExecuteSQL(`
		SELECT table_schema,
		       COUNT(1) AS table_count,
		       ROUND(SUM(data_length + index_length) / 1024, 2) AS total_kb
		FROM information_schema.tables
		GROUP BY table_schema
	`, nil); errSz == nil {
		for _, r := range szRes.Rows {
			t.Logf("Schema Size: %v", r)
		}
	}
	if accRes, errAcc := remoteStore.ExecuteSQL("SELECT id, label, fingerprint FROM muse_accounts", nil); errAcc == nil {
		for _, r := range accRes.Rows {
			t.Logf("muse_accounts Row: %v", r)
		}
	}
	// GiÃ¡Â»Â¯ database test sÃ¡ÂºÂ¡ch sÃ¡ÂºÂ½
	if remoteStore.db != nil {
		remoteStore.resetTestTables()
	}
}

func TestAdminAccountLoginSettingsAndChatFlow(t *testing.T) {
	t.Setenv("MART_ADMIN_PASSWORD", TestBootstrapPassword)
	tmpDir := t.TempDir()
	app, err := NewServerApp(tmpDir)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}
	defer app.pool.GetMySQLStore().Close()

	handler := app.Routes()

	// 1. Ã„ÂÃ„Æ’ng nhÃ¡ÂºÂ­p tÃƒÂ i khoÃ¡ÂºÂ£n Admin mÃ¡ÂºÂ·c Ã„â€˜Ã¡Â»â€¹nh qua /api/users/login
	loginBody := `{"username":"admin","password":"` + TestBootstrapPassword + `"}`
	reqLogin := httptest.NewRequest(http.MethodPost, "/api/users/login", strings.NewReader(loginBody))
	reqLogin.Header.Set("Content-Type", "application/json")
	reqLogin.Header.Set("X-Mart-Client", "web")
	recLogin := httptest.NewRecorder()
	handler.ServeHTTP(recLogin, reqLogin)
	if recLogin.Code != http.StatusOK {
		t.Fatalf("Admin login failed: status=%d body=%s", recLogin.Code, recLogin.Body.String())
	}
	var loginResp struct {
		Token string `json:"token"`
		User  struct {
			ID          string `json:"id"`
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			Role        string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(recLogin.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("Unmarshal admin login response: %v", err)
	}
	if loginResp.Token == "" || loginResp.User.Role != "admin" || loginResp.User.Username != "admin" {
		t.Fatalf("Unexpected admin login payload: %+v", loginResp)
	}

	// 2. KiÃ¡Â»Æ’m tra quyÃ¡Â»Ân truy cÃ¡ÂºÂ­p CÃƒÂ i Ã„â€˜Ã¡ÂºÂ·t Web (/api/admin/users & /api/router/config)
	reqUsers := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	reqUsers.Header.Set("Authorization", "Bearer "+loginResp.Token)
	reqUsers.Header.Set("X-Mart-Client", "web")
	recUsers := httptest.NewRecorder()
	handler.ServeHTTP(recUsers, reqUsers)
	if recUsers.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/users failed: status=%d body=%s", recUsers.Code, recUsers.Body.String())
	}

	reqCfg := httptest.NewRequest(http.MethodPost, "/api/router/config", strings.NewReader(`{"routing_strategy":"weighted_token","auto_failover":true}`))
	reqCfg.Header.Set("Content-Type", "application/json")
	reqCfg.Header.Set("Authorization", "Bearer "+loginResp.Token)
	reqCfg.Header.Set("X-Mart-Client", "web")
	recCfg := httptest.NewRecorder()
	handler.ServeHTTP(recCfg, reqCfg)
	if recCfg.Code != http.StatusOK {
		t.Fatalf("POST /api/router/config failed: status=%d body=%s", recCfg.Code, recCfg.Body.String())
	}

	// 3. KiÃ¡Â»Æ’m tra Giao diÃ¡Â»â€¡n Chat & Workspace cÃ¡Â»Â§a Admin (/api/threads & /api/workspace/docs)
	reqThread := httptest.NewRequest(http.MethodPost, "/api/threads", strings.NewReader(`{"title":"Ã„ÂoÃ¡ÂºÂ¡n chat Admin","model":""}`))
	reqThread.Header.Set("Content-Type", "application/json")
	reqThread.Header.Set("Authorization", "Bearer "+loginResp.Token)
	reqThread.Header.Set("X-Mart-Client", "web")
	recThread := httptest.NewRecorder()
	handler.ServeHTTP(recThread, reqThread)
	if recThread.Code != http.StatusOK {
		t.Fatalf("POST /api/threads for Admin failed: status=%d body=%s", recThread.Code, recThread.Body.String())
	}
}

func TestResponseSpeedBenchmark(t *testing.T) {
	t.Setenv("MART_ADMIN_PASSWORD", TestBootstrapPassword)
	tmpDir := t.TempDir()
	app, err := NewServerApp(tmpDir)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}
	defer func() {
		if st := app.pool.GetMySQLStore(); st != nil {
			_ = st.Close()
		}
	}()

	handler := app.Routes()

	// LÃ¡ÂºÂ¥y JWT Admin
	loginStart := time.Now()
	loginBody := `{"username":"admin","password":"` + TestBootstrapPassword + `"}`
	reqLogin := httptest.NewRequest(http.MethodPost, "/api/users/login", strings.NewReader(loginBody))
	reqLogin.Header.Set("Content-Type", "application/json")
	reqLogin.Header.Set("X-Mart-Client", "web")
	recLogin := httptest.NewRecorder()
	handler.ServeHTTP(recLogin, reqLogin)
	loginDur := time.Since(loginStart)
	if recLogin.Code != http.StatusOK {
		t.Fatalf("Admin login failed: %d", recLogin.Code)
	}
	var loginResp struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(recLogin.Body.Bytes(), &loginResp)
	token := loginResp.Token

	type benchCase struct {
		name   string
		method string
		path   string
		body   string
		gzip   bool
	}
	cases := []benchCase{
		{name: "GET / (HTML Shell)", method: http.MethodGet, path: "/", gzip: true},
		{name: "GET /app.js (Gzip Static Cache)", method: http.MethodGet, path: "/app.js", gzip: true},
		{name: "GET /styles.css (Gzip Static Cache)", method: http.MethodGet, path: "/styles.css", gzip: true},
		{name: "GET /api/health", method: http.MethodGet, path: "/api/health"},
		{name: "GET /api/users/me (JWT RS256 Verify)", method: http.MethodGet, path: "/api/users/me"},
		{name: "GET /api/router/summary", method: http.MethodGet, path: "/api/router/summary"},
		{name: "GET /api/threads (Chat History)", method: http.MethodGet, path: "/api/threads"},
		{name: "GET /api/workspace/docs", method: http.MethodGet, path: "/api/workspace/docs"},
		{name: "GET /api/workspace/memories", method: http.MethodGet, path: "/api/workspace/memories"},
		{name: "GET /api/workspace/tasks", method: http.MethodGet, path: "/api/workspace/tasks"},
		{name: "GET /api/admin/users", method: http.MethodGet, path: "/api/admin/users"},
		{name: "GET /api/mysql/status (Cold 1-Query)", method: http.MethodGet, path: "/api/mysql/status"},
		{name: "GET /api/mysql/status (Warm L1 Cache)", method: http.MethodGet, path: "/api/mysql/status"},
	}

	t.Logf("=== BENCHMARK TOC DO PHAN HOI (SQL40 LOCAL + L1 RAM CACHE) ===")
	t.Logf("%-42s | %10s | %6s", "Endpoint", "Latency", "Status")
	t.Logf("%-42s | %10s | %6d", "POST /api/users/login (PBKDF2+RS256)", loginDur.Round(time.Microsecond), recLogin.Code)

	for _, tc := range cases {
		start := time.Now()
		var bodyReader *strings.Reader
		if tc.body != "" {
			bodyReader = strings.NewReader(tc.body)
		} else {
			bodyReader = strings.NewReader("")
		}
		req := httptest.NewRequest(tc.method, tc.path, bodyReader)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Mart-Client", "web")
		if tc.gzip {
			req.Header.Set("Accept-Encoding", "gzip")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		dur := time.Since(start)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned status %d: %s", tc.name, rec.Code, rec.Body.String())
		}
		t.Logf("%-42s | %10s | %6d", tc.name, dur.Round(time.Microsecond), rec.Code)
	}

	// KiÃ¡Â»Æ’m tra tÃ¡Â»â€˜c Ã„â€˜Ã¡Â»â„¢ thÃ¡Â»Â±c tÃ¡ÂºÂ¿ trÃƒÂªn Aiven MySQL Cloud TLS nÃ¡ÂºÂ¿u cÃƒÂ³ cÃ¡ÂºÂ¥u hÃƒÂ¬nh mÃƒÂ£ hÃƒÂ³a sql40_cloud.enc
	cloudURL := LoadEncryptedCloudSQLConfig(`f:\tools muse ai\data`)
	if strings.HasPrefix(strings.ToLower(cloudURL), "mysql://") {
		connStart := time.Now()
		cloudStore, errCloud := NewMySQLStore(t.TempDir(), cloudURL, "")
		connDur := time.Since(connStart)
		if errCloud == nil {
			defer cloudStore.Close()

			q1Start := time.Now()
			stCold := cloudStore.GetStatus()
			q1Dur := time.Since(q1Start)

			q2Start := time.Now()
			_ = cloudStore.GetStatus()
			q2Dur := time.Since(q2Start)

			selStart := time.Now()
			_, errSel := cloudStore.ExecuteSQL("SELECT id, username, role FROM mart_users WHERE username = 'admin' LIMIT 1", nil)
			selDur := time.Since(selStart)
			if errSel != nil {
				t.Fatalf("Cloud SQL select admin failed: %v", errSel)
			}

			t.Logf("=== BENCHMARK KET NOI AIVEN MYSQL CLOUD TLS 1.2+ ===")
			t.Logf("Engine Version                         : %s", stCold.EngineVersion)
			t.Logf("MySQL TLS Connect + Fast Schema Check  : %s", connDur.Round(time.Millisecond))
			t.Logf("MySQL Point Query (SELECT admin)       : %s", selDur.Round(time.Microsecond))
			t.Logf("MySQL GetStatus (Cold 1-Query 9 Tables): %s", q1Dur.Round(time.Microsecond))
			t.Logf("MySQL GetStatus (Warm L1 RAM Cache)    : %s", q2Dur.Round(time.Microsecond))
		}
	}
}

func TestRichContentAndSnapshotExtractionIntegrity(t *testing.T) {
	// Kiem tra script snapshot JavaScript chua day du cac logic phan tich da phuong tien va cau truc phong phu
	if !strings.Contains(cdpMuseSnapshotJS, `tag === "video"`) {
		t.Fatalf("cdpMuseSnapshotJS thieu logic trich xuat video")
	}
	if !strings.Contains(cdpMuseSnapshotJS, `tag === "iframe"`) {
		t.Fatalf("cdpMuseSnapshotJS thieu logic trich xuat iframe embed video")
	}
	if !strings.Contains(cdpMuseSnapshotJS, `tag === "table"`) {
		t.Fatalf("cdpMuseSnapshotJS thieu logic trich xuat table thanh Markdown Table")
	}
	if !strings.Contains(cdpMuseSnapshotJS, `tag === "blockquote"`) {
		t.Fatalf("cdpMuseSnapshotJS thieu logic trich xuat blockquote")
	}
	if !strings.Contains(cdpMuseSnapshotJS, `isGenerating`) {
		t.Fatalf("cdpMuseSnapshotJS thieu kiem tra trang thai isGenerating de chong ngat som")
	}
	if !strings.Contains(cdpMuseSnapshotJS, `standardFormattingTags`) {
		t.Fatalf("cdpMuseSnapshotJS thieu logic bao toan the XML custom elements")
	}
}

func TestAccountSyncAndDeletionIntegrity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "muse_sync_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mysqlURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if mysqlURL == "" {
		enc := LoadEncryptedCloudSQLConfig(filepath.Join("..", "data"))
		if enc != "" && !strings.Contains(enc, "aivencloud.com") {
			mysqlURL = enc
		}
	}
	if mysqlURL == "" {
		conn, errConn := net.DialTimeout("tcp", "127.0.0.1:3306", 500*time.Millisecond)
		if errConn == nil {
			_ = conn.Close()
			mysqlURL = "mysql://root:@127.0.0.1:3306/muse_router"
		}
	}
	if mysqlURL == "" {
		t.Skip("Skipping account sync test: no MySQL available")
	}

	store, err := NewMySQLStore(tmpDir, mysqlURL, "")
	if err != nil {
		t.Fatalf("NewMySQLStore failed: %v", err)
	}
	defer store.Close()

	if !store.isMySQL || store.db == nil {
		t.Skip("MySQL not active in store")
	}

	nodeA := &MuseAccountNode{
		ID:                  "test-acc-1",
		NodeIndex:           1,
		Label:               "Test Acc #1",
		PlanTier:            "Muse.ai",
		AuthType:            "cookie",
		UpstreamURL:         "https://muse.ai",
		EncryptedCredential: "test-enc-cred-1",
		Fingerprint:         "fp_test_unit_1",
		Status:              "ready",
	}
	nodeB := &MuseAccountNode{
		ID:                  "test-acc-2",
		NodeIndex:           2,
		Label:               "Test Acc #2",
		PlanTier:            "Muse.ai",
		AuthType:            "cookie",
		UpstreamURL:         "https://muse.ai",
		EncryptedCredential: "test-enc-cred-2",
		Fingerprint:         "fp_test_unit_2",
		Status:              "ready",
	}

	// 1. ThÃƒÂªm 2 nodes vÃƒÂ o MySQL
	if err := store.UpsertAccount(nodeA); err != nil {
		t.Fatalf("UpsertAccount A failed: %v", err)
	}
	if err := store.UpsertAccount(nodeB); err != nil {
		t.Fatalf("UpsertAccount B failed: %v", err)
	}

	// 2. Ã„ÂÃ¡Â»â€œng bÃ¡Â»â„¢ 2 nodes qua SyncAllAccounts
	if err := store.SyncAllAccounts([]*MuseAccountNode{nodeA, nodeB}); err != nil {
		t.Fatalf("SyncAllAccounts with 2 nodes failed: %v", err)
	}

	// 3. XÃƒÂ³a nodeB
	if err := store.DeleteAccount(nodeB.ID, nodeB.Fingerprint); err != nil {
		t.Fatalf("DeleteAccount nodeB failed: %v", err)
	}

	// 4. Ã„ÂÃ¡Â»â€œng bÃ¡Â»â„¢ lÃ¡ÂºÂ¡i chÃ¡Â»â€° vÃ¡Â»â€ºi nodeA (mÃƒÂ´ phÃ¡Â»Âng viÃ¡Â»â€¡c ngÃ†Â°Ã¡Â»Âi dÃƒÂ¹ng nhÃ¡ÂºÂ¥n 'Ã„ÂÃ¡Â»â€œng bÃ¡Â»â„¢ SQL')
	if err := store.SyncAllAccounts([]*MuseAccountNode{nodeA}); err != nil {
		t.Fatalf("SyncAllAccounts with 1 node failed: %v", err)
	}

	// 5. NÃ¡ÂºÂ¡p lÃ¡ÂºÂ¡i tÃƒÂ i khoÃ¡ÂºÂ£n tÃ¡Â»Â« MySQL Ã¢â‚¬â€ BÃ¡ÂºÂ®T BUÃ¡Â»ËœC chÃ¡Â»â€° Ã„â€˜Ã†Â°Ã¡Â»Â£c cÃƒÂ³ Ã„â€˜ÃƒÂºng 1 tÃƒÂ i khoÃ¡ÂºÂ£n (nodeA), nodeB KHÃƒâ€NG Ã„ÂÃ†Â¯Ã¡Â»Â¢C PHÃƒâ€°P HÃ¡Â»â€™I SINH
	loaded, err := store.LoadAccounts()
	if err != nil {
		t.Fatalf("LoadAccounts failed: %v", err)
	}

	foundB := false
	for _, l := range loaded {
		if l.ID == nodeB.ID || l.Fingerprint == nodeB.Fingerprint {
			foundB = true
		}
	}
	if foundB {
		t.Fatalf("CRITICAL ERROR: nodeB da bi xoa nhung lai bi hoi sinh sau khi dong bo SQL!")
	}

	// DÃ¡Â»Ân dÃ¡ÂºÂ¹p node test
	_ = store.DeleteAccount(nodeA.ID, nodeA.Fingerprint)
	_ = store.DeleteAccount(nodeB.ID, nodeB.Fingerprint)
	t.Logf("PASS: Account Sync & Deletion Reconciliation test passed perfectly!")
}

func TestSSEChatSendNeverCorruptedByGzipAndCleansPendingTurn(t *testing.T) {
	t.Setenv("MART_ADMIN_PASSWORD", "")
	t.Setenv("RENDER", "true")
	tmpDir := t.TempDir()
	app, err := NewServerApp(filepath.Join(tmpDir, "data"))
	if err != nil {
		t.Fatalf("NewServerApp: %v", err)
	}
	defer app.pool.Shutdown()

	// CÃ¡ÂºÂ¯m 1 tÃƒÂ i khoÃ¡ÂºÂ£n hÃ¡Â»Â£p lÃ¡Â»â€¡ vÃƒÂ o pool Ã„â€˜Ã¡Â»Æ’ test luÃ¡Â»â€œng SSE + Browser Relay + Cleanup Pending Turn
	encCookie, err := app.pool.vault.EncryptSecret("hatch_sess=test_valid_session_cookie_123456")
	if err != nil {
		t.Fatalf("EncryptSecret: %v", err)
	}
	app.pool.mu.Lock()
	app.pool.cdpManager = nil
	app.pool.nodes = append(app.pool.nodes, &MuseAccountNode{
		ID:                  "node-sse-test",
		NodeIndex:           1,
		Label:               "Muse Test #1",
		PlanTier:            "Muse.ai",
		AuthType:            "cookie",
		UpstreamURL:         "https://muse.ai",
		EncryptedCredential: encCookie,
		Fingerprint:         "fp_sse_test_1",
		HasHatchSess:        true,
		Status:              "ready",
	})
	app.pool.mu.Unlock()

	handler := app.Routes()
	token, err := app.jwtMgr.IssueUserToken("admin", "admin", "QuÃ¡ÂºÂ£n trÃ¡Â»â€¹ viÃƒÂªn", "admin", 15*time.Minute)
	if err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}

	chatPayload := `{"thread_id":"thread_sse_1","preferred_node_id":"auto","workspace":"chat","prompt":"alo","stream":true,"client_relay_capable":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/chat/send", strings.NewReader(chatPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Mart-Client", "web")
	req.Header.Set("X-Mart-Ext-Relay", "1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/chat/send status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "" {
		t.Fatalf("Expected empty Content-Encoding on SSE stream, got %q", ce)
	}
	rawSSE := rec.Body.String()
	if !strings.Contains(rawSSE, "event: start\n") || !strings.Contains(rawSSE, "event: done\n") {
		t.Fatalf("Expected both 'event: start' and 'event: done' in plain UTF-8 SSE body, got:\n%s", rawSSE)
	}

	var doneData map[string]interface{}
	for _, block := range strings.Split(rawSSE, "\n\n") {
		if strings.Contains(block, "event: done") {
			for _, line := range strings.Split(block, "\n") {
				if strings.HasPrefix(line, "data:") {
					_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &doneData)
				}
			}
		}
	}
	if doneData == nil || doneData["browser_relay"] != true {
		t.Fatalf("Expected browser_relay=true in SSE done event, got: %+v", doneData)
	}
	turnID, _ := doneData["turn_id"].(string)
	if turnID == "" {
		t.Fatalf("Expected non-empty turn_id in SSE done event")
	}

	// HÃ¡Â»Â§y lÃ†Â°Ã¡Â»Â£t relay qua /api/chat/relay-complete -> turn pending phÃ¡ÂºÂ£i Ã„â€˜Ã†Â°Ã¡Â»Â£c dÃ¡Â»Ân sÃ¡ÂºÂ¡ch khÃ¡Â»Âi thread
	cancelPayload, _ := json.Marshal(map[string]string{
		"thread_id": "thread_sse_1",
		"turn_id":   turnID,
		"node_id":   "node-sse-test",
		"error":     "USER_STOPPED",
	})
	reqCancel := httptest.NewRequest(http.MethodPost, "/api/chat/relay-complete", bytes.NewReader(cancelPayload))
	reqCancel.Header.Set("Content-Type", "application/json")
	reqCancel.Header.Set("Authorization", "Bearer "+token)
	reqCancel.Header.Set("X-Mart-Client", "web")
	recCancel := httptest.NewRecorder()
	handler.ServeHTTP(recCancel, reqCancel)
	if recCancel.Code != http.StatusOK {
		t.Fatalf("POST /api/chat/relay-complete status=%d body=%s", recCancel.Code, recCancel.Body.String())
	}

	app.pool.mu.RLock()
	for _, th := range app.pool.threads {
		if th.ID == "thread_sse_1" {
			for _, m := range th.Messages {
				if m.Pending || (m.Role == "assistant" && strings.TrimSpace(m.Content) == "") {
					app.pool.mu.RUnlock()
					t.Fatalf("Expected pending/empty assistant turn to be cleaned up, but found: %+v", m)
				}
			}
		}
	}
	app.pool.mu.RUnlock()
}

func TestPurgeBrokenNodesAndAutoCaptureEndpoints(t *testing.T) {
	tmpDir := t.TempDir()
	app, err := NewServerApp(tmpDir)
	if err != nil {
		t.Fatalf("NewServerApp: %v", err)
	}
	defer app.pool.GetMySQLStore().Close()
	handler := app.Routes()

	// ThÃƒÂªm 1 node giÃ¡ÂºÂ£ bÃ¡Â»â€¹ hÃ¡Â»Âng mÃƒÂ£ hÃƒÂ³a (encrypted credential khÃƒÂ´ng hÃ¡Â»Â£p lÃ¡Â»â€¡)
	app.pool.mu.Lock()
	brokenNode := &MuseAccountNode{
		ID:                  "broken-test-node-1",
		Label:               "Broken Node",
		EncryptedCredential: "v1:010203040506070809101112:deadbeef:badtag0000000000000000000000000000000000000000000000000000000000",
		Status:              "ready",
	}
	app.pool.nodes = append(app.pool.nodes, brokenNode)
	app.pool.mu.Unlock()

	// GÃ¡Â»Âi API /api/accounts/purge-broken
	reqPurge := httptest.NewRequest(http.MethodPost, "/api/accounts/purge-broken", nil)
	recPurge := httptest.NewRecorder()
	handler.ServeHTTP(recPurge, reqPurge)

	if recPurge.Code != http.StatusOK {
		t.Fatalf("POST /api/accounts/purge-broken status=%d body=%s", recPurge.Code, recPurge.Body.String())
	}

	var purgeRes map[string]interface{}
	if err := json.Unmarshal(recPurge.Body.Bytes(), &purgeRes); err != nil {
		t.Fatalf("Invalid json response: %v", err)
	}
	purgedVal, _ := purgeRes["purged"].(float64)
	if purgedVal < 1 {
		t.Fatalf("Expected at least 1 purged node, got %v", purgedVal)
	}

	// XÃƒÂ¡c nhÃ¡ÂºÂ­n broken node Ã„â€˜ÃƒÂ£ bÃ¡Â»â€¹ xÃƒÂ³a sÃ¡ÂºÂ¡ch khÃ¡Â»Âi pool
	app.pool.mu.RLock()
	for _, n := range app.pool.nodes {
		if n.ID == "broken-test-node-1" {
			app.pool.mu.RUnlock()
			t.Fatalf("Expected broken-test-node-1 to be purged from pool!")
		}
	}
	app.pool.mu.RUnlock()
	t.Logf("PASS: PurgeBrokenNodes test succeeded, cleaned %v broken node(s)", purgedVal)
}
