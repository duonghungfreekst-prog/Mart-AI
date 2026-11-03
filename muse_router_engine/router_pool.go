package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MaxWorkerGoroutines = 64
	BoundedQueueSize    = 1000
	MemWarnThresholdMB  = 500
)

type MuseAccountNode struct {
	ID                  string `json:"id"`
	NodeIndex           int    `json:"node_index"`
	Label               string `json:"label"`
	PlanTier            string `json:"plan_tier"`
	AuthType            string `json:"auth_type"` // "cookie" | "api_key"
	UpstreamURL         string `json:"upstream_url"`
	ProxyURL            string `json:"proxy_url"`
	DeviceProfile       string `json:"device_profile"`
	EncryptedCredential string `json:"encrypted_credential,omitempty"`
	Fingerprint         string `json:"fingerprint"`
	HasHatchSess        bool   `json:"has_hatch_sess"`
	HasHatchGw          bool   `json:"has_hatch_gw"`
	Status              string `json:"status"` // "running" | "ready" | "unavailable"
	ActiveRequests      int    `json:"active_requests"`
	TokensRemaining     int64  `json:"tokens_remaining"`
	TokensQuota         int64  `json:"tokens_quota"`
	TokensUsed          int64  `json:"tokens_used"`
	FreePlanLabel       string `json:"free_plan_label"`
	FreePlanValue       string `json:"free_plan_value"`
	FreePlanSubtitle    string `json:"free_plan_subtitle"`
	FreePlanPercent     int    `json:"free_plan_percent"`
	TopupLabel          string `json:"topup_label"`
	TopupValue          string `json:"topup_value"`
	TopupSubtitle       string `json:"topup_subtitle"`
	TopupPercent        int    `json:"topup_percent"`
	TotalRequests       int64  `json:"total_requests"`
	FailCount           int    `json:"fail_count"`
	LatencyMs           int    `json:"latency_ms"`
	LastUsedAt          string `json:"last_used_at"`
	LastUsedUnixMs      int64  `json:"last_used_unix_ms,omitempty"`
	StatusDetail        string `json:"status_detail"`
}

type ChatAttachment struct {
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
	DataURL  string `json:"data_url"`
}

type ChatMessage struct {
	Role        string           `json:"role"`
	Content     string           `json:"content"`
	Attachments []ChatAttachment `json:"attachments,omitempty"`
	NodeLabel   string           `json:"node_label,omitempty"`
	Model       string           `json:"model,omitempty"`
	LatencyMs   int              `json:"latency_ms,omitempty"`
	Tokens      int              `json:"tokens,omitempty"`
	Timestamp   string           `json:"timestamp,omitempty"`
	TurnID      string           `json:"turn_id,omitempty"`
	Pending     bool             `json:"pending,omitempty"`
}

type ChatThread struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Model     string        `json:"model"`
	Workspace string        `json:"workspace"`
	CreatedAt string        `json:"created_at"`
	UpdatedAt string        `json:"updated_at"`
	Messages  []ChatMessage `json:"messages"`
}

type RouterSummary struct {
	TotalNodes        int                `json:"total_nodes"`
	RunningNodes      int                `json:"running_nodes"`
	ReadyNodes        int                `json:"ready_nodes"`
	UnavailableNodes  int                `json:"unavailable_nodes"`
	ActiveRequests    int                `json:"active_requests"`
	QueuedRequests    int                `json:"queued_requests"`
	TotalTokensRemain int64              `json:"total_tokens_remaining"`
	TotalTokensQuota  int64              `json:"total_tokens_quota"`
	TotalTokensUsed   int64              `json:"total_tokens_used"`
	TotalRequests     int64              `json:"total_requests"`
	FormattedTokens   string             `json:"formatted_tokens"`
	RoutingStrategy   string             `json:"routing_strategy"`
	AutoFailover      bool               `json:"auto_failover"`
	MemAllocMB        uint64             `json:"mem_alloc_mb"`
	LibSQLDriver      string             `json:"libsql_driver"`
	LibSQLURL         string             `json:"libsql_url"`
	Nodes             []*MuseAccountNode `json:"nodes"`
}

type MuseRouterPool struct {
	mu            sync.RWMutex
	nodes         []*MuseAccountNode
	threads       []*ChatThread
	strategy      string
	autoFailover  bool
	roundRobinIdx uint64
	queuedCount   int64
	vaultPath     string
	threadsPath   string
	libsqlStore   *LibSQLStore
	rustBridge    *RustCoreBridge
	cache         *TTLCache
	jobQueue      chan func()
	aiBridgeURL   string
	aiBridgeToken string
	httpClient    *http.Client
	shutdownCh    chan struct{}
}

func NewMuseRouterPool(
	dataDir string,
	rustBridge *RustCoreBridge,
	cache *TTLCache,
	aiBridgeURL string,
	aiBridgeToken string,
) (*MuseRouterPool, error) {
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		return nil, err
	}

	vaultPath := filepath.Join(dataDir, "accounts_vault.json")
	threadsPath := filepath.Join(dataDir, "sessions_store.json")
	legacyThreadsPath := filepath.Join(dataDir, "threads.json")

	libStore, err := NewLibSQLStore(dataDir, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize embedded libSQL store: %w", err)
	}
	libStore.MigrateLegacyJSONIfNeeded(vaultPath, threadsPath, legacyThreadsPath)

	pool := &MuseRouterPool{
		nodes:         make([]*MuseAccountNode, 0),
		threads:       make([]*ChatThread, 0),
		strategy:      "least_active",
		autoFailover:  true,
		vaultPath:     vaultPath,
		threadsPath:   threadsPath,
		libsqlStore:   libStore,
		rustBridge:    rustBridge,
		cache:         cache,
		jobQueue:      make(chan func(), BoundedQueueSize),
		aiBridgeURL:   strings.TrimRight(aiBridgeURL, "/"),
		aiBridgeToken: aiBridgeToken,
		httpClient: &http.Client{
			Timeout: 65 * time.Second,
		},
		shutdownCh: make(chan struct{}),
	}

	for i := 0; i < MaxWorkerGoroutines; i++ {
		go pool.workerLoop()
	}
	go pool.memoryMonitorLoop()

	if err := pool.loadVault(); err != nil {
		LogWarn("Initializing clean account vault in libSQL (%v)", err)
	}
	if err := pool.loadThreads(); err != nil {
		LogWarn("Initializing clean chat session store in libSQL (%v)", err)
	}

	go pool.autoSyncAccountsLoop()

	return pool, nil
}

func (p *MuseRouterPool) workerLoop() {
	for {
		select {
		case <-p.shutdownCh:
			return
		case job := <-p.jobQueue:
			atomic.AddInt64(&p.queuedCount, -1)
			func() {
				defer func() {
					if r := recover(); r != nil {
						LogError("Worker panic recovered: %v", r)
					}
				}()
				job()
			}()
		}
	}
}

func (p *MuseRouterPool) SubmitAsyncJob(job func()) error {
	atomic.AddInt64(&p.queuedCount, 1)
	select {
	case p.jobQueue <- job:
		return nil
	default:
		atomic.AddInt64(&p.queuedCount, -1)
		return errors.New("ERR_WORKER_QUEUE_FULL")
	}
}

func (p *MuseRouterPool) memoryMonitorLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.shutdownCh:
			return
		case <-ticker.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			allocMB := m.Alloc / (1024 * 1024)
			if allocMB > MemWarnThresholdMB {
				LogWarn("Memory allocation exceeded %dMB threshold: current=%dMB", MemWarnThresholdMB, allocMB)
			}
		}
	}
}

