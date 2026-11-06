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
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
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
	OwnerID   string        `json:"owner_id,omitempty"`
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
	DatabaseDriver    string             `json:"database_driver"`
	DatabaseURL       string             `json:"database_url"`
	Nodes             []*MuseAccountNode `json:"nodes"`
}

type MuseRouterPool struct {
	mu                sync.RWMutex
	nodes             []*MuseAccountNode
	threads           []*ChatThread
	strategy          string
	autoFailover      bool
	roundRobinIdx     uint64
	queuedCount       int64
	cachedMemAllocMB  uint64
	bridgeDownUntilMs int64
	vaultPath         string
	threadsPath       string
	mysqlStore        *MySQLStore
	rustBridge        *RustCoreBridge
	cache             *TTLCache
	jobQueue          chan func()
	aiBridgeURL       string
	aiBridgeToken     string
	httpClient        *http.Client
	cdpManager        *CDPBrowserManager
	shutdownCh        chan struct{}
}

type clientBrowserRelayContextKey struct{}
type skipBrowserRelayContextKey struct{}

func (p *MuseRouterPool) shouldTryPythonBridge() bool {
	u := strings.TrimSpace(p.aiBridgeURL)
	if u == "" || u == "http://127.0.0.1:1" {
		return false
	}
	if (os.Getenv("RENDER") != "" || os.Getenv("PORT") != "") && os.Getenv("MUSE_AI_BRIDGE_URL") == "" {
		return false
	}
	if u == "http://127.0.0.1:18089" && atomic.LoadInt64(&p.bridgeDownUntilMs) > time.Now().UnixMilli() {
		return false
	}
	return true
}

