package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// oauthNativeSessions lÆ°u cookie PKCE sinh ra á»Ÿ bÆ°á»›c táº¡o Link ÄÄƒng Nháº­p (key = state)
// Ä‘á»ƒ bÆ°á»›c dÃ¡n Link Callback cÃ³ thá»ƒ Ä‘á»•i sang cookie hatch_sess mÃ  khÃ´ng cáº§n Python Bridge.
var (
	oauthNativeMu       sync.Mutex
	oauthNativeSessions = map[string]map[string]string{}
	oauthNativeLastKey  string
)

const oauthNativeMaxSessions = 64

func storeOAuthNativeSession(state string, cookies map[string]string) {
	if len(cookies) == 0 {
		return
	}
	oauthNativeMu.Lock()
	defer oauthNativeMu.Unlock()
	if len(oauthNativeSessions) >= oauthNativeMaxSessions {
		oauthNativeSessions = map[string]map[string]string{}
	}
	key := state
	if key == "" {
		key = "_last"
	}
	oauthNativeSessions[key] = cookies
	oauthNativeLastKey = key
}

func loadOAuthNativeSession(state string) map[string]string {
	oauthNativeMu.Lock()
	defer oauthNativeMu.Unlock()
	if s, ok := oauthNativeSessions[state]; ok && state != "" {
		return s
	}
	if s, ok := oauthNativeSessions[oauthNativeLastKey]; ok {
		return s
	}
	return nil
}

func cookiesToHeader(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		if k != "" && v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}

func isOAuthCallbackURL(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "https://muse.ai/oidc/callback") ||
		strings.HasPrefix(t, "https://muse.ai/api/oidc/callback") ||
		strings.HasPrefix(t, "https://auth.muse.ai/oidc/muse")
}

// resolveOAuthCallbackNative Ä‘á»•i Link Callback sang chuá»—i Cookie cÃ³ hatch_sess báº±ng Go thuáº§n.
// Tráº£ vá» (cookie, true) náº¿u thÃ nh cÃ´ng.
func resolveOAuthCallbackNative(ctx context.Context, raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if !isOAuthCallbackURL(trimmed) {
		return raw, false
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	callbackURL := trimmed
	if strings.HasPrefix(trimmed, "https://auth.muse.ai/oidc/muse") {
		reqMid, err := http.NewRequestWithContext(ctx, http.MethodGet, trimmed, nil)
		if err != nil {
			return raw, false
		}
		reqMid.Header.Set("User-Agent", ua)
		respMid, err := client.Do(reqMid)
		if err != nil {
			LogWarn("OAuth callback hop failed: %v", err)
			return raw, false
		}
		respMid.Body.Close()
		if loc := respMid.Header.Get("Location"); loc != "" {
			callbackURL = loc
		}
	}

	state := ""
	if u, err := url.Parse(callbackURL); err == nil {
		state = u.Query().Get("state")
	}
	sess := loadOAuthNativeSession(state)
	if sess == nil {
		LogWarn("OAuth callback: khÃ´ng tÃ¬m tháº¥y phiÃªn PKCE (server cÃ³ thá»ƒ vá»«a khá»Ÿi Ä‘á»™ng láº¡i) â€” hÃ£y táº¡o láº¡i Link ÄÄƒng Nháº­p")
		return raw, false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, callbackURL, nil)
	if err != nil {
		return raw, false
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", "https://muse.ai/")
	req.Header.Set("Cookie", cookiesToHeader(sess))
	resp, err := client.Do(req)
	if err != nil {
		LogWarn("OAuth callback exchange failed: %v", err)
		return raw, false
	}
	resp.Body.Close()

	merged := make(map[string]string, len(sess)+4)
	for k, v := range sess {
		merged[k] = v
	}
	for _, c := range resp.Cookies() {
		if c.Name != "" && c.Value != "" {
			merged[c.Name] = c.Value
		}
	}
	if merged["hatch_sess"] == "" {
		LogWarn("OAuth callback: mÃ¡y chá»§ khÃ´ng tráº£ hatch_sess (HTTP %d) â€” link cÃ³ thá»ƒ Ä‘Ã£ dÃ¹ng rá»“i hoáº·c háº¿t háº¡n", resp.StatusCode)
		return raw, false
	}
	return cookiesToHeader(merged), true
}
