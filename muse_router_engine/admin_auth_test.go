package main

import (
	"archive/zip"
	"bytes"
	"context"
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
		t.Fatalf("standalone admin middleware without password should pass through, got %d", code)
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

	// 1) ChÆ°a Ä‘áº·t máº­t kháº©u admin -> luÃ´n tá»« chá»‘i (503)
	t.Setenv("MART_ADMIN_PASSWORD", "")
	if rec := post("", "", `{"cookie":"hatch_sess=x"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no admin password: expected 503, got %d", rec.Code)
	}

	// 2) ÄÃ£ Ä‘áº·t máº­t kháº©u: thiáº¿u / sai thÃ´ng tin -> 401
	const pw = "unit-test-admin-pw"
	t.Setenv("MART_ADMIN_PASSWORD", pw)
	if rec := post("", "", `{"cookie":"hatch_sess=x"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: expected 401, got %d", rec.Code)
	}
	if rec := post("Bearer nope", "", `{"cookie":"hatch_sess=x"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong credentials: expected 401, got %d", rec.Code)
	}

	// 3) ÄÃºng máº­t kháº©u nhÆ°ng cookie thiáº¿u hatch_sess -> 400
	if rec := post("Bearer "+pw, "", `{"cookie":"datr=abc"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing hatch_sess: expected 400, got %d", rec.Code)
	}

	// 4) CORS: Ä‘Ãºng ID extension Ä‘Æ°á»£c phÃ©p, ID khÃ¡c bá»‹ cháº·n
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

	// 5) PhiÃªn trÃ¬nh duyá»‡t Ä‘Ã£ vÆ°á»£t qua Basic Auth nháº­n JWT RS256 váº«n gá»i Ä‘Æ°á»£c /api/router/summary, nhÆ°ng khÃ´ng thay tháº¿ máº­t kháº©u trá»±c tiáº¿p trÃªn /api/accounts/ingest
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

	// 6) Endpoint táº£i gÃ³i tiá»‡n Ã­ch Chrome (.zip) chá»©a Ä‘á»§ 5 file
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
	if len(zr.File) != 6 {
		t.Fatalf("expected 6 files in extension.zip, got %d", len(zr.File))
	}
}

func TestWriteJSONErrorEscapesQuotes(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSONError(rec, http.StatusBadGateway, `KhÃ´ng káº¿t ná»‘i Ä‘Æ°á»£c dá»‹ch vá»¥ AI Bridge: Post "http://127.0.0.1:18089/internal/chat": dial tcp`)
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
	// Trá» AI Bridge tá»›i cá»•ng Ä‘Ã³ng vÃ  táº¯t cdpManager Ä‘á»ƒ kiá»ƒm thá»­ riÃªng nhÃ¡nh dá»± phÃ²ng browser_relay
	app.pool.aiBridgeURL = "http://127.0.0.1:1"
	if app.pool.cdpManager != nil {
		app.pool.cdpManager.Shutdown()
		app.pool.cdpManager = nil
	}

	encCred, err := app.pool.vault.EncryptSecret("hatch_sess=TEST_SESS_VALUE; datr=abc")
	if err != nil {
		t.Fatalf("EncryptSecret failed: %v", err)
	}
	fp := app.pool.vault.ComputeFingerprint("hatch_sess=TEST_SESS_VALUE")

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
		StatusDetail:        `KhÃ´ng káº¿t ná»‘i Ä‘Æ°á»£c dá»‹ch vá»¥ AI Bridge trÃªn Muse #1: Post "http://127.0.0.1:18089/internal/chat": connection refused`,
	})
	normalizeAccountIndices(app.pool.nodes)
	app.pool.mu.Unlock()

	sum := app.pool.GetSummary()
	if sum.ReadyNodes != 1 || sum.UnavailableNodes != 0 {
		t.Fatalf("expected Muse #1 auto-healed to ready, got summary=%+v", sum)
	}

	handler := app.Routes()

	// 1) Gá»­i chat trÃªn Render -> tráº£ 200 browser_relay=true thay vÃ¬ 502
	sendReq := httptest.NewRequest(http.MethodPost, "/api/chat/send", strings.NewReader(`{"prompt":"xin chao","client_relay_capable":true}`))
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

	// 2) Khi chÆ°a cÃ i tiá»‡n Ã­ch (há»§y lÆ°á»£t chá») -> Muse #1 váº«n pháº£i giá»¯ tráº¡ng thÃ¡i ready (khÃ´ng bá»‹ Ngáº¯t káº¿t ná»‘i)
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

	// 3) Gá»­i láº¡i khi Ä‘Ã£ cÃ³ tiá»‡n Ã­ch v1.1 -> hoÃ n táº¥t tráº£ lá»i & cáº­p nháº­t token/request
	sendReq2 := httptest.NewRequest(http.MethodPost, "/api/chat/send", strings.NewReader(`{"prompt":"xin chao","client_relay_capable":true}`))
	sendReq2.Header.Set("Content-Type", "application/json")
	sendRec2 := httptest.NewRecorder()
	handler.ServeHTTP(sendRec2, sendReq2)
	var relay2 map[string]interface{}
	_ = json.Unmarshal(sendRec2.Body.Bytes(), &relay2)

	completeBody, _ := json.Marshal(map[string]interface{}{
		"thread_id":   relay2["thread_id"],
		"turn_id":     relay2["turn_id"],
		"node_id":     relay2["node_id"],
		"reply":       "ChÃ o báº¡n! MÃ¬nh lÃ  Muse.",
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
	if compData["reply"] != "ChÃ o báº¡n! MÃ¬nh lÃ  Muse." {
		t.Fatalf("unexpected reply: %+v", compData)
	}
	sumFinal := app.pool.GetSummary()
	if sumFinal.ReadyNodes != 1 || sumFinal.TotalRequests != 1 || sumFinal.TotalTokensUsed != 28 {
		t.Fatalf("Muse #1 must stay ready with updated telemetry after completion, got %+v", sumFinal)
	}
}

func TestCDPBrowserEngineLocalEndToEnd(t *testing.T) {
	if bin, _ := findChromeBinary(); bin == "" {
		t.Skip("Skipping CDP live test: no Chrome/Edge binary found on host")
	}

	fakeMuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/login" {
			_, _ = w.Write([]byte(`<!doctype html><html><body style="margin:0;padding:40px;font-family:sans-serif;">
				<input id="emailBox" type="text" style="width:300px;height:40px;" />
				<script>document.getElementById('emailBox').focus();</script>
			</body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<!doctype html><html><body>
			<div id="messages"><div class="prose"><p>Xin chÃ o ban Ä‘áº§u</p></div></div>
			<textarea id="composer" aria-label="Message"></textarea>
			<script>
				var comp = document.getElementById('composer');
				comp.addEventListener('keydown', function(ev) {
					if (ev.key === 'Enter') {
						ev.preventDefault();
						var val = comp.value;
						comp.value = '';
						var d = document.createElement('div');
						d.className = 'prose';
						d.innerHTML = '<p>Pháº£n há»“i CDP cho: <strong>' + val + '</strong></p>';
						document.getElementById('messages').appendChild(d);
					}
				});
			</script>
		</body></html>`))
	}))
	defer fakeMuse.Close()

	tempDir := filepath.Join(".", "test_cdp_live_engine")
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	app, err := NewServerApp(tempDir)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}
	defer app.pool.Shutdown()
	app.pool.aiBridgeURL = "http://127.0.0.1:1"

	// 1) Kiá»ƒm thá»­ Cá»­a sá»• ÄÄƒng nháº­p Trá»±c tiáº¿p (chá»¥p JPEG + gá»­i phÃ­m chá»¯)
	sess, err := app.pool.cdpManager.StartRemoteLoginSession(context.Background(), "Muse Test", "", fakeMuse.URL+"/login")
	if err != nil {
		t.Fatalf("StartRemoteLoginSession failed: %v", err)
	}
	snap, err := app.pool.cdpManager.SendRemoteLoginInput(context.Background(), RemoteLoginInputReq{
		SessionID: sess.ID,
		Type:      "text",
		Text:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("SendRemoteLoginInput failed: %v", err)
	}
	frameStr, _ := snap["frame"].(string)
	if !strings.HasPrefix(frameStr, "data:image/jpeg;base64,") {
		t.Fatalf("expected live JPEG frame from CDP, got prefix: %.32q", frameStr)
	}
	app.pool.cdpManager.CancelRemoteLoginSession(sess.ID)

	// 2) Kiá»ƒm thá»­ Chat tháº­t phÃ­a Server qua CDP (khÃ´ng cáº§n Python Bridge, khÃ´ng cáº§n Extension)
	encCred, err := app.pool.vault.EncryptSecret("hatch_sess=CDP_TEST_SESS")
	if err != nil {
		t.Fatalf("EncryptSecret failed: %v", err)
	}
	fp := app.pool.vault.ComputeFingerprint("hatch_sess=CDP_TEST_SESS")
	app.pool.mu.Lock()
	app.pool.nodes = append(app.pool.nodes, &MuseAccountNode{
		ID:                  "node_cdp_chat_1",
		Label:               "Muse #1",
		PlanTier:            "Muse.ai",
		AuthType:            "cookie",
		UpstreamURL:         fakeMuse.URL,
		EncryptedCredential: encCred,
		Fingerprint:         fp,
		HasHatchSess:        true,
		Status:              "ready",
	})
	normalizeAccountIndices(app.pool.nodes)
	app.pool.mu.Unlock()

	handler := app.Routes()
	sendReq := httptest.NewRequest(http.MethodPost, "/api/chat/send", strings.NewReader(`{"prompt":"ChÃ o Mart AI"}`))
	sendReq.Header.Set("Content-Type", "application/json")
	sendRec := httptest.NewRecorder()
	handler.ServeHTTP(sendRec, sendReq)
	if sendRec.Code != http.StatusOK {
		t.Fatalf("POST /api/chat/send via server CDP failed (%d): %s", sendRec.Code, sendRec.Body.String())
	}
	var chatRes map[string]interface{}
	if err := json.Unmarshal(sendRec.Body.Bytes(), &chatRes); err != nil {
		t.Fatalf("unmarshal chatRes: %v", err)
	}
	replyStr, _ := chatRes["reply"].(string)
	if !strings.Contains(replyStr, "Pháº£n há»“i CDP cho: **ChÃ o Mart AI**") {
		t.Fatalf("unexpected CDP chat reply: %q (full=%+v)", replyStr, chatRes)
	}
}