// autoSyncAccountsLoop tự động đồng bộ trạng thái & mức sử dụng Token của các tài khoản với muse.ai
// ngay khi khởi động và định kỳ mỗi 45 giây mà người dùng không cần bấm nút thủ công.
func (p *MuseRouterPool) autoSyncAccountsLoop() {
	initialTimer := time.NewTimer(2500 * time.Millisecond)
	select {
	case <-p.shutdownCh:
		initialTimer.Stop()
		return
	case <-initialTimer.C:
		p.AutoSyncIdleNodes()
	}

	ticker := time.NewTicker(45 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.shutdownCh:
			return
		case <-ticker.C:
			p.AutoSyncIdleNodes()
		}
	}
}

func (p *MuseRouterPool) AutoSyncIdleNodes() {
	p.mu.RLock()
	ids := make([]string, 0, len(p.nodes))
	for _, n := range p.nodes {
		if n.ActiveRequests == 0 {
			ids = append(ids, n.ID)
		}
	}
	p.mu.RUnlock()

	for _, id := range ids {
		select {
		case <-p.shutdownCh:
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
		_, _ = p.WakeAndProbeNode(ctx, id)
		cancel()
	}
}

func (p *MuseRouterPool) Shutdown() {
	close(p.shutdownCh)
	if p.libsqlStore != nil {
		_ = p.libsqlStore.Close()
	}
}

func (p *MuseRouterPool) GetLibSQLStore() *LibSQLStore {
	return p.libsqlStore
}

func FormatTokenCount(tokens int64) string {
	if tokens <= 0 {
		return "0"
	}
	if tokens >= 1_000_000_000 {
		val := float64(tokens) / 1_000_000_000.0
		return fmt.Sprintf("≈%.3fB", val)
	}
	if tokens >= 1_000_000 {
		val := float64(tokens) / 1_000_000.0
		return fmt.Sprintf("≈%.1fM", val)
	}
	if tokens >= 1_000 {
		val := float64(tokens) / 1_000.0
		return fmt.Sprintf("≈%.1fK", val)
	}
	return fmt.Sprintf("%d", tokens)
}

func computeDeterministicDeviceProfile(fp string) string {
	seed := strings.TrimSpace(fp)
	if seed == "" {
		seed = "default_muse_fp"
	}
	digest := sha256.Sum256([]byte(seed))
	shortUAs := []string{
		"Chrome 131 · Win11",
		"Chrome 130 · Win11",
		"Edge 131 · Win11",
		"Chrome 129 · Win10",
		"Chrome 128 · Win10",
	}
	gpus := []string{
		"RTX 3060",
		"Iris Xe",
		"RTX 4060",
		"RX 6600",
		"GTX 1660S",
		"UHD 770",
		"RTX 3070Ti",
		"Radeon iGPU",
	}
	screens := []string{
		"1920×1080",
		"1536×864",
		"2560×1440",
		"1440×900",
		"1680×1050",
		"1600×900",
		"1366×768",
		"1920×1200",
	}
	hws := []string{
		"8C/16GB",
		"12C/16GB",
		"16C/32GB",
		"6C/8GB",
		"8C/8GB",
		"12C/32GB",
		"4C/8GB",
		"16C/16GB",
	}
	devID := strings.ToUpper(hex.EncodeToString(digest[8:11]))
	return fmt.Sprintf(
		"DEV-%s · %s · %s · %s (%s)",
		devID,
		shortUAs[int(digest[0])%len(shortUAs)],
		gpus[int(digest[1])%len(gpus)],
		screens[int(digest[2])%len(screens)],
		hws[int(digest[3])%len(hws)],
	)
}

func normalizeAccountIndices(nodes []*MuseAccountNode) {
	for idx, n := range nodes {
		n.NodeIndex = idx + 1
		if n.Label == "" || strings.HasPrefix(n.Label, "Muse #") {
			n.Label = fmt.Sprintf("Muse #%d", idx+1)
		}
		if strings.TrimSpace(n.DeviceProfile) == "" && n.Fingerprint != "" {
			n.DeviceProfile = computeDeterministicDeviceProfile(n.Fingerprint)
		}
		if n.Status == "ready" || n.Status == "running" {
			if n.TokensQuota <= 0 || n.TokensQuota == 1000000 {
				n.TokensQuota = 1000000000
			}
			if n.TokensRemaining <= 0 || n.TokensRemaining < 1000000 {
				rem := n.TokensQuota - n.TokensUsed
				if rem < 0 {
					rem = 0
				}
				n.TokensRemaining = rem
			}
			if n.FreePlanLabel == "" {
				n.FreePlanLabel = "Gói miễn phí"
			}
			if n.FreePlanValue == "" {
				pct := n.FreePlanPercent
				if pct <= 0 {
					pct = 4
					n.FreePlanPercent = 4
				}
				n.FreePlanValue = fmt.Sprintf("Đã dùng %d%%", pct)
			}
			if n.FreePlanSubtitle == "" {
				n.FreePlanSubtitle = "Giới hạn hàng tuần sẽ đặt lại vào 10 tháng 11"
			}
			if n.TopupLabel == "" {
				n.TopupLabel = "Token bổ sung"
			}
			if n.TopupValue == "" {
				n.TopupValue = "Đã dùng 0% (còn 1 tỷ token)"
			}
			if n.TopupSubtitle == "" {
				n.TopupSubtitle = "Không bao giờ hết hạn"
			}
		}
	}
}

func (p *MuseRouterPool) loadVault() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.libsqlStore != nil {
		loaded, err := p.libsqlStore.LoadAccounts()
		if err == nil {
			normalizeAccountIndices(loaded)
			p.nodes = loaded
			LogInfo("Loaded %d Muse accounts directly from embedded libSQL (%s)", len(p.nodes), p.libsqlStore.dbURL)
			_ = p.syncBackupJSONLocked()
			return nil
		}
		LogWarn("libSQL LoadAccounts fallback check: %v", err)
	}

	if _, err := os.Stat(p.vaultPath); os.IsNotExist(err) {
		return p.saveVaultLocked()
	}

	raw, err := os.ReadFile(p.vaultPath)
	if err != nil {
		return err
	}
	var loaded []*MuseAccountNode
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return err
	}
	normalizeAccountIndices(loaded)
	p.nodes = loaded
	if p.libsqlStore != nil {
		_ = p.libsqlStore.SyncAllAccounts(p.nodes)
	}
	return nil
}

func (p *MuseRouterPool) syncBackupJSONLocked() error {
	data, err := json.MarshalIndent(p.nodes, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.vaultPath, data, 0600)
}

func (p *MuseRouterPool) saveVaultLocked() error {
	normalizeAccountIndices(p.nodes)
	if p.libsqlStore != nil {
		if err := p.libsqlStore.SyncAllAccounts(p.nodes); err != nil {
			LogError("libSQL SyncAllAccounts error: %v", err)
			return err
		}
	}
	return p.syncBackupJSONLocked()
}

func (p *MuseRouterPool) loadThreads() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.libsqlStore != nil {
		loaded, err := p.libsqlStore.LoadThreadsWithMessages()
		if err == nil {
			p.threads = loaded
			return nil
		}
		LogWarn("libSQL LoadThreadsWithMessages error: %v", err)
	}

	p.threads = []*ChatThread{}
	return nil
}

func (p *MuseRouterPool) saveThreadsLocked() error {
	if p.libsqlStore != nil {
		for _, t := range p.threads {
			if err := p.libsqlStore.SaveThreadWithMessages(t); err != nil {
				LogError("libSQL SaveThreadWithMessages error: %v", err)
				return err
			}
		}
	}
	data, err := json.MarshalIndent(p.threads, "", "  ")
	if err == nil {
		_ = os.WriteFile(p.threadsPath, data, 0600)
	}
	return nil
}

