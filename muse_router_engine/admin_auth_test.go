package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractCookieValue(t *testing.T) {
	cookie := "datr=abc; hatch_sess=SESS123==; hatch_gw=gw"
	if got := extractCookieValue(cookie, "hatch_sess"); got != "SESS123==" {
		t.Fatalf("hatch_sess = %q", got)
	}
	if got := extractCookieValue(cookie, "missing"); got != "" {
		t.Fatalf("missing cookie should be empty, got %q", got)
	}
	if got := extractCookieValue("hatch_sess", "hatch_sess"); got != "" {
		t.Fatalf("cookie without value should be empty, got %q", got)
	}
}

func TestAdminAuthMiddleware(t *testing.T) {
	const pw = "unit-test-admin-pw"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := AdminAuthMiddleware(next)

	do := func(method, path, authHeader string) int {
		req := httptest.NewRequest(method, path, nil)
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	t.Setenv("MART_ADMIN_PASSWORD", "")
	if code := do(http.MethodGet, "/api/router/summary", ""); code != http.StatusOK {
		t.Fatalf("no password configured: expected open (200), got %d", code)
	}

	t.Setenv("MART_ADMIN_PASSWORD", pw)
	if code := do(http.MethodGet, "/api/router/summary", ""); code != http.StatusUnauthorized {
		t.Fatalf("missing credentials: expected 401, got %d", code)
	}
	if code := do(http.MethodGet, "/api/router/summary", "Bearer wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: expected 401, got %d", code)
	}
	if code := do(http.MethodGet, "/api/router/summary", "Bearer "+pw); code != http.StatusOK {
		t.Fatalf("correct bearer: expected 200, got %d", code)
	}
	basic := "Basic " + encodeBasicForTest("anyuser", pw)
	if code := do(http.MethodGet, "/admin", basic); code != http.StatusOK {
		t.Fatalf("correct basic: expected 200, got %d", code)
	}
	if code := do(http.MethodGet, "/api/health", ""); code != http.StatusOK {
		t.Fatalf("health must stay open, got %d", code)
	}
	if code := do(http.MethodOptions, "/api/accounts/ingest", ""); code != http.StatusOK {
		t.Fatalf("preflight must pass through, got %d", code)
	}
}