func (p *MuseRouterPool) markPythonBridgeDown() {
	if strings.TrimSpace(p.aiBridgeURL) == "http://127.0.0.1:18089" {
		atomic.StoreInt64(&p.bridgeDownUntilMs, time.Now().Add(20*time.Second).UnixMilli())
	}
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

	sqlStore, err := NewMySQLStore(dataDir, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize MySQL store: %w", err)
	}
	if sqlStore != nil {
		sqlStore.MigrateLegacyJSONIfNeeded(vaultPath, threadsPath, legacyThreadsPath)
	}

	pool := &MuseRouterPool{
		nodes:         make([]*MuseAccountNode, 0),
		threads:       make([]*ChatThread, 0),
		strategy:      "least_active",
		autoFailover:  true,
		vaultPath:     vaultPath,
		threadsPath:   threadsPath,
		mysqlStore:    sqlStore,
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

	var initMem runtime.MemStats
	runtime.ReadMemStats(&initMem)
	atomic.StoreUint64(&pool.cachedMemAllocMB, initMem.Alloc/(1024*1024))

	pool.cdpManager = NewCDPBrowserManager(dataDir, pool)
	for i := 0; i < MaxWorkerGoroutines; i++ {
		go pool.workerLoop()
	}
	go pool.memoryMonitorLoop()

	if err := pool.loadVault(); err != nil {
		LogWarn("Initializing clean account vault in MySQL (%v)", err)
	}
	if err := pool.loadThreads(); err != nil {
		LogWarn("Initializing clean chat session store in MySQL (%v)", err)
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
			atomic.StoreUint64(&p.cachedMemAllocMB, allocMB)
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

	ticker := time.NewTicker(180 * time.Second)
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
	if p.cdpManager != nil {
		p.cdpManager.Shutdown()
	}
	if p.mysqlStore != nil {
		_ = p.mysqlStore.Close()
	}
}

func (p *MuseRouterPool) GetMySQLStore() *MySQLStore {
	return p.mysqlStore
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
		if n.Status == "unavailable" && n.HasHatchSess && strings.Contains(n.StatusDetail, "AI Bridge") {
			n.Status = "ready"
			n.StatusDetail = "Đang hoạt động (Đồng bộ trực tiếp)"
		}
		if n.Status == "ready" || n.Status == "running" {
			if n.FreePlanLabel == "" {
				n.FreePlanLabel = "Gói miễn phí"
			}
			if n.FreePlanValue == "" {
				n.FreePlanValue = "Chưa lấy được dữ liệu từ Muse.ai"
			}
			if n.FreePlanSubtitle == "" {
				n.FreePlanSubtitle = "Giới hạn hàng tuần được đồng bộ trực tiếp với muse.ai"
			}
			if n.TopupLabel == "" {
				n.TopupLabel = "Token bổ sung"
			}
			if n.TopupValue == "" {
				n.TopupValue = "Chưa lấy được dữ liệu từ Muse.ai"
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

	if p.mysqlStore != nil {
		loaded, err := p.mysqlStore.LoadAccounts()
		if err == nil {
			normalizeAccountIndices(loaded)
			p.nodes = loaded
			LogInfo("Loaded %d Muse accounts directly from SQL40 (%s)", len(p.nodes), maskDatabaseURL(p.mysqlStore.dbURL))
			_ = p.syncBackupJSONLocked()
			return nil
		}
		LogWarn("MySQL LoadAccounts fallback check: %v", err)
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
	if p.mysqlStore != nil {
		_ = p.mysqlStore.SyncAllAccounts(p.nodes)
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
	if p.mysqlStore != nil {
		if err := p.mysqlStore.SyncAllAccounts(p.nodes); err != nil {
			LogError("MySQL SyncAllAccounts error: %v", err)
			return err
		}
	}
	return p.syncBackupJSONLocked()
}

func (p *MuseRouterPool) loadThreads() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.mysqlStore != nil {
		loaded, err := p.mysqlStore.LoadThreadsWithMessages()
		if err == nil {
			p.threads = loaded
			return nil
		}
		LogWarn("MySQL LoadThreadsWithMessages error: %v", err)
	}

	p.threads = []*ChatThread{}
	return nil
}

func (p *MuseRouterPool) saveThreadsLocked() error {
	if p.mysqlStore != nil {
		for _, t := range p.threads {
			if err := p.mysqlStore.SaveThreadWithMessages(t); err != nil {
				LogError("MySQL SaveThreadWithMessages error: %v", err)
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

func (p *MuseRouterPool) saveSingleThreadLocked(target *ChatThread) error {
	if target == nil {
		return nil
	}
	if p.mysqlStore != nil {
		if err := p.mysqlStore.SaveThreadWithMessages(target); err != nil {
			LogError("MySQL SaveThreadWithMessages error: %v", err)
			return err
		}
	}
	snap := make([]*ChatThread, 0, len(p.threads))
	for _, t := range p.threads {
		snap = append(snap, cloneChatThread(t))
	}
	tPath := p.threadsPath
	_ = p.SubmitAsyncJob(func() {
		if data, err := json.Marshal(snap); err == nil {
			_ = os.WriteFile(tPath, data, 0600)
		}
	})
	return nil
}

func (p *MuseRouterPool) GetSummary() RouterSummary {
	p.mu.RLock()
	needReload := len(p.nodes) == 0 && p.mysqlStore != nil
	p.mu.RUnlock()

	if needReload {
		_ = p.loadVault()
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	allocMB := atomic.LoadUint64(&p.cachedMemAllocMB)

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
			if copyNode.FreePlanLabel == "" {
				copyNode.FreePlanLabel = "Gói miễn phí"
			}
			if copyNode.FreePlanValue == "" {
				copyNode.FreePlanValue = "Chưa lấy được dữ liệu từ Muse.ai"
			}
			if copyNode.FreePlanSubtitle == "" {
				copyNode.FreePlanSubtitle = "Giới hạn hàng tuần được đồng bộ trực tiếp với muse.ai"
			}
			if copyNode.TopupLabel == "" {
				copyNode.TopupLabel = "Token bổ sung"
			}
			if copyNode.TopupValue == "" {
				copyNode.TopupValue = "Chưa lấy được dữ liệu từ Muse.ai"
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

	dbDriver := "mysql"
	dbURL := ""
	if p.mysqlStore != nil {
		p.mysqlStore.mu.RLock()
		dbURL = maskDatabaseURL(p.mysqlStore.dbURL)
		p.mysqlStore.mu.RUnlock()
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
		MemAllocMB:        allocMB,
		DatabaseDriver:    dbDriver,
		DatabaseURL:       dbURL,
		Nodes:             safeNodes,
	}
}

// GetPublicSummary trả về tóm tắt trạng thái Pool cho tài khoản người dùng thường (ẩn chi tiết Node/Cookie/Proxy nội bộ của Admin).
func (p *MuseRouterPool) GetPublicSummary() RouterSummary {
	sum := p.GetSummary()
	sum.DatabaseURL = ""
	sum.Nodes = []*MuseAccountNode{}
	return sum
}

func ownerMatchesThread(threadOwnerID string, actorOwnerID string) bool {
	if actorOwnerID == "" || actorOwnerID == "admin" {
		return threadOwnerID == "" || threadOwnerID == "admin"
	}
	return threadOwnerID == actorOwnerID
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
	authType = strings.TrimSpace(authType)
	if authType == "" {
		authType = "cookie"
	}
	rawCredential = strings.TrimSpace(rawCredential)
	if authType == "cookie" && rawCredential != "" && !strings.Contains(rawCredential, "=") &&
		!strings.HasPrefix(rawCredential, "{") && !strings.HasPrefix(rawCredential, "[") &&
		!strings.HasPrefix(strings.ToLower(rawCredential), "http://") && !strings.HasPrefix(strings.ToLower(rawCredential), "https://") {
		rawCredential = "hatch_sess=" + rawCredential
	}
	if strings.TrimSpace(rawCredential) == "" {
		return nil, errors.New("ERR_EMPTY_CREDENTIAL")
	}
	requestedLabel := strings.TrimSpace(label)
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

	newSession := extractCookieValue(rawCredential, "hatch_sess")
	for _, existing := range p.nodes {
		if existing == nil || existing.AuthType != authType {
			continue
		}
		duplicate := existing.Fingerprint == fp
		if !duplicate && authType == "cookie" && newSession != "" {
			oldCredential, decryptErr := p.rustBridge.DecryptSecret(existing.EncryptedCredential)
			if decryptErr == nil {
				oldSession := extractCookieValue(oldCredential, "hatch_sess")
				duplicate = oldSession != "" && ConstantTimeStringEquals(oldSession, newSession)
			}
		}
		if !duplicate {
			continue
		}

		existing.EncryptedCredential = encrypted
		existing.Fingerprint = fp
		existing.DeviceProfile = computeDeterministicDeviceProfile(fp)
		existing.HasHatchSess = hasSess
		existing.HasHatchGw = hasGw
		if requestedLabel != "" {
			existing.Label = requestedLabel
		}
		if cleanProxy != "" {
			existing.ProxyURL = cleanProxy
		}
		existing.StatusDetail = "Muse.ai session refreshed; checking account"
		existingID := existing.ID
		saveErr := p.saveVaultLocked()
		p.mu.Unlock()
		if saveErr != nil {
			return nil, saveErr
		}
		_, _ = p.WakeAndProbeNode(ctx, existingID)
		p.mu.RLock()
		defer p.mu.RUnlock()
		for _, node := range p.nodes {
			if node.ID == existingID {
				copyNode := *node
				copyNode.EncryptedCredential = ""
				return &copyNode, nil
			}
		}
		return nil, errors.New("ERR_NODE_NOT_FOUND")
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
	if p.shouldTryPythonBridge() {
		reqBody, _ := json.Marshal(map[string]interface{}{})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.aiBridgeURL+"/internal/oauth-link", bytes.NewReader(reqBody))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Internal-Bridge-Token", p.aiBridgeToken)
			if resp, errDo := p.httpClient.Do(req); errDo == nil {
				bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
				resp.Body.Close()
				_ = json.Unmarshal(bodyBytes, &out)
			} else {
				p.markPythonBridgeDown()
			}
		}
	}

	if out == nil || out["login_url"] == nil {
		directOut, errDirect := p.generateOAuthLoginLinkDirect(ctx)
		if errDirect != nil {
			return nil, errDirect
		}
		out = directOut
	}

	if p.mysqlStore != nil {
		sessID, _ := out["session_id"].(string)
		loginURL, _ := out["login_url"].(string)
		stateVal, _ := out["state"].(string)
		appIDVal, _ := out["app_id"].(string)
		redirectVal, _ := out["redirect_uri"].(string)
		_ = p.mysqlStore.SaveOAuthSession(sessID, loginURL, stateVal, appIDVal, redirectVal)
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
	pkceCookies := map[string]string{}
	for _, c := range resp.Cookies() {
		if c.Name != "" && c.Value != "" {
			pkceCookies[c.Name] = c.Value
		}
	}
	storeOAuthNativeSession(stateVal, pkceCookies)
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
	if !p.shouldTryPythonBridge() {
		return nil, errors.New("ERR_AI_BRIDGE_UNREACHABLE: cloud mode uses inline remote browser login")
	}
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
	credential, err := p.rustBridge.DecryptSecret(encrypted)
	if err != nil {
		return nil, fmt.Errorf("ERR_BROWSER_LOGIN_DECRYPT: %w", err)
	}
	if strings.TrimSpace(credential) == "" {
		return nil, errors.New("ERR_BROWSER_LOGIN_EMPTY_CREDENTIAL")
	}
	_ = tokensRemaining
	node, _, err := p.IngestAccountCookie(parentCtx, label, credential, cleanProxy)
	return node, err
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

	if p.shouldTryPythonBridge() {
		ctx, cancel := context.WithTimeout(parentCtx, 10*time.Second)
		reqBody, _ := json.Marshal(map[string]interface{}{
			"encrypted_credential": target.EncryptedCredential,
			"node_label":           target.Label,
			"proxy_url":            target.ProxyURL,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.aiBridgeURL+"/internal/open-native", bytes.NewReader(reqBody))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Internal-Bridge-Token", p.aiBridgeToken)
			resp, errDo := p.httpClient.Do(req)
			if errDo == nil {
				body, errRead := io.ReadAll(io.LimitReader(resp.Body, 65536))
				resp.Body.Close()
				cancel()
				if errRead == nil && resp.StatusCode == http.StatusOK {
					var out map[string]interface{}
					if err := json.Unmarshal(body, &out); err == nil && out != nil {
						return out, nil
					}
				}
			} else {
				cancel()
				p.markPythonBridgeDown()
			}
		} else {
			cancel()
		}
	}

	rawCookie, _ := p.rustBridge.DecryptSecret(target.EncryptedCredential)
	upstream := strings.TrimSpace(target.UpstreamURL)
	if upstream == "" {
		upstream = "https://muse.ai"
	}
	return map[string]interface{}{
		"status":        "browser_relay",
		"browser_relay": true,
		"node_id":       target.ID,
		"node_label":    target.Label,
		"upstream_url":  upstream,
		"relay_cookie":  strings.TrimSpace(rawCookie),
		"detail":        fmt.Sprintf("Đã mở cửa sổ trình duyệt Muse.ai gốc cho %s.", target.Label),
	}, nil
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
	targetNode := p.nodes[idx]
	targetFP := targetNode.Fingerprint

	p.nodes = append(p.nodes[:idx], p.nodes[idx+1:]...)
	if p.mysqlStore != nil {
		if err := p.mysqlStore.DeleteAccount(nodeID, targetFP); err != nil {
			LogError("MySQL DeleteAccount error for node %s (fp %s): %v", nodeID, targetFP, err)
		}
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
	needReload := len(p.nodes) == 0 && p.mysqlStore != nil
	p.mu.RUnlock()

	if needReload {
		_ = p.loadVault()
	}

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

	var resp *http.Response
	var err error
	if p.shouldTryPythonBridge() {
		reqPayload, _ := json.Marshal(map[string]interface{}{
			"encrypted_credential": target.EncryptedCredential,
			"auth_type":            target.AuthType,
			"upstream_url":         target.UpstreamURL,
			"node_id":              target.ID,
			"proxy_url":            target.ProxyURL,
		})
		req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, p.aiBridgeURL+"/internal/probe", bytes.NewReader(reqPayload))
		if errReq != nil {
			return nil, errReq
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Bridge-Token", p.aiBridgeToken)
		resp, err = p.httpClient.Do(req)
		if err != nil {
			p.markPythonBridgeDown()
		} else if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			p.markPythonBridgeDown()
			err = errors.New("AI_BRIDGE_UNAUTHORIZED")
		}
	} else {
		err = errors.New("AI_BRIDGE_BYPASSED")
	}

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
				// Muse.ai reports the current balance; local chat usage is tracked
				// separately and must not be deducted from this value a second time.
				n.TokensRemaining = int64(tok)
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

	if resolved, ok := resolveOAuthCallbackNative(ctx, trimmed); ok {
		trimmed = resolved
		if enc, errEnc := p.rustBridge.EncryptSecret(resolved); errEnc == nil {
			newFP := p.rustBridge.ComputeFingerprint(resolved)
			p.mu.Lock()
			for _, n := range p.nodes {
				if n.ID == target.ID {
					n.EncryptedCredential = enc
					n.Fingerprint = newFP
					n.DeviceProfile = computeDeterministicDeviceProfile(newFP)
					_ = p.saveVaultLocked()
					break
				}
			}
			p.mu.Unlock()
			target.Fingerprint = newFP
		} else {
			LogWarn("Không mã hóa lại được cookie sau khi đổi callback: %v", errEnc)
		}
	} else if isOAuthCallbackURL(trimmed) {
		return map[string]interface{}{
			"status":         "unavailable",
			"latency_ms":     float64(0),
			"has_hatch_sess": false,
			"has_hatch_gw":   false,
			"detail":         "Link Callback không đổi được sang phiên đăng nhập — hãy bấm 'Tự Lấy Link Đăng Nhập' tạo link mới rồi đăng nhập lại",
		}, nil
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
	sessionReq, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream+"/api/session", nil)
	if err != nil {
		return nil, err
	}
	sessionReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	sessionReq.Header.Set("Accept", "application/json")
	sessionReq.Header.Set("Cookie", cookieHeader)
	sessionResp, err := p.httpClient.Do(sessionReq)
	if err != nil {
		return nil, err
	}
	sessionBody, _ := io.ReadAll(io.LimitReader(sessionResp.Body, 32768))
	sessionResp.Body.Close()
	latMs := float64(time.Since(startTs).Milliseconds())
	if sessionResp.StatusCode == http.StatusUnauthorized || sessionResp.StatusCode == http.StatusForbidden || !hasSess {
		return map[string]interface{}{
			"status":         "unavailable",
			"latency_ms":     latMs,
			"has_hatch_sess": hasSess,
			"has_hatch_gw":   hasGw,
			"detail":         fmt.Sprintf("Muse.ai xác nhận phiên không hợp lệ (HTTP %d)", sessionResp.StatusCode),
		}, nil
	}
	if sessionResp.StatusCode < http.StatusOK || sessionResp.StatusCode >= http.StatusMultipleChoices {
		return map[string]interface{}{
			"status":         "unavailable",
			"latency_ms":     latMs,
			"has_hatch_sess": hasSess,
			"has_hatch_gw":   hasGw,
			"detail":         fmt.Sprintf("Không xác minh được phiên Muse.ai (HTTP %d)", sessionResp.StatusCode),
		}, nil
	}
	var sessionData struct {
		VMState string `json:"vm_state"`
	}
	if err := json.Unmarshal(sessionBody, &sessionData); err != nil {
		return map[string]interface{}{
			"status":         "unavailable",
			"latency_ms":     latMs,
			"has_hatch_sess": hasSess,
			"has_hatch_gw":   hasGw,
			"detail":         "Muse.ai trả dữ liệu phiên không đọc được; chưa xác nhận tài khoản",
		}, nil
	}
	if strings.EqualFold(strings.TrimSpace(sessionData.VMState), "DISABLED") {
		return map[string]interface{}{
			"status":         "unavailable",
			"latency_ms":     latMs,
			"has_hatch_sess": hasSess,
			"has_hatch_gw":   hasGw,
			"detail":         "Muse.ai báo VM của tài khoản đang bị vô hiệu hóa",
		}, nil
	}

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
	verBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	latMs = float64(time.Since(startTs).Milliseconds())

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || !hasSess {
		return map[string]interface{}{
			"status":         "unavailable",
			"latency_ms":     latMs,
			"has_hatch_sess": hasSess,
			"has_hatch_gw":   hasGw,
			"detail":         fmt.Sprintf("Cookie hatch_sess hết hạn hoặc không hợp lệ (HTTP %d)", resp.StatusCode),
		}, nil
	}

	var verData struct {
		DeploymentID string `json:"deploymentId"`
	}
	_ = json.Unmarshal(verBody, &verData)

	// Subscription lookup may need to refresh Muse.ai's rotating Next.js action
	// ID from its current page/chunks when a cached action ID is stale.
	subCtx, cancelSub := context.WithTimeout(ctx, 12*time.Second)
	subInfo := p.fetchRealMuseSubscriptionGo(subCtx, upstream, cookieHeader, verData.DeploymentID)
	cancelSub()

	tokRem := subInfo.TokensRemaining
	detail := fmt.Sprintf("Đã đồng bộ Muse.ai (%dms · %s)", int(latMs), subInfo.FreePlanValue)
	if !subInfo.Fetched {
		detail = fmt.Sprintf("Đăng nhập Muse.ai thành công (%dms), nhưng chưa đọc được dữ liệu mức sử dụng; hãy thử đồng bộ lại", int(latMs))
	}
	return map[string]interface{}{
		"status":                      "ready",
		"latency_ms":                  latMs,
		"tokens_remaining":            tokRem,
		"subscription_data_available": subInfo.Fetched,
		"has_hatch_sess":              hasSess,
		"has_hatch_gw":                hasGw,
		"free_plan_label":             subInfo.FreePlanLabel,
		"free_plan_value":             subInfo.FreePlanValue,
		"free_plan_subtitle":          subInfo.FreePlanSubtitle,
		"free_plan_percent":           float64(subInfo.FreePlanPercent),
		"topup_label":                 subInfo.TopupLabel,
		"topup_value":                 subInfo.TopupValue,
		"topup_subtitle":              subInfo.TopupSubtitle,
		"topup_percent":               float64(subInfo.TopupPercent),
		"device_profile":              computeDeterministicDeviceProfile(target.Fingerprint),
		"detail":                      detail,
	}, nil
}

type museSubscriptionParsed struct {
	Fetched          bool
	FreePlanLabel    string
	FreePlanValue    string
	FreePlanSubtitle string
	FreePlanPercent  int
	TopupLabel       string
	TopupValue       string
	TopupSubtitle    string
	TopupPercent     int
	TokensRemaining  float64
}

var (
	reNextChunkJS      = regexp.MustCompile(`/_next/static/chunks/[^"'\s<>]+\.js(?:\?[^"'\s<>]*)?`)
	reNextActHex       = regexp.MustCompile(`[0-9a-f]{40,42}`)
	reMuseSubActionRef = regexp.MustCompile(`createServerReference\(["']([0-9a-f]{40,42})["']\s*,[^)]{0,512}["']fetchSubscriptionAction["']\)`)
)

func (p *MuseRouterPool) callMuseSubscriptionAction(ctx context.Context, upstream, cookieHeader, deployID, actionID string) (*museSubscriptionParsed, bool) {
	actionURL := upstream + "/"
	if deployID != "" && deployID != "verified" {
		actionURL = upstream + "/?dpl=" + url.QueryEscape(deployID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, actionURL, strings.NewReader(`[{"includeAgreement":true}]`))
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("Accept", "text/x-component")
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Next-Action", actionID)
	req.Header.Set("Next-Router-State-Tree", "%5B%22%22%2C%7B%22children%22%3A%5B%22(app)%22%2C%7B%22children%22%3A%5B%22(main)%22%2C%7B%22children%22%3A%5B%22__PAGE__%22%2C%7B%7D%5D%7D%5D%7D%5D%7D%2Cnull%2Cnull%2Ctrue%5D")
	req.Header.Set("Origin", upstream)
	req.Header.Set("Referer", upstream+"/")
	req.Header.Set("Accept-Language", "vi-VN,vi;q=0.9,en-US;q=0.8")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	if deployID != "" && deployID != "verified" {
		req.Header.Set("x-deployment-id", deployID)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	rawBytes, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil || len(rawBytes) == 0 {
		return nil, false
	}

	for _, line := range strings.Split(string(rawBytes), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, `"subscription"`) {
			continue
		}
		colonIdx := strings.IndexByte(line, ':')
		if colonIdx < 0 || colonIdx+1 >= len(line) {
			continue
		}
		var payload struct {
			Subscription *struct {
				UsageRowLabel      string `json:"usageRowLabel"`
				UsageRowValueLabel string `json:"usageRowValueLabel"`
				StatusSubtitle     string `json:"statusSubtitle"`
				Usage              *struct {
					PercentUsed float64 `json:"percentUsed"`
				} `json:"usage"`
				TopupRowLabel      string  `json:"topupRowLabel"`
				TopupRowValueLabel string  `json:"topupRowValueLabel"`
				BillingSubtitle    string  `json:"billingSubtitle"`
				TopupBalance       float64 `json:"topupBalance"`
				TopupTotal         float64 `json:"topupTotal"`
			} `json:"subscription"`
		}
		if err := json.Unmarshal([]byte(line[colonIdx+1:]), &payload); err != nil || payload.Subscription == nil {
			continue
		}
		sub := payload.Subscription
		if sub.Usage == nil && strings.TrimSpace(sub.UsageRowValueLabel) == "" &&
			strings.TrimSpace(sub.TopupRowValueLabel) == "" && sub.TopupBalance == 0 && sub.TopupTotal == 0 {
			continue
		}
		pctUsed := 0
		if sub.Usage != nil {
			pctUsed = int(math.Round(sub.Usage.PercentUsed))
			if pctUsed < 0 {
				pctUsed = 0
			} else if pctUsed > 100 {
				pctUsed = 100
			}
		}
		freeLbl := strings.TrimSpace(sub.UsageRowLabel)
		if freeLbl == "" {
			freeLbl = "Gói miễn phí"
		}
		freeVal := strings.TrimSpace(strings.ReplaceAll(sub.UsageRowValueLabel, "\u00a0", " "))
		if freeVal == "" {
			freeVal = fmt.Sprintf("Đã dùng %d%%", pctUsed)
		}
		freeSub := strings.TrimSpace(sub.StatusSubtitle)
		if freeSub == "" {
			freeSub = "Giới hạn hàng tuần"
		}
		topLbl := strings.TrimSpace(sub.TopupRowLabel)
		if topLbl == "" {
			topLbl = "Token bổ sung"
		}
		topVal := strings.TrimSpace(strings.ReplaceAll(sub.TopupRowValueLabel, "\u00a0", " "))
		if topVal == "" && sub.TopupBalance == 0 && sub.TopupTotal == 0 {
			topVal = "0"
		}
		if topVal == "" {
			topVal = "Chưa lấy được dữ liệu từ Muse.ai"
		}
		topSub := strings.TrimSpace(sub.BillingSubtitle)
		if topSub == "" {
			topSub = "Không bao giờ hết hạn"
		}
		topPct := 0
		if sub.TopupTotal > 0 {
			topPct = int(math.Round(((sub.TopupTotal - sub.TopupBalance) / sub.TopupTotal) * 100.0))
			if topPct < 0 {
				topPct = 0
			} else if topPct > 100 {
				topPct = 100
			}
		}
		tokRem := float64(0)
		if sub.TopupBalance > 0 {
			tokRem = sub.TopupBalance
		}
		return &museSubscriptionParsed{
			Fetched:          true,
			FreePlanLabel:    freeLbl,
			FreePlanValue:    freeVal,
			FreePlanSubtitle: freeSub,
			FreePlanPercent:  pctUsed,
			TopupLabel:       topLbl,
			TopupValue:       topVal,
			TopupSubtitle:    topSub,
			TopupPercent:     topPct,
			TokensRemaining:  tokRem,
		}, true
	}
	return nil, false
}

func (p *MuseRouterPool) discoverMuseSubActionID(ctx context.Context, upstream, cookieHeader string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream+"/", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Cookie", cookieHeader)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return ""
	}
	htmlBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 262144))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(htmlBytes) == 0 {
		return ""
	}
	chunks := reNextChunkJS.FindAllString(string(htmlBytes), 96)
	seen := map[string]bool{}
	uniqueChunks := make([]string, 0, len(chunks))
	for _, ch := range chunks {
		if !seen[ch] {
			seen[ch] = true
			uniqueChunks = append(uniqueChunks, ch)
		}
	}

	// The settings module is lazy-loaded and can be beyond the initial chunks.
	// Scan public static chunks concurrently, without sending session cookies.
	scanCtx, cancelScan := context.WithCancel(ctx)
	defer cancelScan()
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	var foundMu sync.Mutex
	found := ""
	for _, ch := range uniqueChunks {
		ch := ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-scanCtx.Done():
				return
			}
			defer func() { <-sem }()

			cReq, errReq := http.NewRequestWithContext(scanCtx, http.MethodGet, upstream+ch, nil)
			if errReq != nil {
				return
			}
			cResp, errDo := p.httpClient.Do(cReq)
			if errDo != nil {
				return
			}
			if cResp.ContentLength > 524288 {
				cResp.Body.Close()
				return
			}
			jsBytes, _ := io.ReadAll(io.LimitReader(cResp.Body, 524289))
			cResp.Body.Close()
			if len(jsBytes) > 524288 {
				return
			}
			js := string(jsBytes)
			match := reMuseSubActionRef.FindStringSubmatch(js)
			actionID := ""
			if len(match) > 1 {
				actionID = match[1]
			} else if idx := strings.Index(js, "fetchSubscriptionAction"); idx >= 0 {
				start := idx - 350
				if start < 0 {
					start = 0
				}
				end := idx + 350
				if end > len(js) {
					end = len(js)
				}
				actionID = reNextActHex.FindString(js[start:end])
			}
			if actionID == "" {
				return
			}
			foundMu.Lock()
			if found == "" {
				found = actionID
				cancelScan()
			}
			foundMu.Unlock()
		}()
	}
	wg.Wait()
	foundMu.Lock()
	defer foundMu.Unlock()
	if found != "" && p.cache != nil {
		p.cache.Set("muse_sub_action_id", found, 2*time.Hour)
	}
	return found
}

func (p *MuseRouterPool) fetchRealMuseSubscriptionGo(ctx context.Context, upstream, cookieHeader, deployID string) museSubscriptionParsed {
	def := museSubscriptionParsed{
		FreePlanLabel:    "Gói miễn phí",
		FreePlanValue:    "Chưa lấy được dữ liệu từ Muse.ai",
		FreePlanSubtitle: "Muse.ai chưa trả dữ liệu usage; hãy thử đồng bộ lại",
		FreePlanPercent:  0,
		TopupLabel:       "Token bổ sung",
		TopupValue:       "Chưa lấy được dữ liệu từ Muse.ai",
		TopupSubtitle:    "Không bao giờ hết hạn",
		TopupPercent:     0,
		TokensRemaining:  0,
	}

	actionID := "409b972cbb33d3e372f4ca9d3c8332677217064f62"
	if p.cache != nil {
		if cached, ok := p.cache.Get("muse_sub_action_id"); ok {
			if s, okS := cached.(string); okS && s != "" {
				actionID = s
			}
		}
	}

	if parsed, ok := p.callMuseSubscriptionAction(ctx, upstream, cookieHeader, deployID, actionID); ok && parsed != nil {
		return *parsed
	}
	if newActionID := p.discoverMuseSubActionID(ctx, upstream, cookieHeader); newActionID != "" && newActionID != actionID {
		if parsed, ok := p.callMuseSubscriptionAction(ctx, upstream, cookieHeader, deployID, newActionID); ok && parsed != nil {
			return *parsed
		}
	}
	return def
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
	return p.RouteAndExecuteChatForOwner(parentCtx, "", threadID, preferredNodeID, model, workspace, userPrompt, attachments, idempotencyKey)
}

func (p *MuseRouterPool) RouteAndExecuteChatForOwner(
	parentCtx context.Context,
	ownerID string,
	threadID string,
	preferredNodeID string,
	model string,
	workspace string,
	userPrompt string,
	attachments []ChatAttachment,
	idempotencyKey string,
	onChunk ...func(chunkType string, data map[string]interface{}),
) (map[string]interface{}, error) {
	if idempotencyKey != "" {
		cacheKey := "idem:" + ownerID + ":" + idempotencyKey
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

	resolvedThreadID, turnID, messagesPayload := p.reserveQuestionTurnForOwner(
		ownerID,
		threadID,
		model,
		workspace,
		userPrompt,
		attachments,
	)
	if len(onChunk) > 0 && onChunk[0] != nil {
		onChunk[0]("start", map[string]interface{}{
			"thread_id": resolvedThreadID,
			"turn_id":   turnID,
			"model":     model,
		})
	}
	var lastErr error
	skipClientRelay, _ := parentCtx.Value(skipBrowserRelayContextKey{}).(bool)
	forceClientRelay, _ := parentCtx.Value(clientBrowserRelayContextKey{}).(bool)

	for _, candidate := range candidates {
		p.markNodeRunning(candidate.ID)
		startTs := time.Now()
		// Nếu máy chủ có sẵn Chromium Headless CDP, ưu tiên chạy CDP trực tiếp trên máy chủ để cách ly 100% cuộc trò chuyện sạch!
		// Chỉ khi máy chủ không có sẵn Chromium mới chuyển tiếp qua Client Browser Relay của Extension.
		if !skipClientRelay && forceClientRelay && candidate.AuthType == "cookie" && candidate.HasHatchSess && (p.cdpManager == nil || !p.cdpManager.HasBrowserAvailable()) {
			LogInfo("Routing web chat through client browser relay for node %s", candidate.ID)
			return p.prepareBrowserRelayResponse(candidate, resolvedThreadID, turnID, model, userPrompt)
		}

		ctx, cancel := context.WithTimeout(parentCtx, 60*time.Second)
		var resp *http.Response
		var errDo error
		if p.shouldTryPythonBridge() {
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
			resp, errDo = p.httpClient.Do(req)
			if errDo != nil {
				p.markPythonBridgeDown()
			} else if resp.StatusCode == http.StatusUnauthorized {
				_ = resp.Body.Close()
				p.markPythonBridgeDown()
				errDo = errors.New("AI_BRIDGE_UNAUTHORIZED")
			}
		} else {
			errDo = errors.New("AI_BRIDGE_BYPASSED")
		}
		if errDo != nil {
			cancel()
			errMsg := fmt.Sprintf("Không kết nối được dịch vụ AI Bridge trên %s: %v", candidate.Label, errDo)
			upClean := strings.TrimRight(strings.ToLower(strings.TrimSpace(candidate.UpstreamURL)), "/")
			if candidate.AuthType == "api_key" || (!candidate.HasHatchSess && upClean != "" && !strings.HasSuffix(upClean, "muse.ai")) {
				directCtx, directCancel := context.WithTimeout(parentCtx, 45*time.Second)
				replyDirect, tokDirect, errDirect := p.executeOpenAICompatibleDirect(directCtx, candidate, model, messagesPayload)
				directCancel()
				if errDirect == nil {
					latMs := int(time.Since(startTs).Milliseconds())
					return p.CompleteBrowserRelayTurn(resolvedThreadID, turnID, candidate.ID, model, replyDirect, tokDirect, latMs, "")
				}
				errMsg = errDirect.Error()
			} else if candidate.HasHatchSess {
				if p.cdpManager != nil && p.cdpManager.HasBrowserAvailable() {
					var onChunkCDP func(delta string, fullText string)
					if len(onChunk) > 0 && onChunk[0] != nil {
						onChunkCDP = func(delta string, fullText string) {
							onChunk[0]("delta", map[string]interface{}{
								"thread_id":   resolvedThreadID,
								"turn_id":     turnID,
								"delta":       delta,
								"content":     fullText,
								"routed_node": candidate.Label,
								"node_id":     candidate.ID,
								"done":        false,
							})
						}
					}
					replyCDP, tokCDP, latCDP, errCDP := p.cdpManager.ExecuteMuseChat(parentCtx, candidate, userPrompt, onChunkCDP)
					if errCDP == nil {
						return p.CompleteBrowserRelayTurn(resolvedThreadID, turnID, candidate.ID, model, replyCDP, tokCDP, latCDP, "")
					}
					LogWarn("CDP server-side chat error on %s: %v", candidate.Label, errCDP)
					p.unmarkNodeRunningKeepReady(candidate.ID)
					lastErr = errCDP
					if !p.autoFailover {
						break
					}
					continue
				}
				if p.cdpManager == nil && !skipClientRelay {
					return p.prepareBrowserRelayResponse(candidate, resolvedThreadID, turnID, model, userPrompt)
				}
				errMsg = "Máy chủ chưa thể kết nối Headless Chromium tới Muse.ai"
				if lastErr != nil {
					errMsg = lastErr.Error()
				}
			}
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
		if strings.TrimSpace(assistantReply) == "" {
			emptyReplyErr := errors.New("Muse AI bridge returned an empty reply")
			LogWarn("Node %s returned HTTP 200 with empty content; trying fallback", candidate.Label)
			lastErr = emptyReplyErr
			if p.cdpManager == nil && !skipClientRelay && candidate.AuthType == "cookie" && candidate.HasHatchSess {
				return p.prepareBrowserRelayResponse(candidate, resolvedThreadID, turnID, model, userPrompt)
			}
			p.releaseNode(candidate.ID, 0, latencyMs, false, emptyReplyErr.Error())
			if !p.autoFailover {
				break
			}
			continue
		}
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

		if p.mysqlStore != nil && updatedThread != nil {
			p.mysqlStore.RecordRoutingTelemetry(
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
			p.cache.Set("idem:"+ownerID+":"+idempotencyKey, result, 5*time.Minute)
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
	if !p.shouldTryPythonBridge() {
		return busyFPs, busyLabels
	}
	ctxTimeout, cancel := context.WithTimeout(parentCtx, 350*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctxTimeout, http.MethodGet, p.aiBridgeURL+"/internal/live-status", nil)
	if err != nil {
		return busyFPs, busyLabels
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		p.markPythonBridgeDown()
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
//   - Nếu tài khoản đang trả lời dở câu hỏi trước (ActiveRequests > 0) hoặc còn đang làm việc nền trên Muse gốc (busyFPs/busyLabels),
//     tự động đẩy tài khoản đó xuống sau để câu hỏi mới nhảy sang tài khoản khác đang rảnh.
//   - Nếu tài khoản vừa trả lời đoạn chat này đang hoàn toàn rảnh thì mới giữ ở tài khoản đó.
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
	return p.reserveQuestionTurnForOwner("", threadID, model, workspace, userPrompt, attachments)
}

func cleanStaleMessagesInThread(t *ChatThread, nowNano int64) bool {
	if t == nil || len(t.Messages) == 0 {
		return false
	}
	staleTurns := make(map[string]bool)
	changed := false
	for _, m := range t.Messages {
		if m.Role == "assistant" && strings.TrimSpace(m.Content) == "" && len(m.Attachments) == 0 {
			if !m.Pending {
				changed = true
				if m.TurnID != "" {
					staleTurns[m.TurnID] = true
				}
				continue
			}
			// Kiểm tra thời gian tạo turn (turn_<unixNano>)
			if strings.HasPrefix(m.TurnID, "turn_") {
				if tsNano, err := strconv.ParseInt(strings.TrimPrefix(m.TurnID, "turn_"), 10, 64); err == nil {
					if nowNano-tsNano > int64(95*time.Second) {
						staleTurns[m.TurnID] = true
						changed = true
					}
				}
			} else {
				changed = true
			}
		}
	}
	if !changed {
		return false
	}
	filtered := make([]ChatMessage, 0, len(t.Messages))
	for _, m := range t.Messages {
		if m.TurnID != "" && staleTurns[m.TurnID] {
			continue
		}
		if m.Role == "assistant" && strings.TrimSpace(m.Content) == "" && len(m.Attachments) == 0 {
			continue
		}
		filtered = append(filtered, m)
	}
	t.Messages = filtered
	return true
}

func (p *MuseRouterPool) reserveQuestionTurnForOwner(
	ownerID string,
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
	idTakenByOther := false
	for _, t := range p.threads {
		if t.ID == threadID {
			if ownerMatchesThread(t.OwnerID, ownerID) {
				target = t
			} else {
				idTakenByOther = true
			}
			break
		}
	}

	if target != nil {
		cleanStaleMessagesInThread(target, now.UnixNano())
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
		if strings.TrimSpace(newID) == "" || idTakenByOther {
			newID = fmt.Sprintf("thread_%d_%d", now.UnixMilli(), now.UnixNano()%10000)
		}
		target = &ChatThread{
			ID:        newID,
			OwnerID:   ownerID,
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

	_ = p.saveSingleThreadLocked(target)
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
		_ = p.saveSingleThreadLocked(t)
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
		targetTurnID := strings.TrimSpace(turnID)
		if targetTurnID == "" {
			for i := len(t.Messages) - 1; i >= 0; i-- {
				if t.Messages[i].Pending && t.Messages[i].TurnID != "" {
					targetTurnID = t.Messages[i].TurnID
					break
				}
			}
		}
		filtered := make([]ChatMessage, 0, len(t.Messages))
		for _, m := range t.Messages {
			if targetTurnID != "" && m.TurnID == targetTurnID {
				continue
			}
			if targetTurnID == "" && m.Pending && m.Role == "assistant" && strings.TrimSpace(m.Content) == "" {
				continue
			}
			filtered = append(filtered, m)
		}
		t.Messages = filtered
		_ = p.saveSingleThreadLocked(t)
		return
	}
}

func (p *MuseRouterPool) cancelQuestionTurnIfStillPending(threadID string, turnID string) {
	if threadID == "" || turnID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, t := range p.threads {
		if t.ID != threadID {
			continue
		}
		stillPending := false
		for _, m := range t.Messages {
			if m.TurnID == turnID && m.Pending {
				stillPending = true
				break
			}
		}
		if !stillPending {
			return
		}
		filtered := make([]ChatMessage, 0, len(t.Messages))
		for _, m := range t.Messages {
			if m.TurnID == turnID {
				continue
			}
			filtered = append(filtered, m)
		}
		t.Messages = filtered
		_ = p.saveSingleThreadLocked(t)
		return
	}
}

func (p *MuseRouterPool) ListThreads() []*ChatThread {
	return p.ListThreadsForOwner("")
}

func (p *MuseRouterPool) ListThreadsForOwner(ownerID string) []*ChatThread {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]*ChatThread, 0, len(p.threads))
	for _, t := range p.threads {
		if ownerMatchesThread(t.OwnerID, ownerID) {
			out = append(out, cloneChatThread(t))
		}
	}
	return out
}

func (p *MuseRouterPool) HasThreadAccess(ownerID string, threadID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, t := range p.threads {
		if t.ID == threadID {
			return ownerMatchesThread(t.OwnerID, ownerID)
		}
	}
	return false
}

func (p *MuseRouterPool) CreateNewThread(title string, model string, workspace string) *ChatThread {
	return p.CreateNewThreadForOwner("", title, model, workspace)
}

func (p *MuseRouterPool) CreateNewThreadForOwner(ownerID string, title string, model string, workspace string) *ChatThread {
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

	now := time.Now()
	t := &ChatThread{
		ID:        fmt.Sprintf("thread_%d_%d", now.UnixMilli(), now.UnixNano()%10000),
		OwnerID:   ownerID,
		Title:     title,
		Model:     model,
		Workspace: workspace,
		CreatedAt: now.Format(time.RFC3339),
		UpdatedAt: now.Format(time.RFC3339),
		Messages:  []ChatMessage{},
	}
	p.threads = append([]*ChatThread{t}, p.threads...)
	_ = p.saveSingleThreadLocked(t)
	c := *t
	return &c
}

func (p *MuseRouterPool) RenameThreadForOwner(ownerID string, threadID string, newTitle string) error {
	cleanTitle := strings.TrimSpace(newTitle)
	if cleanTitle == "" {
		return errors.New("ERR_EMPTY_TITLE")
	}
	if len([]rune(cleanTitle)) > 120 {
		cleanTitle = string([]rune(cleanTitle)[:120])
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range p.threads {
		if t.ID == threadID && ownerMatchesThread(t.OwnerID, ownerID) {
			t.Title = cleanTitle
			t.UpdatedAt = time.Now().Format(time.RFC3339)
			return p.saveSingleThreadLocked(t)
		}
	}
	return errors.New("ERR_THREAD_NOT_FOUND")
}

func (p *MuseRouterPool) DeleteThread(threadID string) error {
	return p.DeleteThreadForOwner("", threadID)
}

func (p *MuseRouterPool) DeleteThreadForOwner(ownerID string, threadID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	idx := -1
	for i, t := range p.threads {
		if t.ID == threadID && ownerMatchesThread(t.OwnerID, ownerID) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("ERR_THREAD_NOT_FOUND")
	}
	p.threads = append(p.threads[:idx], p.threads[idx+1:]...)
	if p.mysqlStore != nil {
		_ = p.mysqlStore.DeleteThread(threadID)
	}
	return nil
}

func (p *MuseRouterPool) PurgeOwnerThreadsInMemory(ownerID string) {
	if ownerID == "" || ownerID == "admin" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := make([]*ChatThread, 0, len(p.threads))
	for _, t := range p.threads {
		if t.OwnerID != ownerID {
			kept = append(kept, t)
		}
	}
	p.threads = kept
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
	return p.GetLiveActivityStatusForOwner(ctx, "")
}

func (p *MuseRouterPool) GetLiveActivityStatusForOwner(ctx context.Context, ownerID string) map[string]interface{} {
	if !p.shouldTryPythonBridge() {
		return map[string]interface{}{
			"active":       false,
			"status_text":  "Đã kết nối",
			"avatar_frame": "",
			"partial_md":   "",
		}
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctxTimeout, http.MethodGet, p.aiBridgeURL+"/internal/live-status", nil)
	if err == nil {
		resp, err2 := p.httpClient.Do(req)
		if err2 != nil {
			p.markPythonBridgeDown()
		} else {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var out map[string]interface{}
				if json.NewDecoder(resp.Body).Decode(&out) == nil && out != nil {
					if rawFollowups, has := out["followups"]; has {
						if updated := p.applyBackgroundFollowups(rawFollowups); len(updated) > 0 {
							filtered := make([]*ChatThread, 0, len(updated))
							for _, ut := range updated {
								if ownerMatchesThread(ut.OwnerID, ownerID) {
									filtered = append(filtered, ut)
								}
							}
							if len(filtered) > 0 {
								out["updated_threads"] = filtered
							} else {
								delete(out, "updated_threads")
							}
						}
					}
					delete(out, "followups")
					if sessID, _ := out["session_id"].(string); sessID != "" && !p.HasThreadAccess(ownerID, sessID) {
						return map[string]interface{}{
							"active":       false,
							"status_text":  "Đã kết nối",
							"avatar_frame": "",
							"partial_md":   "",
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