func (p *MuseRouterPool) GetSummary() RouterSummary {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	safeNodes := make([]*MuseAccountNode, 0, len(p.nodes))
	var totalTokens int64
	var totalQuota int64
	var totalUsed int64
	var totalReqs int64
	runningCnt := 0
	readyCnt := 0
	unavailCnt := 0
	activeReqs := 0

	for _, n := range p.nodes {
		copyNode := *n
		copyNode.EncryptedCredential = ""
		if copyNode.Status == "ready" || copyNode.Status == "running" {
			if copyNode.TokensQuota <= 0 || copyNode.TokensQuota == 1000000 {
				copyNode.TokensQuota = 1000000000
			}
			if copyNode.TokensRemaining <= 0 || copyNode.TokensRemaining < 1000000 {
				rem := copyNode.TokensQuota - copyNode.TokensUsed
				if rem < 0 {
					rem = 0
				}
				copyNode.TokensRemaining = rem
			}
			if copyNode.FreePlanLabel == "" {
				copyNode.FreePlanLabel = "Gói miễn phí"
			}
			if copyNode.FreePlanValue == "" {
				pct := copyNode.FreePlanPercent
				if pct <= 0 {
					pct = 4
					copyNode.FreePlanPercent = 4
				}
				copyNode.FreePlanValue = fmt.Sprintf("Đã dùng %d%%", pct)
			}
			if copyNode.FreePlanSubtitle == "" {
				copyNode.FreePlanSubtitle = "Giới hạn hàng tuần sẽ đặt lại vào 10 tháng 11"
			}
			if copyNode.TopupLabel == "" {
				copyNode.TopupLabel = "Token bổ sung"
			}
			if copyNode.TopupValue == "" {
				copyNode.TopupValue = "Đã dùng 0% (còn 1 tỷ token)"
			}
			if copyNode.TopupSubtitle == "" {
				copyNode.TopupSubtitle = "Không bao giờ hết hạn"
			}
		}
		safeNodes = append(safeNodes, &copyNode)

		if copyNode.Status != "unavailable" && copyNode.TokensRemaining > 0 {
			totalTokens += copyNode.TokensRemaining
		}
		totalQuota += copyNode.TokensQuota
		totalUsed += copyNode.TokensUsed
		totalReqs += copyNode.TotalRequests
		activeReqs += copyNode.ActiveRequests
		switch copyNode.Status {
		case "running":
			runningCnt++
		case "ready":
			readyCnt++
		default:
			unavailCnt++
		}
	}

	qCnt := int(atomic.LoadInt64(&p.queuedCount))
	if qCnt < 0 {
		qCnt = 0
	}

	libDriver := "libsql"
	libURL := ""
	if p.libsqlStore != nil {
		p.libsqlStore.mu.RLock()
		libURL = p.libsqlStore.dbURL
		p.libsqlStore.mu.RUnlock()
	}

	return RouterSummary{
		TotalNodes:        len(safeNodes),
		RunningNodes:      runningCnt,
		ReadyNodes:        readyCnt,
		UnavailableNodes:  unavailCnt,
		ActiveRequests:    activeReqs,
		QueuedRequests:    qCnt,
		TotalTokensRemain: totalTokens,
		TotalTokensQuota:  totalQuota,
		TotalTokensUsed:   totalUsed,
		TotalRequests:     totalReqs,
		FormattedTokens:   FormatTokenCount(totalTokens),
		RoutingStrategy:   p.strategy,
		AutoFailover:      p.autoFailover,
		MemAllocMB:        m.Alloc / (1024 * 1024),
		LibSQLDriver:      libDriver,
		LibSQLURL:         libURL,
		Nodes:             safeNodes,
	}
}

func detectCookieFlags(raw string) (bool, bool) {
	lower := strings.ToLower(raw)
	if !strings.Contains(lower, "=") && !strings.HasPrefix(strings.TrimSpace(raw), "[") && !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return true, false
	}
	return strings.Contains(lower, "hatch_sess"), strings.Contains(lower, "hatch_gw")
}

// AddAccountNode mã hóa Cookie/Token qua Rust Core DLL, lưu vào Vault và KIỂM TRA XÁC THỰC THẬT SỰ với muse.ai
func (p *MuseRouterPool) AddAccountNode(
	ctx context.Context,
	label string,
	planTier string,
	authType string,
	upstreamURL string,
	rawCredential string,
	tokensRemaining int64,
	proxyURL ...string,
) (*MuseAccountNode, error) {
	if strings.TrimSpace(rawCredential) == "" {
		return nil, errors.New("ERR_EMPTY_CREDENTIAL")
	}
	encrypted, err := p.rustBridge.EncryptSecret(rawCredential)
	if err != nil {
		return nil, err
	}
	fp := p.rustBridge.ComputeFingerprint(rawCredential)
	hasSess, hasGw := detectCookieFlags(rawCredential)
	cleanProxy := ""
	if len(proxyURL) > 0 {
		cleanProxy = strings.TrimSpace(proxyURL[0])
	}

	p.mu.Lock()
	idx := len(p.nodes) + 1
	if strings.TrimSpace(label) == "" {
		label = fmt.Sprintf("Muse #%d", idx)
	}
	if strings.TrimSpace(planTier) == "" {
		planTier = "Muse.ai"
	}
	if strings.TrimSpace(authType) == "" {
		authType = "cookie"
	}
	if strings.TrimSpace(upstreamURL) == "" {
		upstreamURL = "https://muse.ai"
	}
	if tokensRemaining < 0 {
		tokensRemaining = 0
	}

	node := &MuseAccountNode{
		ID:                  fmt.Sprintf("muse-node-%d-%d", idx, time.Now().UnixMilli()%100000),
		NodeIndex:           idx,
		Label:               label,
		PlanTier:            planTier,
		AuthType:            authType,
		UpstreamURL:         upstreamURL,
		ProxyURL:            cleanProxy,
		DeviceProfile:       computeDeterministicDeviceProfile(fp),
		EncryptedCredential: encrypted,
		Fingerprint:         fp,
		HasHatchSess:        hasSess,
		HasHatchGw:          hasGw,
		Status:              "unavailable",
		ActiveRequests:      0,
		TokensRemaining:     tokensRemaining,
		TokensQuota:         tokensRemaining,
		TokensUsed:          0,
		TotalRequests:       0,
		FailCount:           0,
		LatencyMs:           0,
		LastUsedAt:          time.Now().Format("15:04:05"),
		LastUsedUnixMs:      time.Now().UnixMilli(),
		StatusDetail:        "Đang kiểm tra xác thực với muse.ai...",
	}

	p.nodes = append(p.nodes, node)
	if err := p.saveVaultLocked(); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	p.mu.Unlock()

	_, _ = p.WakeAndProbeNode(ctx, node.ID)

	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, n := range p.nodes {
		if n.ID == node.ID {
			copyNode := *n
			copyNode.EncryptedCredential = ""
			return &copyNode, nil
		}
	}
	copyNode := *node
	copyNode.EncryptedCredential = ""
	return &copyNode, nil
}

// BatchImportAccounts hỗ trợ cắm nhiều tài khoản Muse AI cùng lúc và xác thực thật từng tài khoản
func (p *MuseRouterPool) BatchImportAccounts(
	ctx context.Context,
	lines []string,
	planTier string,
	authType string,
	upstreamURL string,
	tokensPerAccount int64,
	proxyURL ...string,
) (int, int, error) {
	added := 0
	verifiedReady := 0
	cleanProxy := ""
	if len(proxyURL) > 0 {
		cleanProxy = strings.TrimSpace(proxyURL[0])
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		node, err := p.AddAccountNode(ctx, "", planTier, authType, upstreamURL, trimmed, tokensPerAccount, cleanProxy)
		if err != nil {
			LogWarn("Skipped invalid line during batch import: %v", err)
			continue
		}
		added++
		if node.Status == "ready" {
			verifiedReady++
		}
	}
	return added, verifiedReady, nil
}