func encodeBasicForTest(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func TestIngestEndpointSecurity(t *testing.T) {
	tempDir := filepath.Join(".", "test_ingest_security")
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	app, err := NewServerApp(tempDir)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}
	defer app.pool.Shutdown()
	handler := app.Routes()

	post := func(auth, origin, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/accounts/ingest", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1) Chưa đặt mật khẩu admin -> luôn từ chối (503)
	t.Setenv("MART_ADMIN_PASSWORD", "")
	if rec := post("", "", `{"cookie":"hatch_sess=x"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no admin password: expected 503, got %d", rec.Code)
	}

	// 2) Đã đặt mật khẩu: thiếu / sai thông tin -> 401
	const pw = "unit-test-admin-pw"
	t.Setenv("MART_ADMIN_PASSWORD", pw)
	if rec := post("", "", `{"cookie":"hatch_sess=x"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: expected 401, got %d", rec.Code)
	}
	if rec := post("Bearer nope", "", `{"cookie":"hatch_sess=x"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong credentials: expected 401, got %d", rec.Code)
	}

	// 3) Đúng mật khẩu nhưng cookie thiếu hatch_sess -> 400
	if rec := post("Bearer "+pw, "", `{"cookie":"datr=abc"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing hatch_sess: expected 400, got %d", rec.Code)
	}

	// 4) CORS: đúng ID extension được phép, ID khác bị chặn
	rec := post("Bearer "+pw, "chrome-extension://mjlkhmjhbhkjfccffeoimoindodmglaj", `{"cookie":"datr=abc"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("allowed extension origin should reach handler (400), got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "chrome-extension://mjlkhmjhbhkjfccffeoimoindodmglaj" {
		t.Fatalf("ACAO header = %q", got)
	}
	if rec := post("Bearer "+pw, "chrome-extension://aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", `{"cookie":"hatch_sess=x"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("unknown extension origin: expected 403, got %d", rec.Code)
	}

	// 5) Phiên trình duyệt đã vượt qua Basic Auth nhận JWT RS256 vẫn gọi được /api/router/summary, nhưng không thay thế mật khẩu trực tiếp trên /api/accounts/ingest
	jwtTok, err := app.jwtMgr.IssueToken("muse_router_operator", 5*time.Minute)
	if err != nil {
		t.Fatalf("IssueToken failed: %v", err)
	}
	sumReq := httptest.NewRequest(http.MethodGet, "/api/router/summary", nil)
	sumReq.Header.Set("Authorization", "Bearer "+jwtTok)
	sumRec := httptest.NewRecorder()
	handler.ServeHTTP(sumRec, sumReq)
	if sumRec.Code != http.StatusOK {
		t.Fatalf("RS256 session JWT on /api/router/summary: expected 200, got %d", sumRec.Code)
	}
	if rec := post("Bearer "+jwtTok, "", `{"cookie":"hatch_sess=x"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("RS256 session JWT must not authorize /api/accounts/ingest without admin password: got %d", rec.Code)
	}

	// 6) Endpoint tải gói tiện ích Chrome (.zip) chứa đủ 5 file
	zipReq := httptest.NewRequest(http.MethodGet, "/api/accounts/extension.zip", nil)
	zipReq.Header.Set("Authorization", "Bearer "+pw)
	zipRec := httptest.NewRecorder()
	handler.ServeHTTP(zipRec, zipReq)
	if zipRec.Code != http.StatusOK {
		t.Fatalf("extension.zip expected 200, got %d", zipRec.Code)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipRec.Body.Bytes()), int64(zipRec.Body.Len()))
	if err != nil {
		t.Fatalf("invalid zip archive: %v", err)
	}
	if len(zr.File) != 5 {
		t.Fatalf("expected 5 files in extension.zip, got %d", len(zr.File))
	}
}

func TestWriteJSONErrorEscapesQuotes(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSONError(rec, http.StatusBadGateway, `Không kết nối được dịch vụ AI Bridge: Post "http://127.0.0.1:18089/internal/chat": dial tcp`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	var parsed map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("WriteJSONError produced invalid JSON (%v): %s", err, rec.Body.String())
	}
	if !strings.Contains(parsed["error"], `"http://127.0.0.1:18089/internal/chat"`) {
		t.Fatalf("unexpected error field: %q", parsed["error"])
	}
}

func TestCloudBrowserRelayFlowAndNodeHealth(t *testing.T) {
	t.Setenv("MART_ADMIN_PASSWORD", "")
	tempDir := filepath.Join(".", "test_cloud_relay_flow")
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	app, err := NewServerApp(tempDir)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}
	defer app.pool.Shutdown()
	// Trỏ AI Bridge tới cổng đóng để mô phỏng môi trường Render Cloud không có Python Playwright
	app.pool.aiBridgeURL = "http://127.0.0.1:1"

	encCred, err := app.pool.rustBridge.EncryptSecret("hatch_sess=TEST_SESS_VALUE; datr=abc")
	if err != nil {
		t.Fatalf("EncryptSecret failed: %v", err)
	}
	fp := app.pool.rustBridge.ComputeFingerprint("hatch_sess=TEST_SESS_VALUE")

	app.pool.mu.Lock()
	app.pool.nodes = append(app.pool.nodes, &MuseAccountNode{
		ID:                  "node_test_render_1",
		Label:               "Muse #1",
		PlanTier:            "Muse.ai",
		AuthType:            "cookie",
		UpstreamURL:         "https://muse.ai",
		EncryptedCredential: encCred,
		Fingerprint:         fp,
		HasHatchSess:        true,
		Status:              "unavailable",
		StatusDetail:        `Không kết nối được dịch vụ AI Bridge trên Muse #1: Post "http://127.0.0.1:18089/internal/chat": connection refused`,
	})
	normalizeAccountIndices(app.pool.nodes)
	app.pool.mu.Unlock()

	sum := app.pool.GetSummary()
	if sum.ReadyNodes != 1 || sum.UnavailableNodes != 0 {
		t.Fatalf("expected Muse #1 auto-healed to ready, got summary=%+v", sum)
	}

	handler := app.Routes()

	// 1) Gửi chat trên Render -> trả 200 browser_relay=true thay vì 502
	sendReq := httptest.NewRequest(http.MethodPost, "/api/chat/send", strings.NewReader(`{"prompt":"xin chao"}`))
	sendReq.Header.Set("Content-Type", "application/json")
	sendRec := httptest.NewRecorder()
	handler.ServeHTTP(sendRec, sendReq)
	if sendRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for browser_relay, got %d: %s", sendRec.Code, sendRec.Body.String())
	}
	var relayPayload map[string]interface{}
	if err := json.Unmarshal(sendRec.Body.Bytes(), &relayPayload); err != nil {
		t.Fatalf("unmarshal relayPayload: %v", err)
	}
	if relayPayload["browser_relay"] != true {
		t.Fatalf("expected browser_relay=true, got %+v", relayPayload)
	}

	// 2) Khi chưa cài tiện ích (hủy lượt chờ) -> Muse #1 vẫn phải giữ trạng thái ready (không bị Ngắt kết nối)
	cancelBody, _ := json.Marshal(map[string]interface{}{
		"thread_id": relayPayload["thread_id"],
		"turn_id":   relayPayload["turn_id"],
		"node_id":   relayPayload["node_id"],
		"error":     "EXT_REQUIRED",
	})
	cancelReq := httptest.NewRequest(http.MethodPost, "/api/chat/relay-complete", bytes.NewReader(cancelBody))
	cancelReq.Header.Set("Content-Type", "application/json")
	cancelRec := httptest.NewRecorder()
	handler.ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel relay expected 200, got %d", cancelRec.Code)
	}
	sumAfterCancel := app.pool.GetSummary()
	if sumAfterCancel.ReadyNodes != 1 || sumAfterCancel.UnavailableNodes != 0 {
		t.Fatalf("Muse #1 must remain ready after cancel, got %+v", sumAfterCancel)
	}

	// 3) Gửi lại khi đã có tiện ích v1.1 -> hoàn tất trả lời & cập nhật token/request
	sendReq2 := httptest.NewRequest(http.MethodPost, "/api/chat/send", strings.NewReader(`{"prompt":"xin chao"}`))
	sendReq2.Header.Set("Content-Type", "application/json")
	sendRec2 := httptest.NewRecorder()
	handler.ServeHTTP(sendRec2, sendReq2)
	var relay2 map[string]interface{}
	_ = json.Unmarshal(sendRec2.Body.Bytes(), &relay2)

	completeBody, _ := json.Marshal(map[string]interface{}{
		"thread_id":   relay2["thread_id"],
		"turn_id":     relay2["turn_id"],
		"node_id":     relay2["node_id"],
		"reply":       "Chào bạn! Mình là Muse.",
		"tokens_used": 28,
		"latency_ms":  640,
	})
	compReq := httptest.NewRequest(http.MethodPost, "/api/chat/relay-complete", bytes.NewReader(completeBody))
	compReq.Header.Set("Content-Type", "application/json")
	compRec := httptest.NewRecorder()
	handler.ServeHTTP(compRec, compReq)
	if compRec.Code != http.StatusOK {
		t.Fatalf("complete relay expected 200, got %d: %s", compRec.Code, compRec.Body.String())
	}
	var compData map[string]interface{}
	_ = json.Unmarshal(compRec.Body.Bytes(), &compData)
	if compData["reply"] != "Chào bạn! Mình là Muse." {
		t.Fatalf("unexpected reply: %+v", compData)
	}
	sumFinal := app.pool.GetSummary()
	if sumFinal.ReadyNodes != 1 || sumFinal.TotalRequests != 1 || sumFinal.TotalTokensUsed != 28 {
		t.Fatalf("Muse #1 must stay ready with updated telemetry after completion, got %+v", sumFinal)
	}
}
