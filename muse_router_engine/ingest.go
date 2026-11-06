package main

import (
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:mart_ai_extension
var embeddedExtensionFS embed.FS

// extractCookieValue lấy giá trị của cookie `name` từ chuỗi dạng "a=1; b=2".
func extractCookieValue(cookieHeader, name string) string {
	trimmed := strings.TrimSpace(cookieHeader)
	for _, part := range strings.Split(cookieHeader, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && kv[0] == name {
			return strings.TrimSpace(kv[1])
		}
	}
	if name == "hatch_sess" && len(trimmed) >= 12 && !strings.EqualFold(trimmed, "hatch_sess") &&
		!strings.Contains(trimmed, "=") && !strings.Contains(trimmed, ";") &&
		!strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") &&
		!strings.HasPrefix(strings.ToLower(trimmed), "http://") && !strings.HasPrefix(strings.ToLower(trimmed), "https://") {
		return trimmed
	}
	return ""
}

// IngestAccountCookie nhận chuỗi cookie muse.ai gửi từ extension trình duyệt.
// Nếu hatch_sess đã có trong Pool thì cập nhật cookie cho tài khoản đó (không tạo trùng),
// ngược lại thêm tài khoản mới. Trả về (node, created, error).
func (p *MuseRouterPool) IngestAccountCookie(ctx context.Context, label, cookie string, proxyURLs ...string) (*MuseAccountNode, bool, error) {
	cookie = strings.TrimSpace(cookie)
	newSess := extractCookieValue(cookie, "hatch_sess")
	if newSess == "" {
		return nil, false, errors.New("ERR_MISSING_HATCH_SESS")
	}
	proxyURL := ""
	if len(proxyURLs) > 0 {
		proxyURL = strings.TrimSpace(proxyURLs[0])
	}

	type pair struct{ id, enc string }
	p.mu.RLock()
	pairs := make([]pair, 0, len(p.nodes))
	for _, n := range p.nodes {
		pairs = append(pairs, pair{id: n.ID, enc: n.EncryptedCredential})
	}
	p.mu.RUnlock()

	existingID := ""
	for _, pr := range pairs {
		raw, err := p.rustBridge.DecryptSecret(pr.enc)
		if err != nil {
			LogWarn("Ingest: không giải mã được credential của node %s: %v", pr.id, err)
			continue
		}
		if cur := extractCookieValue(raw, "hatch_sess"); cur != "" && ConstantTimeStringEquals(cur, newSess) {
			existingID = pr.id
			break
		}
	}

	if existingID == "" {
		node, err := p.AddAccountNode(ctx, label, "", "cookie", "", cookie, 0, proxyURL)
		if err != nil {
			return nil, false, err
		}
		return node, true, nil
	}

	enc, err := p.rustBridge.EncryptSecret(cookie)
	if err != nil {
		return nil, false, err
	}
	fp := p.rustBridge.ComputeFingerprint(cookie)
	hasSess, hasGw := detectCookieFlags(cookie)
	p.mu.Lock()
	for _, n := range p.nodes {
		if n.ID == existingID {
			n.EncryptedCredential = enc
			n.Fingerprint = fp
			n.HasHatchSess = hasSess
			n.HasHatchGw = hasGw
			n.DeviceProfile = computeDeterministicDeviceProfile(fp)
			if proxyURL != "" {
				n.ProxyURL = proxyURL
			}
			break
		}
	}
	saveErr := p.saveVaultLocked()
	p.mu.Unlock()
	if saveErr != nil {
		return nil, false, saveErr
	}

	_, _ = p.WakeAndProbeNode(ctx, existingID)

	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, n := range p.nodes {
		if n.ID == existingID {
			c := *n
			c.EncryptedCredential = ""
			return &c, false, nil
		}
	}
	return nil, false, errors.New("ERR_NODE_NOT_FOUND")
}

func (a *ServerApp) handleIngestAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}
	if adminPassword() == "" {
		WriteJSONError(w, http.StatusServiceUnavailable, "ERR_ADMIN_PASSWORD_NOT_SET")
		return
	}
	if !isAdminAuthorized(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Mart AI Admin", charset="UTF-8"`)
		WriteJSONError(w, http.StatusUnauthorized, "ERR_UNAUTHORIZED")
		return
	}
	var req struct {
		Label  string `json:"label"`
		Cookie string `json:"cookie"`
	}
	if err := a.readJSONBody(r, &req); err != nil {
		WriteJSONError(w, http.StatusBadRequest, "ERR_INVALID_INPUT")
		return
	}
	node, created, err := a.pool.IngestAccountCookie(r.Context(), strings.TrimSpace(req.Label), req.Cookie)
	if err != nil {
		LogWarn("Ingest account failed: %v", err)
		code := "ERR_INGEST_FAILED"
		if err.Error() == "ERR_MISSING_HATCH_SESS" {
			code = "ERR_MISSING_HATCH_SESS"
		}
		WriteJSONError(w, http.StatusBadRequest, code)
		return
	}
	LogInfo("Ingest account ok: label=%s created=%v status=%s", SanitizeLog(node.Label), created, node.Status)
	WriteJSONResponse(w, http.StatusOK, map[string]interface{}{
		"created": created,
		"node":    node,
	})
}

// handleDownloadExtensionZip đóng gói thư mục mart_ai_extension thành file .zip trực tiếp từ bộ nhớ
// để người dùng tải 1 chạm ngay trên trang /admin của Render.
func (a *ServerApp) handleDownloadExtensionZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteJSONError(w, http.StatusMethodNotAllowed, "ERR_METHOD_NOT_ALLOWED")
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	subFS, err := fs.Sub(embeddedExtensionFS, "mart_ai_extension")
	if err != nil {
		LogError("Extension embed subFS error: %v", err)
		WriteJSONError(w, http.StatusInternalServerError, "ERR_EXTENSION_BUNDLE")
		return
	}

	err = fs.WalkDir(subFS, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		raw, errRead := fs.ReadFile(subFS, path)
		if errRead != nil {
			return errRead
		}
		fw, errCreate := zw.Create(path)
		if errCreate != nil {
			return errCreate
		}
		_, errWrite := fw.Write(raw)
		return errWrite
	})
	if err != nil {
		_ = zw.Close()
		LogError("Extension zip walk error: %v", err)
		WriteJSONError(w, http.StatusInternalServerError, "ERR_EXTENSION_ZIP")
		return
	}
	if err := zw.Close(); err != nil {
		LogError("Extension zip close error: %v", err)
		WriteJSONError(w, http.StatusInternalServerError, "ERR_EXTENSION_ZIP")
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="mart_ai_extension.zip"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