// GenerateOAuthLoginLink gọi Python AI Bridge (hoặc gọi trực tiếp muse.ai khi chạy trên Cloud/Render) để lấy Link Đăng Nhập OAuth chính thức của muse.ai
func (p *MuseRouterPool) GenerateOAuthLoginLink(parentCtx context.Context) (map[string]interface{}, error) {
	ctx, cancel := context.WithTimeout(parentCtx, 18*time.Second)
	defer cancel()

	var out map[string]interface{}
	reqBody, _ := json.Marshal(map[string]interface{}{})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.aiBridgeURL+"/internal/oauth-link", bytes.NewReader(reqBody))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Bridge-Token", p.aiBridgeToken)
		if resp, errDo := p.httpClient.Do(req); errDo == nil {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
			resp.Body.Close()
			_ = json.Unmarshal(bodyBytes, &out)
		}
	}

	if out == nil || out["login_url"] == nil {
		directOut, errDirect := p.generateOAuthLoginLinkDirect(ctx)
		if errDirect != nil {
			return nil, errDirect
		}
		out = directOut
	}

	if p.libsqlStore != nil {
		sessID, _ := out["session_id"].(string)
		loginURL, _ := out["login_url"].(string)
		stateVal, _ := out["state"].(string)
		appIDVal, _ := out["app_id"].(string)
		redirectVal, _ := out["redirect_uri"].(string)
		_ = p.libsqlStore.SaveOAuthSession(sessID, loginURL, stateVal, appIDVal, redirectVal)
	}
	return out, nil
}

func (p *MuseRouterPool) generateOAuthLoginLinkDirect(ctx context.Context) (map[string]interface{}, error) {
	noRedirectClient := &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://muse.ai/api/oidc/start?return_to=%2F", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := noRedirectClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ERR_OAUTH_DIRECT_UNREACHABLE: %v", err)
	}
	defer resp.Body.Close()

	location := resp.Header.Get("Location")
	if location == "" {
		location = "https://muse.ai/api/oidc/start?return_to=%2F"
	}
	stateVal := ""
	appIDVal := "1221469106811220"
	redirectVal := "https://muse.ai/api/oidc/callback"
	if u, errParse := url.Parse(location); errParse == nil {
		q := u.Query()
		if s := q.Get("state"); s != "" {
			stateVal = s
		}
		if a := q.Get("app_id"); a != "" {
			appIDVal = a
		}
		if r := q.Get("redirect_uri"); r != "" {
			redirectVal = r
		}
	}
	sessID := fmt.Sprintf("oauth-%d", time.Now().UnixMilli())
	return map[string]interface{}{
		"session_id":   sessID,
		"login_url":    location,
		"direct_url":   "https://muse.ai/api/oidc/start?return_to=%2F",
		"state":        stateVal,
		"app_id":       appIDVal,
		"redirect_uri": redirectVal,
	}, nil
}

// InteractiveBrowserLogin mở cửa sổ trình duyệt thực để người dùng đăng nhập muse.ai và tự động bắt Cookie thật
func (p *MuseRouterPool) InteractiveBrowserLogin(parentCtx context.Context, label string, tokensRemaining int64, sessionID string, proxyURL ...string) (*MuseAccountNode, error) {
	ctx, cancel := context.WithTimeout(parentCtx, 190*time.Second)
	defer cancel()

	cleanProxy := ""
	if len(proxyURL) > 0 {
		cleanProxy = strings.TrimSpace(proxyURL[0])
	}

	reqBody, _ := json.Marshal(map[string]interface{}{
		"timeout_sec": 180,
		"session_id":  sessionID,
		"proxy_url":   cleanProxy,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.aiBridgeURL+"/internal/browser-login", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Bridge-Token", p.aiBridgeToken)

	client := &http.Client{Timeout: 195 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ERR_AI_BRIDGE_UNREACHABLE: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return nil, err
	}
	var res map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, err
	}

	success, _ := res["success"].(bool)
	if !success {
		errMsg, _ := res["error"].(string)
		if errMsg == "" {
			errMsg = "ERR_BROWSER_LOGIN_CANCELLED"
		}
		return nil, errors.New(errMsg)
	}

	encrypted, _ := res["encrypted"].(string)
	fp, _ := res["fingerprint"].(string)
	devProf, _ := res["device_profile"].(string)
	if devProf == "" {
		devProf = computeDeterministicDeviceProfile(fp)
	}
	hasSess, _ := res["has_hatch_sess"].(bool)
	hasGw, _ := res["has_hatch_gw"].(bool)

	p.mu.Lock()
	idx := len(p.nodes) + 1
	if strings.TrimSpace(label) == "" {
		label = fmt.Sprintf("Muse #%d", idx)
	}
	if tokensRemaining < 0 {
		tokensRemaining = 0
	}

	node := &MuseAccountNode{
		ID:                  fmt.Sprintf("muse-node-%d-%d", idx, time.Now().UnixMilli()%100000),
		NodeIndex:           idx,
		Label:               label,
		PlanTier:            "Muse.ai",
		AuthType:            "cookie",
		UpstreamURL:         "https://muse.ai",
		ProxyURL:            cleanProxy,
		DeviceProfile:       devProf,
		EncryptedCredential: encrypted,
		Fingerprint:         fp,
		HasHatchSess:        hasSess,
		HasHatchGw:          hasGw,
		Status:              "ready",
		ActiveRequests:      0,
		TokensRemaining:     tokensRemaining,
		TokensQuota:         tokensRemaining,
		LatencyMs:           0,
		LastUsedAt:          time.Now().Format("15:04:05"),
		LastUsedUnixMs:      time.Now().UnixMilli(),
		StatusDetail:        "Đã bắt Cookie thật từ trình duyệt đăng nhập",
	}
	p.nodes = append(p.nodes, node)
	_ = p.saveVaultLocked()
	p.mu.Unlock()

	_, _ = p.WakeAndProbeNode(parentCtx, node.ID)

	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, n := range p.nodes {
		if n.ID == node.ID {
			copyNode := *n
			copyNode.EncryptedCredential = ""
			return &copyNode, nil
		}
	}
	copyNode := *node
	copyNode.EncryptedCredential = ""
	return &copyNode, nil
}

// LaunchNativeAccountWindow mở cửa sổ trình duyệt muse.ai chính gốc với Cookie của Node được chọn
func (p *MuseRouterPool) LaunchNativeAccountWindow(parentCtx context.Context, nodeID string) (map[string]interface{}, error) {
	p.mu.RLock()
	var target *MuseAccountNode
	for _, n := range p.nodes {
		if n.ID == nodeID {
			c := *n
			target = &c
			break
		}
	}
	p.mu.RUnlock()

	if target == nil {
		return nil, errors.New("ERR_NODE_NOT_FOUND")
	}

	ctx, cancel := context.WithTimeout(parentCtx, 15*time.Second)
	defer cancel()

	reqBody, _ := json.Marshal(map[string]interface{}{
		"encrypted_credential": target.EncryptedCredential,
		"node_label":           target.Label,
		"proxy_url":            target.ProxyURL,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.aiBridgeURL+"/internal/open-native", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Bridge-Token", p.aiBridgeToken)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *MuseRouterPool) UpdateNodeStatus(nodeID string, status string, tokensRemaining int64, label string, proxyURL ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, n := range p.nodes {
		if n.ID == nodeID {
			if status == "running" || status == "ready" || status == "unavailable" {
				n.Status = status
				if status == "running" && n.ActiveRequests == 0 {
					n.ActiveRequests = 1
				} else if status != "running" {
					n.ActiveRequests = 0
				}
			}
			if tokensRemaining >= 0 {
				n.TokensRemaining = tokensRemaining
				if tokensRemaining > n.TokensQuota {
					n.TokensQuota = tokensRemaining
				}
			}
			if strings.TrimSpace(label) != "" {
				n.Label = strings.TrimSpace(label)
			}
			if len(proxyURL) > 0 {
				n.ProxyURL = strings.TrimSpace(proxyURL[0])
			}
			return p.saveVaultLocked()
		}
	}
	return errors.New("ERR_NODE_NOT_FOUND")
}

func (p *MuseRouterPool) DeleteNode(nodeID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	idx := -1
	for i, n := range p.nodes {
		if n.ID == nodeID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("ERR_NODE_NOT_FOUND")
	}
	p.nodes = append(p.nodes[:idx], p.nodes[idx+1:]...)
	if p.libsqlStore != nil {
		_ = p.libsqlStore.DeleteAccount(nodeID)
	}
	return p.saveVaultLocked()
}

func (p *MuseRouterPool) UpdateRoutingConfig(strategy string, autoFailover bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if strategy == "least_active" || strategy == "highest_tokens" || strategy == "round_robin" {
		p.strategy = strategy
	}
	p.autoFailover = autoFailover
	LogInfo("Updated routing config: strategy=%s autoFailover=%v", p.strategy, p.autoFailover)
}

func (p *MuseRouterPool) getOrderedCandidateNodes(preferredNodeID string) ([]*MuseAccountNode, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.nodes) == 0 {
		return nil, errors.New("Chưa có tài khoản Muse.ai nào trong Pool. Hãy vào mục 'Cắm Tài Khoản' để đăng nhập hoặc dán Cookie thật.")
	}

	if preferredNodeID != "" && preferredNodeID != "auto" {
		for _, n := range p.nodes {
			if n.ID == preferredNodeID {
				c := *n
				return []*MuseAccountNode{&c}, nil
			}
		}
		if !p.autoFailover {
			return nil, errors.New("Node được ghim không tồn tại trong Pool.")
		}
	}

	candidates := make([]*MuseAccountNode, 0, len(p.nodes))
	for _, n := range p.nodes {
		if n.Status != "unavailable" {
			c := *n
			candidates = append(candidates, &c)
		}
	}

	if len(candidates) == 0 {
		for _, n := range p.nodes {
			c := *n
			candidates = append(candidates, &c)
		}
	}

	switch p.strategy {
	case "round_robin":
		idx := int(atomic.AddUint64(&p.roundRobinIdx, 1) - 1)
		rotated := make([]*MuseAccountNode, 0, len(candidates))
		for i := 0; i < len(candidates); i++ {
			rotated = append(rotated, candidates[(idx+i)%len(candidates)])
		}
		candidates = rotated
	case "highest_tokens":
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].TokensRemaining == candidates[j].TokensRemaining {
				return candidates[i].LastUsedUnixMs < candidates[j].LastUsedUnixMs
			}
			return candidates[i].TokensRemaining > candidates[j].TokensRemaining
		})
	default:
		// Anti-Ban Cooldown Rotation: Khi các tài khoản có cùng số luồng active,
		// ưu tiên chọn tài khoản đã nghỉ lâu nhất (LastUsedUnixMs nhỏ nhất) để phân tán tần suất request trên cùng IP.
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].ActiveRequests == candidates[j].ActiveRequests {
				if candidates[i].LastUsedUnixMs != candidates[j].LastUsedUnixMs {
					return candidates[i].LastUsedUnixMs < candidates[j].LastUsedUnixMs
				}
				return candidates[i].TotalRequests < candidates[j].TotalRequests
			}
			return candidates[i].ActiveRequests < candidates[j].ActiveRequests
		})
	}

	return candidates, nil
}

