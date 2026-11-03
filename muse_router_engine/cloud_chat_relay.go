package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (p *MuseRouterPool) unmarkNodeRunningKeepReady(nodeID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.nodes {
		if n.ID == nodeID {
			if n.ActiveRequests > 0 {
				n.ActiveRequests--
			}
			if n.ActiveRequests == 0 && n.Status == "running" {
				n.Status = "ready"
			}
			_ = p.saveVaultLocked()
			break
		}
	}
}

func (p *MuseRouterPool) executeOpenAICompatibleDirect(
	ctx context.Context,
	candidate *MuseAccountNode,
	model string,
	messagesPayload []map[string]string,
) (string, int64, error) {
	rawCred, err := p.rustBridge.DecryptSecret(candidate.EncryptedCredential)
	if err != nil {
		return "", 0, err
	}
	base := strings.TrimRight(strings.TrimSpace(candidate.UpstreamURL), "/")
	if base == "" {
		base = "https://muse.ai"
	}
	endpoint := base + "/v1/chat/completions"
	if strings.HasSuffix(base, "/v1") {
		endpoint = base + "/chat/completions"
	}

	payload := map[string]interface{}{
		"messages": messagesPayload,
		"stream":   false,
	}
	if strings.TrimSpace(model) != "" {
		payload["model"] = model
	}
	rawBody, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if candidate.AuthType == "api_key" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(rawCred))
	} else {
		req.Header.Set("Cookie", strings.TrimSpace(rawCred))
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("Upstream trả về HTTP %d", resp.StatusCode)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", 0, err
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", 0, errors.New("Upstream trả về phản hồi rỗng")
	}
	return parsed.Choices[0].Message.Content, parsed.Usage.TotalTokens, nil
}

func (p *MuseRouterPool) prepareBrowserRelayResponse(
	candidate *MuseAccountNode,
	resolvedThreadID string,
	turnID string,
	model string,
	userPrompt string,
) (map[string]interface{}, error) {
	rawCred, err := p.rustBridge.DecryptSecret(candidate.EncryptedCredential)
	if err != nil {
		p.unmarkNodeRunningKeepReady(candidate.ID)
		p.cancelQuestionTurn(resolvedThreadID, turnID)
		return nil, err
	}
	cookieHeader := strings.TrimSpace(rawCred)
	if !strings.Contains(cookieHeader, "=") && cookieHeader != "" {
		cookieHeader = "hatch_sess=" + cookieHeader
	}

	// Phòng hờ trình duyệt người dùng đóng tab giữa chừng: tự giải phóng slot sau 95 giây mà không làm hỏng trạng thái ready.
	go func(nodeID string) {
		time.Sleep(95 * time.Second)
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, n := range p.nodes {
			if n.ID == nodeID && n.ActiveRequests > 0 {
				if time.Now().UnixMilli()-n.LastUsedUnixMs >= 90000 {
					n.ActiveRequests = 0
					if n.Status == "running" {
						n.Status = "ready"
					}
					_ = p.saveVaultLocked()
				}
			}
		}
	}(candidate.ID)

	return map[string]interface{}{
		"browser_relay": true,
		"thread_id":     resolvedThreadID,
		"turn_id":       turnID,
		"node_id":       candidate.ID,
		"routed_node":   candidate.Label,
		"model":         model,
		"prompt":        userPrompt,
		"relay_cookie":  cookieHeader,
	}, nil
}

func (p *MuseRouterPool) CompleteBrowserRelayTurn(
	threadID string,
	turnID string,
	nodeID string,
	model string,
	reply string,
	tokensUsed int64,
	latencyMs int,
	errDetail string,
) (map[string]interface{}, error) {
	if strings.TrimSpace(errDetail) != "" || strings.TrimSpace(reply) == "" {
		p.unmarkNodeRunningKeepReady(nodeID)
		p.cancelQuestionTurn(threadID, turnID)
		if errDetail == "" {
			errDetail = "Chưa nhận được phản hồi từ phiên trình duyệt Muse.ai"
		}
		return map[string]interface{}{
			"cancelled": true,
			"error":     errDetail,
		}, nil
	}

	if strings.TrimSpace(model) == "" {
		model = "Muse"
	}
	if tokensUsed <= 0 {
		tokensUsed = int64(len(reply)/4) + 1
	}
	if latencyMs <= 0 {
		latencyMs = 1200
	}

	var nodeLabel = "Muse"
	p.mu.RLock()
	for _, n := range p.nodes {
		if n.ID == nodeID {
			nodeLabel = n.Label
			break
		}
	}
	p.mu.RUnlock()

	p.releaseNode(nodeID, tokensUsed, latencyMs, true, "")

	updatedThread := p.completeQuestionTurn(
		threadID,
		turnID,
		reply,
		nodeLabel,
		model,
		latencyMs,
		int(tokensUsed),
	)
	if p.libsqlStore != nil && updatedThread != nil {
		p.libsqlStore.RecordRoutingTelemetry(
			updatedThread.ID,
			nodeID,
			nodeLabel,
			model,
			int64(latencyMs),
			tokensUsed,
			"ok",
		)
	}

	return map[string]interface{}{
		"thread":      updatedThread,
		"turn_id":     turnID,
		"routed_node": nodeLabel,
		"node_id":     nodeID,
		"model":       model,
		"latency_ms":  latencyMs,
		"tokens_used": tokensUsed,
		"reply":       reply,
	}, nil
}

func (a *ServerApp) handleChatRelayComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	var req struct {
		ThreadID   string `json:"thread_id"`
		TurnID     string `json:"turn_id"`
		NodeID     string `json:"node_id"`
		Model      string `json:"model"`
		Reply      string `json:"reply"`
		TokensUsed int64  `json:"tokens_used"`
		LatencyMs  int    `json:"latency_ms"`
		Error      string `json:"error"`
	}
	if err := a.readJSONBody(r, &req); err != nil {
		WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}
	res, err := a.pool.CompleteBrowserRelayTurn(
		req.ThreadID,
		req.TurnID,
		req.NodeID,
		req.Model,
		req.Reply,
		req.TokensUsed,
		req.LatencyMs,
		req.Error,
	)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	res["summary"] = a.pool.GetSummary()
	WriteJSONResponse(w, http.StatusOK, res)
}

func (p *MuseRouterPool) CancelIfBrowserRelay(res map[string]interface{}) bool {
	if res == nil {
		return false
	}
	isRelay, _ := res["browser_relay"].(bool)
	if !isRelay {
		return false
	}
	threadID, _ := res["thread_id"].(string)
	turnID, _ := res["turn_id"].(string)
	nodeID, _ := res["node_id"].(string)
	_, _ = p.CompleteBrowserRelayTurn(threadID, turnID, nodeID, "", "", 0, 0, "ERR_HEADLESS_RELAY_UNSUPPORTED")
	return true
}