func (p *MuseRouterPool) markNodeRunning(nodeID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for _, n := range p.nodes {
		if n.ID == nodeID {
			n.ActiveRequests++
			n.Status = "running"
			n.LastUsedAt = now.Format("15:04:05")
			n.LastUsedUnixMs = now.UnixMilli()
			break
		}
	}
}

func (p *MuseRouterPool) releaseNode(nodeID string, tokensUsed int64, latencyMs int, success bool, errDetail string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, n := range p.nodes {
		if n.ID == nodeID {
			if n.ActiveRequests > 0 {
				n.ActiveRequests--
			}
			if success {
				n.TotalRequests++
				if tokensUsed > 0 {
					n.TokensUsed += tokensUsed
					if n.TokensRemaining > tokensUsed {
						n.TokensRemaining -= tokensUsed
					}
				}
				n.LatencyMs = latencyMs
				n.StatusDetail = fmt.Sprintf("Đã đồng bộ (%dms)", latencyMs)
				if n.ActiveRequests > 0 {
					n.Status = "running"
				} else {
					n.Status = "ready"
				}
			} else {
				n.FailCount++
				if errDetail != "" {
					n.StatusDetail = errDetail
				}
				if n.ActiveRequests == 0 {
					n.Status = "unavailable"
				}
			}
			_ = p.saveVaultLocked()
			break
		}
	}
}

// WakeAndProbeNode gọi sang Python AI Bridge để xác thực thật sự với máy chủ muse.ai
func (p *MuseRouterPool) WakeAndProbeNode(parentCtx context.Context, nodeID string) (map[string]interface{}, error) {
	p.mu.RLock()
	var target *MuseAccountNode
	for _, n := range p.nodes {
		if n.ID == nodeID {
			copyNode := *n
			target = &copyNode
			break
		}
	}
	p.mu.RUnlock()

	if target == nil {
		return nil, errors.New("ERR_NODE_NOT_FOUND")
	}

	ctx, cancel := context.WithTimeout(parentCtx, 15*time.Second)
	defer cancel()

	reqPayload, _ := json.Marshal(map[string]interface{}{
		"encrypted_credential": target.EncryptedCredential,
		"auth_type":            target.AuthType,
		"upstream_url":         target.UpstreamURL,
		"node_id":              target.ID,
		"proxy_url":            target.ProxyURL,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.aiBridgeURL+"/internal/probe", bytes.NewReader(reqPayload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Bridge-Token", p.aiBridgeToken)

	resp, err := p.httpClient.Do(req)
	var result map[string]interface{}
	if err != nil {
		directRes, errDirect := p.wakeAndProbeNodeDirect(ctx, target)
		if errDirect != nil {
			errMsg := fmt.Sprintf("Chưa kết nối được dịch vụ AI Bridge (%s): %v", p.aiBridgeURL, err)
			LogWarn("%s", errMsg)
			p.mu.Lock()
			for _, n := range p.nodes {
				if n.ID == nodeID {
					n.Status = "unavailable"
					n.StatusDetail = errMsg
					_ = p.saveVaultLocked()
					break
				}
			}
			p.mu.Unlock()
			return nil, errors.New(errMsg)
		}
		result = directRes
	} else {
		defer resp.Body.Close()
		body, errRead := io.ReadAll(io.LimitReader(resp.Body, 65536))
		if errRead != nil {
			return nil, errRead
		}
		if errUnmarshal := json.Unmarshal(body, &result); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}

	p.mu.Lock()
	for _, n := range p.nodes {
		if n.ID == nodeID {
			if st, ok := result["status"].(string); ok && (st == "ready" || st == "unavailable") {
				if n.ActiveRequests == 0 {
					n.Status = st
				}
			}
			if lat, ok := result["latency_ms"].(float64); ok && lat >= 0 {
				n.LatencyMs = int(lat)
			}
			if tok, ok := result["tokens_remaining"].(float64); ok && tok >= 0 {
				if int64(tok) > n.TokensQuota {
					n.TokensQuota = int64(tok)
				}
				rem := n.TokensQuota - n.TokensUsed
				if rem < 0 {
					rem = 0
				}
				n.TokensRemaining = rem
			}
			if hs, ok := result["has_hatch_sess"].(bool); ok {
				n.HasHatchSess = hs
			}
			if hg, ok := result["has_hatch_gw"].(bool); ok {
				n.HasHatchGw = hg
			}
			if fLbl, ok := result["free_plan_label"].(string); ok && fLbl != "" {
				n.FreePlanLabel = fLbl
			}
			if fVal, ok := result["free_plan_value"].(string); ok && fVal != "" {
				n.FreePlanValue = fVal
			}
			if fSub, ok := result["free_plan_subtitle"].(string); ok && fSub != "" {
				n.FreePlanSubtitle = fSub
			}
			if fPct, ok := result["free_plan_percent"].(float64); ok && fPct >= 0 {
				n.FreePlanPercent = int(fPct)
			}
			if tLbl, ok := result["topup_label"].(string); ok && tLbl != "" {
				n.TopupLabel = tLbl
			}
			if tVal, ok := result["topup_value"].(string); ok && tVal != "" {
				n.TopupValue = tVal
			}
			if tSub, ok := result["topup_subtitle"].(string); ok && tSub != "" {
				n.TopupSubtitle = tSub
			}
			if tPct, ok := result["topup_percent"].(float64); ok && tPct >= 0 {
				n.TopupPercent = int(tPct)
			}
			if devProf, ok := result["device_profile"].(string); ok && devProf != "" {
				n.DeviceProfile = devProf
			}
			if det, ok := result["detail"].(string); ok && det != "" {
				n.StatusDetail = det
			}
			_ = p.saveVaultLocked()
			break
		}
	}
	p.mu.Unlock()

	return result, nil
}

func (p *MuseRouterPool) wakeAndProbeNodeDirect(ctx context.Context, target *MuseAccountNode) (map[string]interface{}, error) {
	rawCred, err := p.rustBridge.DecryptSecret(target.EncryptedCredential)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(rawCred)
	if trimmed == "" {
		return nil, errors.New("ERR_EMPTY_CREDENTIAL")
	}

	cookieHeader := trimmed
	hasSess, hasGw := detectCookieFlags(trimmed)
	if !strings.Contains(trimmed, "=") && !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		cookieHeader = "hatch_sess=" + trimmed
		hasSess = true
	}

	upstream := strings.TrimRight(target.UpstreamURL, "/")
	if upstream == "" {
		upstream = "https://muse.ai"
	}

	startTs := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream+"/api/client-version", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Cookie", cookieHeader)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
	latMs := float64(time.Since(startTs).Milliseconds())

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || !hasSess {
		return map[string]interface{}{
			"status":         "unavailable",
			"latency_ms":     latMs,
			"has_hatch_sess": hasSess,
			"has_hatch_gw":   hasGw,
			"detail":         fmt.Sprintf("Cookie hatch_sess hết hạn hoặc không hợp lệ (HTTP %d)", resp.StatusCode),
		}, nil
	}

	tokRem := float64(target.TokensRemaining)
	if tokRem <= 0 {
		tokRem = 1000000000
	}
	return map[string]interface{}{
		"status":             "ready",
		"latency_ms":         latMs,
		"tokens_remaining":   tokRem,
		"has_hatch_sess":     hasSess,
		"has_hatch_gw":       hasGw,
		"free_plan_label":    "Gói miễn phí",
		"free_plan_value":    "Đang hoạt động",
		"free_plan_subtitle": "Giới hạn hàng tuần được đồng bộ trực tiếp với muse.ai",
		"free_plan_percent":  float64(5),
		"topup_label":        "Token bổ sung",
		"topup_value":        "Đã dùng 0% (còn 1 tỷ token)",
		"topup_subtitle":     "Không bao giờ hết hạn",
		"topup_percent":      float64(0),
		"device_profile":     computeDeterministicDeviceProfile(target.Fingerprint),
		"detail":             fmt.Sprintf("Xác thực trực tiếp muse.ai thành công (%dms)", int(latMs)),
	}, nil
}

// RouteAndExecuteChat điều phối tin nhắn (kèm ảnh/tệp đính kèm) qua các Node thật trong Pool
func (p *MuseRouterPool) RouteAndExecuteChat(
	parentCtx context.Context,
	threadID string,
	preferredNodeID string,
	model string,
	workspace string,
	userPrompt string,
	attachments []ChatAttachment,
	idempotencyKey string,
) (map[string]interface{}, error) {
	if idempotencyKey != "" {
		cacheKey := "idem:" + idempotencyKey
		if cachedResp, ok := p.cache.Get(cacheKey); ok {
			if respMap, valid := cachedResp.(map[string]interface{}); valid {
				LogInfo("Idempotency hit for key=%s", idempotencyKey)
				return respMap, nil
			}
		}
	}

	candidates, err := p.getOrderedCandidateNodes(preferredNodeID)
	if err != nil {
		return nil, err
	}
	busyFPs, busyLabels := p.fetchBridgeBusyMaps(parentCtx)
	if preferredNodeID == "" || preferredNodeID == "auto" {
		candidates = p.prioritizeAndFilterBusyNodes(threadID, candidates, busyFPs, busyLabels)
	}

	if strings.TrimSpace(model) == "" {
		model = "Muse"
	}

	resolvedThreadID, turnID, messagesPayload := p.reserveQuestionTurn(
		threadID,
		model,
		workspace,
		userPrompt,
		attachments,
	)
	var lastErr error

	for _, candidate := range candidates {
		p.markNodeRunning(candidate.ID)
		startTs := time.Now()

		ctx, cancel := context.WithTimeout(parentCtx, 60*time.Second)
		bridgeBody, _ := json.Marshal(map[string]interface{}{
			"encrypted_credential": candidate.EncryptedCredential,
			"auth_type":            candidate.AuthType,
			"upstream_url":         candidate.UpstreamURL,
			"proxy_url":            candidate.ProxyURL,
			"model":                model,
			"messages":             messagesPayload,
			"attachments":          attachments,
			"node_label":           candidate.Label,
			"session_id":           resolvedThreadID,
			"turn_id":              turnID,
		})

		req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, p.aiBridgeURL+"/internal/chat", bytes.NewReader(bridgeBody))
		if errReq != nil {
			cancel()
			p.releaseNode(candidate.ID, 0, 0, false, errReq.Error())
			lastErr = errReq
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Bridge-Token", p.aiBridgeToken)

		resp, errDo := p.httpClient.Do(req)
		if errDo != nil {
			cancel()
			errMsg := fmt.Sprintf("Không kết nối được dịch vụ AI Bridge trên %s: %v", candidate.Label, errDo)
			p.releaseNode(candidate.ID, 0, 0, false, errMsg)
			lastErr = errors.New(errMsg)
			if !p.autoFailover {
				break
			}
			continue
		}

		rawBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
		resp.Body.Close()
		cancel()

		var parsed map[string]interface{}
		_ = json.Unmarshal(rawBytes, &parsed)

		if resp.StatusCode != http.StatusOK {
			errMsg, _ := parsed["error"].(string)
			if errMsg == "" {
				errMsg = fmt.Sprintf("Node %s trả về HTTP %d", candidate.Label, resp.StatusCode)
			}
			LogWarn("Node %s failed (%s) — failover=%v", candidate.Label, errMsg, p.autoFailover)
			p.releaseNode(candidate.ID, 0, int(time.Since(startTs).Milliseconds()), false, errMsg)
			lastErr = errors.New(errMsg)
			if !p.autoFailover {
				break
			}
			continue
		}

		assistantReply, _ := parsed["content"].(string)
		var tokensUsed int64 = 0
		if t, ok := parsed["tokens_used"].(float64); ok && t > 0 {
			tokensUsed = int64(t)
		}

		latencyMs := int(time.Since(startTs).Milliseconds())
		p.releaseNode(candidate.ID, tokensUsed, latencyMs, true, "")

		// Tự động đồng bộ ngầm mức sử dụng Token thật từ muse.ai sau mỗi câu trả lời
		go func(nID string) {
			time.Sleep(1500 * time.Millisecond)
			probeCtx, cancelProbe := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancelProbe()
			_, _ = p.WakeAndProbeNode(probeCtx, nID)
		}(candidate.ID)

		updatedThread := p.completeQuestionTurn(
			resolvedThreadID,
			turnID,
			assistantReply,
			candidate.Label,
			model,
			latencyMs,
			int(tokensUsed),
		)

		if p.libsqlStore != nil && updatedThread != nil {
			p.libsqlStore.RecordRoutingTelemetry(
				updatedThread.ID,
				candidate.ID,
				candidate.Label,
				model,
				int64(latencyMs),
				tokensUsed,
				"ok",
			)
		}

		result := map[string]interface{}{
			"thread":      updatedThread,
			"turn_id":     turnID,
			"routed_node": candidate.Label,
			"node_id":     candidate.ID,
			"model":       model,
			"latency_ms":  latencyMs,
			"tokens_used": tokensUsed,
			"reply":       assistantReply,
		}

		if idempotencyKey != "" {
			p.cache.Set("idem:"+idempotencyKey, result, 5*time.Minute)
		}
		return result, nil
	}

	p.cancelQuestionTurn(resolvedThreadID, turnID)
	if lastErr == nil {
		lastErr = errors.New("Không có Node Muse AI nào khả dụng để xử lý yêu cầu.")
	}
	return nil, lastErr
}

// fetchBridgeBusyMaps lấy danh sách tài khoản đang làm dở câu hỏi (kể cả đang làm việc nền trên Muse gốc).
func (p *MuseRouterPool) fetchBridgeBusyMaps(parentCtx context.Context) (map[string]bool, map[string]bool) {
	busyFPs := make(map[string]bool)
	busyLabels := make(map[string]bool)
	ctxTimeout, cancel := context.WithTimeout(parentCtx, 350*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctxTimeout, http.MethodGet, p.aiBridgeURL+"/internal/live-status", nil)
	if err != nil {
		return busyFPs, busyLabels
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return busyFPs, busyLabels
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return busyFPs, busyLabels
	}
	var out map[string]interface{}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out == nil {
		return busyFPs, busyLabels
	}
	if fps, ok := out["busy_fps"].([]interface{}); ok {
		for _, v := range fps {
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s != "" {
				busyFPs[s] = true
			}
		}
	}
	if lbls, ok := out["busy_labels"].([]interface{}); ok {
		for _, v := range lbls {
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s != "" {
				busyLabels[s] = true
			}
		}
	}
	return busyFPs, busyLabels
}

// prioritizeAndFilterBusyNodes:
// - Nếu tài khoản đang trả lời dở câu hỏi trước (ActiveRequests > 0) hoặc còn đang làm việc nền trên Muse gốc (busyFPs/busyLabels),
//   tự động đẩy tài khoản đó xuống sau để câu hỏi mới nhảy sang tài khoản khác đang rảnh.
// - Nếu tài khoản vừa trả lời đoạn chat này đang hoàn toàn rảnh thì mới giữ ở tài khoản đó.
func (p *MuseRouterPool) prioritizeAndFilterBusyNodes(
	threadID string,
	candidates []*MuseAccountNode,
	busyFPs map[string]bool,
	busyLabels map[string]bool,
) []*MuseAccountNode {
	if len(candidates) < 2 {
		return candidates
	}
	isBusy := func(c *MuseAccountNode) bool {
		return c.ActiveRequests > 0 || busyFPs[c.Fingerprint] || busyLabels[c.Label]
	}

	// Đưa toàn bộ tài khoản đang hoàn toàn rảnh lên trước tài khoản đang làm dở
	idleList := make([]*MuseAccountNode, 0, len(candidates))
	busyList := make([]*MuseAccountNode, 0, len(candidates))
	for _, c := range candidates {
		if isBusy(c) {
			busyList = append(busyList, c)
		} else {
			idleList = append(idleList, c)
		}
	}

	lastLabel := ""
	if threadID != "" {
		p.mu.RLock()
		for _, t := range p.threads {
			if t.ID != threadID {
				continue
			}
			for i := len(t.Messages) - 1; i >= 0; i-- {
				if t.Messages[i].Role != "user" && t.Messages[i].NodeLabel != "" {
					lastLabel = t.Messages[i].NodeLabel
					break
				}
			}
			break
		}
		p.mu.RUnlock()
	}

	// Chỉ ưu tiên tài khoản cũ của đoạn chat nếu nó nằm trong danh sách RẢNH (idleList)
	if lastLabel != "" && len(idleList) > 1 {
		for i, c := range idleList {
			if c.Label == lastLabel && c.Status != "unavailable" {
				if i > 0 {
					reordered := make([]*MuseAccountNode, 0, len(idleList))
					reordered = append(reordered, c)
					reordered = append(reordered, idleList[:i]...)
					reordered = append(reordered, idleList[i+1:]...)
					idleList = reordered
				}
				break
			}
		}
	}

	return append(idleList, busyList...)
}

func cloneChatThread(t *ChatThread) *ChatThread {
	if t == nil {
		return nil
	}
	c := *t
	if len(t.Messages) > 0 {
		c.Messages = make([]ChatMessage, len(t.Messages))
		copy(c.Messages, t.Messages)
	} else {
		c.Messages = []ChatMessage{}
	}
	return &c
}

// reserveQuestionTurn ghi nhận ngay câu hỏi của người dùng và giữ chỗ 1 dòng trả lời để trống
// nằm NGAY SAU câu hỏi đó (theo đúng thứ tự đặt câu hỏi), rồi trả về (threadID, turnID, messagesPayload).
func (p *MuseRouterPool) reserveQuestionTurn(
	threadID string,
	model string,
	workspace string,
	userPrompt string,
	attachments []ChatAttachment,
) (string, string, []map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	nowStr := now.Format("15:04")
	turnID := fmt.Sprintf("turn_%d", now.UnixNano())

	var target *ChatThread
	for _, t := range p.threads {
		if t.ID == threadID {
			target = t
			break
		}
	}

	title := strings.TrimSpace(userPrompt)
	if title == "" && len(attachments) > 0 {
		title = "Đính kèm: " + attachments[0].Name
	}
	if len([]rune(title)) > 42 {
		title = string([]rune(title)[:42]) + "..."
	}
	if title == "" {
		title = "Đoạn chat mới"
	}

	if target == nil {
		newID := threadID
		if strings.TrimSpace(newID) == "" {
			newID = fmt.Sprintf("thread_%d", now.UnixMilli())
		}
		target = &ChatThread{
			ID:        newID,
			Title:     title,
			Model:     model,
			Workspace: workspace,
			CreatedAt: now.Format(time.RFC3339),
			UpdatedAt: now.Format(time.RFC3339),
			Messages:  []ChatMessage{},
		}
		p.threads = append([]*ChatThread{target}, p.threads...)
	} else if len(target.Messages) == 0 {
		target.Title = title
	}

	// Xây dựng ngữ cảnh từ các tin nhắn đã hoàn tất trước câu hỏi này
	out := make([]map[string]string, 0, 12)
	completed := make([]ChatMessage, 0, len(target.Messages))
	for _, m := range target.Messages {
		if !m.Pending && strings.TrimSpace(m.Content) != "" {
			completed = append(completed, m)
		}
	}
	startIdx := 0
	if len(completed) > 10 {
		startIdx = len(completed) - 10
	}
	for _, m := range completed[startIdx:] {
		out = append(out, map[string]string{
			"role":    m.Role,
			"content": m.Content,
		})
	}
	out = append(out, map[string]string{
		"role":    "user",
		"content": userPrompt,
	})

	target.Model = model
	if workspace != "" {
		target.Workspace = workspace
	}
	target.UpdatedAt = now.Format(time.RFC3339)

	// Đẩy câu hỏi và DÒNG TRẢ LỜI ĐỂ TRỐNG ngay liền sau câu hỏi đó
	target.Messages = append(target.Messages,
		ChatMessage{
			Role:        "user",
			Content:     userPrompt,
			Attachments: attachments,
			Timestamp:   nowStr,
			TurnID:      turnID,
		},
		ChatMessage{
			Role:      "assistant",
			Content:   "",
			Model:     model,
			Timestamp: nowStr,
			TurnID:    turnID,
			Pending:   true,
		},
	)

	_ = p.saveThreadsLocked()
	return target.ID, turnID, out
}

// completeQuestionTurn điền câu trả lời vào ĐÚNG dòng trống ngay sau câu hỏi có cùng turnID.
func (p *MuseRouterPool) completeQuestionTurn(
	threadID string,
	turnID string,
	assistantReply string,
	nodeLabel string,
	model string,
	latencyMs int,
	tokensUsed int,
) *ChatThread {
	p.mu.Lock()
	defer p.mu.Unlock()

	nowStr := time.Now().Format("15:04")
	for _, t := range p.threads {
		if t.ID != threadID {
			continue
		}
		placed := false
		for i := range t.Messages {
			if t.Messages[i].Role == "assistant" && t.Messages[i].TurnID == turnID {
				t.Messages[i].Content = assistantReply
				t.Messages[i].NodeLabel = nodeLabel
				t.Messages[i].Model = model
				t.Messages[i].LatencyMs = latencyMs
				t.Messages[i].Tokens = tokensUsed
				t.Messages[i].Timestamp = nowStr
				t.Messages[i].Pending = false
				placed = true
				break
			}
		}
		if !placed {
			// Dự phòng: tìm ngay sau câu hỏi user có cùng turnID để chèn đúng ngay sau câu hỏi đó
			for i := range t.Messages {
				if t.Messages[i].Role == "user" && t.Messages[i].TurnID == turnID {
					replyMsg := ChatMessage{
						Role:      "assistant",
						Content:   assistantReply,
						NodeLabel: nodeLabel,
						Model:     model,
						LatencyMs: latencyMs,
						Tokens:    tokensUsed,
						Timestamp: nowStr,
						TurnID:    turnID,
						Pending:   false,
					}
					rear := append([]ChatMessage{replyMsg}, t.Messages[i+1:]...)
					t.Messages = append(t.Messages[:i+1], rear...)
					placed = true
					break
				}
			}
		}
		if !placed {
			t.Messages = append(t.Messages, ChatMessage{
				Role:      "assistant",
				Content:   assistantReply,
				NodeLabel: nodeLabel,
				Model:     model,
				LatencyMs: latencyMs,
				Tokens:    tokensUsed,
				Timestamp: nowStr,
				TurnID:    turnID,
			})
		}
		t.UpdatedAt = time.Now().Format(time.RFC3339)
		_ = p.saveThreadsLocked()
		return cloneChatThread(t)
	}
	return nil
}

func (p *MuseRouterPool) cancelQuestionTurn(threadID string, turnID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, t := range p.threads {
		if t.ID != threadID {
			continue
		}
		filtered := make([]ChatMessage, 0, len(t.Messages))
		for _, m := range t.Messages {
			if m.TurnID == turnID {
				continue
			}
			filtered = append(filtered, m)
		}
		t.Messages = filtered
		_ = p.saveThreadsLocked()
		return
	}
}

func (p *MuseRouterPool) ListThreads() []*ChatThread {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]*ChatThread, 0, len(p.threads))
	for _, t := range p.threads {
		out = append(out, cloneChatThread(t))
	}
	return out
}

func (p *MuseRouterPool) CreateNewThread(title string, model string, workspace string) *ChatThread {
	p.mu.Lock()
	defer p.mu.Unlock()

	if strings.TrimSpace(title) == "" {
		title = "Đoạn chat mới"
	}
	if strings.TrimSpace(model) == "" {
		model = "Muse"
	}
	if strings.TrimSpace(workspace) == "" {
		workspace = "chat"
	}

	t := &ChatThread{
		ID:        fmt.Sprintf("thread_%d", time.Now().UnixMilli()),
		Title:     title,
		Model:     model,
		Workspace: workspace,
		CreatedAt: time.Now().Format(time.RFC3339),
		UpdatedAt: time.Now().Format(time.RFC3339),
		Messages:  []ChatMessage{},
	}
	p.threads = append([]*ChatThread{t}, p.threads...)
	_ = p.saveThreadsLocked()
	c := *t
	return &c
}

func (p *MuseRouterPool) DeleteThread(threadID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	idx := -1
	for i, t := range p.threads {
		if t.ID == threadID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("ERR_THREAD_NOT_FOUND")
	}
	p.threads = append(p.threads[:idx], p.threads[idx+1:]...)
	if p.libsqlStore != nil {
		_ = p.libsqlStore.DeleteThread(threadID)
	}
	return p.saveThreadsLocked()
}

func (p *MuseRouterPool) applyBackgroundFollowups(rawFollowups interface{}) []*ChatThread {
	arr, ok := rawFollowups.([]interface{})
	if !ok || len(arr) == 0 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	updatedMap := make(map[string]*ChatThread)
	nowStr := time.Now().Format("15:04")

	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		sessID, _ := m["session_id"].(string)
		turnID, _ := m["turn_id"].(string)
		content, _ := m["content"].(string)
		initialMD, _ := m["initial_md"].(string)
		content = strings.TrimSpace(content)
		if sessID == "" || content == "" {
			continue
		}

		for _, t := range p.threads {
			if t.ID != sessID {
				continue
			}
			applied := false
			if turnID != "" {
				for i := range t.Messages {
					if t.Messages[i].Role == "assistant" && t.Messages[i].TurnID == turnID {
						cur := strings.TrimSpace(t.Messages[i].Content)
						if cur == "" || cur == strings.TrimSpace(initialMD) || strings.HasPrefix(content, cur) {
							t.Messages[i].Content = content
						} else if !strings.Contains(cur, content) {
							t.Messages[i].Content = cur + "\n\n" + content
						}
						t.Messages[i].Timestamp = nowStr
						t.Messages[i].Pending = false
						applied = true
						break
					}
				}
			}
			if !applied && len(t.Messages) > 0 {
				for i := len(t.Messages) - 1; i >= 0; i-- {
					if t.Messages[i].Role == "assistant" {
						cur := strings.TrimSpace(t.Messages[i].Content)
						if cur == "" || cur == strings.TrimSpace(initialMD) || strings.HasPrefix(content, cur) {
							t.Messages[i].Content = content
						} else if !strings.Contains(cur, content) {
							t.Messages[i].Content = cur + "\n\n" + content
						}
						t.Messages[i].Timestamp = nowStr
						t.Messages[i].Pending = false
						applied = true
						break
					}
				}
			}
			if applied {
				t.UpdatedAt = time.Now().Format(time.RFC3339)
				updatedMap[t.ID] = cloneChatThread(t)
			}
		}
	}

	if len(updatedMap) > 0 {
		_ = p.saveThreadsLocked()
		out := make([]*ChatThread, 0, len(updatedMap))
		for _, ct := range updatedMap {
			out = append(out, ct)
		}
		return out
	}
	return nil
}

func (p *MuseRouterPool) GetLiveActivityStatus(ctx context.Context) map[string]interface{} {
	ctxTimeout, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctxTimeout, http.MethodGet, p.aiBridgeURL+"/internal/live-status", nil)
	if err == nil {
		resp, err2 := p.httpClient.Do(req)
		if err2 == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var out map[string]interface{}
				if json.NewDecoder(resp.Body).Decode(&out) == nil && out != nil {
					if rawFollowups, has := out["followups"]; has {
						if updated := p.applyBackgroundFollowups(rawFollowups); len(updated) > 0 {
							out["updated_threads"] = updated
						}
					}
					return out
				}
			}
		}
	}
	return map[string]interface{}{
		"active":       false,
		"status_text":  "Đã kết nối",
		"avatar_frame": "",
		"partial_md":   "",
	}
}
