package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite"
)

// LibSQLStore quản lý kết nối trực tiếp tới cơ sở dữ liệu chuẩn SQL40 / MySQL / libSQL
// (hỗ trợ cả MySQL 8.0 TLS Aiven Cloud mysql://..., nhúng trực tiếp file:... WAL lẫn libsql://).
type LibSQLStore struct {
	mu              sync.RWMutex
	db              *sql.DB
	dataDir         string
	dbURL           string
	dbFile          string
	authToken       string
	isRemote        bool
	isMySQL         bool
	statusMu        sync.RWMutex
	cachedStatus    *LibSQLStatusResponse
	cachedStatusExp time.Time
}

func (s *LibSQLStore) invalidateStatusCache() {
	if s == nil {
		return
	}
	s.statusMu.Lock()
	s.cachedStatus = nil
	s.cachedStatusExp = time.Time{}
	s.statusMu.Unlock()
}

// LibSQLTableStat thống kê số bản ghi thực tế của từng bảng trong libSQL.
type LibSQLTableStat struct {
	TableName string `json:"table_name"`
	RowCount  int64  `json:"row_count"`
}

// LibSQLStatusResponse phản hồi trạng thái chi tiết của hệ thống cơ sở dữ liệu libSQL.
type LibSQLStatusResponse struct {
	Connected     bool              `json:"connected"`
	Driver        string            `json:"driver"`
	EngineVersion string            `json:"engine_version"`
	DatabaseURL   string            `json:"database_url"`
	DatabaseFile  string            `json:"database_file"`
	JournalMode   string            `json:"journal_mode"`
	IsRemote      bool              `json:"is_remote"`
	FileSizeBytes int64             `json:"file_size_bytes"`
	Tables        []LibSQLTableStat `json:"tables"`
	CheckedAt     string            `json:"checked_at"`
}

// LibSQLQueryResult kết quả truy vấn trực tiếp trên libSQL.
type LibSQLQueryResult struct {
	Columns         []string        `json:"columns"`
	Rows            [][]interface{} `json:"rows"`
	RowsAffected    int64           `json:"rows_affected"`
	LastInsertRowID int64           `json:"last_insert_rowid"`
	ExecutionTimeMs int64           `json:"execution_time_ms"`
	DatabaseDriver  string          `json:"database_driver"`
}

// Cấu trúc Hrana v2 HTTP Pipeline chuẩn của libSQL (/v2/pipeline)
type HranaPipelineRequest struct {
	Baton    string               `json:"baton,omitempty"`
	Requests []HranaStreamRequest `json:"requests"`
}

type HranaStreamRequest struct {
	Type  string          `json:"type"` // "execute" | "batch" | "sequence" | "describe" | "close"
	Stmt  *HranaStmt      `json:"stmt,omitempty"`
	Batch *HranaBatchSpec `json:"batch,omitempty"`
	Sql   string          `json:"sql,omitempty"`
}

type HranaStmt struct {
	Sql      string     `json:"sql"`
	Args     []HranaArg `json:"args,omitempty"`
	WantRows *bool      `json:"want_rows,omitempty"`
}

type HranaBatchSpec struct {
	Steps []HranaBatchStep `json:"steps"`
}

type HranaBatchStep struct {
	Stmt HranaStmt `json:"stmt"`
}

type HranaArg struct {
	Type  string      `json:"type"` // "text" | "integer" | "float" | "null" | "blob"
	Value interface{} `json:"value,omitempty"`
}

type HranaCol struct {
	Name     string `json:"name"`
	Decltype string `json:"decltype,omitempty"`
}

type HranaStmtResult struct {
	Cols             []HranaCol   `json:"cols"`
	Rows             [][]HranaArg `json:"rows"`
	AffectedRowCount int64        `json:"affected_row_count"`
	LastInsertRowid  *string      `json:"last_insert_rowid,omitempty"`
}

type HranaStreamResult struct {
	Type     string               `json:"type"` // "ok" | "error"
	Response *HranaStreamResponse `json:"response,omitempty"`
	Error    *HranaErrorObject    `json:"error,omitempty"`
}

type HranaStreamResponse struct {
	Type   string           `json:"type"` // "execute" | "batch" | "sequence" | "close"
	Result *HranaStmtResult `json:"result,omitempty"`
}

type HranaErrorObject struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

type HranaPipelineResponse struct {
	Baton   *string             `json:"baton"`
	BaseUrl *string             `json:"base_url"`
	Results []HranaStreamResult `json:"results"`
}

func normalizeFileLibSQLURL(dataDir string) (string, string, error) {
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		return "", "", err
	}
	absDir, err := filepath.Abs(dataDir)
	if err != nil {
		return "", "", err
	}
	dbFilePath := filepath.Join(absDir, "muse_router.libsql.db")
	slashPath := filepath.ToSlash(dbFilePath)
	return "file:" + slashPath, dbFilePath, nil
}

func cloudSQLMasterKey() string {
	if k := strings.TrimSpace(os.Getenv("MUSE_VAULT_MASTER_KEY")); k != "" {
		return k
	}
	return "MUSE_ROUTER_SQL40_CLOUD_VAULT_KEY_2026_AES256CTR"
}

func maskDatabaseURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(strings.ToLower(trimmed), "mysql://") {
		return trimmed
	}
	cfg, err := parseMySQLConnectionURL(trimmed)
	if err != nil {
		return "mysql://***"
	}
	return fmt.Sprintf("mysql://%s:***@%s:%s/%s?ssl-mode=REQUIRED", cfg.User, cfg.Host, cfg.Port, cfg.Database)
}

func isProductionDataDir(dataDir string) bool {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return false
	}
	lower := strings.ToLower(filepath.ToSlash(abs))
	// Không tự động nạp cấu hình Cloud trong thư mục tạm của go test
	if strings.Contains(lower, "/temp/") || strings.Contains(lower, "/tmp/") || strings.Contains(lower, "test_") {
		return false
	}
	return true
}

// SaveEncryptedCloudSQLConfig mã hóa chuỗi kết nối SQL bằng chuẩn AES-256-CTR + HMAC-SHA256 trước khi ghi xuống đĩa (PHAN 3).
func SaveEncryptedCloudSQLConfig(dataDir string, rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return errors.New("empty sql url")
	}
	enc, err := encryptSecretNative(trimmed, cloudSQLMasterKey())
	if err != nil {
		return err
	}
	_ = os.MkdirAll(dataDir, 0750)
	targetFile := filepath.Join(dataDir, "sql40_cloud.enc")
	if err := os.WriteFile(targetFile, []byte(enc), 0600); err != nil {
		return err
	}
	// Đồng thời lưu bản mã hóa tại thư mục engine nếu đang ở môi trường dự án để Render Cloud tự động nhận diện
	if isProductionDataDir(dataDir) {
		engineEncPath := filepath.Join(filepath.Dir(dataDir), "muse_router_engine", "sql40_cloud.enc")
		if _, stErr := os.Stat(filepath.Dir(engineEncPath)); stErr == nil {
			_ = os.WriteFile(engineEncPath, []byte(enc), 0600)
		}
	}
	return nil
}

// LoadEncryptedCloudSQLConfig giải mã cấu hình SQL Cloud từ tệp AES-256-CTR + HMAC-SHA256.
func LoadEncryptedCloudSQLConfig(dataDir string) string {
	candidates := []string{
		filepath.Join(dataDir, "sql40_cloud.enc"),
	}
	if isProductionDataDir(dataDir) {
		candidates = append(candidates,
			filepath.Join("sql40_cloud.enc"),
			filepath.Join("muse_router_engine", "sql40_cloud.enc"),
			filepath.Join(filepath.Dir(dataDir), "muse_router_engine", "sql40_cloud.enc"),
		)
	}
	for _, p := range candidates {
		rawBytes, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		cipherText := strings.TrimSpace(string(rawBytes))
		if cipherText == "" {
			continue
		}
		plain, err := decryptSecretNative(cipherText, cloudSQLMasterKey())
		if err == nil && strings.TrimSpace(plain) != "" {
			return strings.TrimSpace(plain)
		}
	}
	return ""
}

// NewLibSQLStore khởi tạo kết nối chuẩn SQL40 (MySQL 8.0 TLS Aiven Cloud / SQLite WAL).
func NewLibSQLStore(dataDir string, customURL string, authToken string) (*LibSQLStore, error) {
	defaultURL, dbFile, err := normalizeFileLibSQLURL(dataDir)
	if err != nil {
		return nil, err
	}

	targetURL := strings.TrimSpace(customURL)
	if targetURL == "" {
		targetURL = nonEmptyStr(
			os.Getenv("MUSE_MYSQL_URL"),
			os.Getenv("DATABASE_URL"),
			os.Getenv("MUSE_SQL_URL"),
			os.Getenv("MUSE_LIBSQL_URL"),
		)
	}
	if targetURL == "" {
		targetURL = LoadEncryptedCloudSQLConfig(dataDir)
	}
	if targetURL == "" {
		targetURL = defaultURL
	}
	if authToken == "" {
		authToken = strings.TrimSpace(os.Getenv("MUSE_LIBSQL_AUTH_TOKEN"))
	}

	store := &LibSQLStore{
		dataDir: dataDir,
		dbFile:  dbFile,
	}
	if err := store.connectInternal(targetURL, authToken); err != nil {
		if targetURL != defaultURL {
			LogWarn("Cloud SQL (%s) connect failed (%v), falling back to local SQL40 WAL", maskDatabaseURL(targetURL), err)
			if errFallback := store.connectInternal(defaultURL, ""); errFallback != nil {
				return nil, errFallback
			}
		} else {
			return nil, err
		}
	}
	if err := store.initSchema(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func (s *LibSQLStore) connectInternal(targetURL string, authToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lowerURL := strings.ToLower(strings.TrimSpace(targetURL))
	isMySQL := strings.HasPrefix(lowerURL, "mysql://")
	isRemote := isMySQL ||
		strings.HasPrefix(lowerURL, "libsql://") ||
		strings.HasPrefix(lowerURL, "http://") ||
		strings.HasPrefix(lowerURL, "https://") ||
		strings.HasPrefix(lowerURL, "ws://") ||
		strings.HasPrefix(lowerURL, "wss://")

	var db *sql.DB
	var err error

	if isMySQL {
		db, err = sql.Open("mysql", targetURL)
		if err != nil {
			return fmt.Errorf("sql.Open(mysql) error: %w", err)
		}
	} else if isRemote && authToken != "" {
		connector, connErr := libsql.NewConnector(targetURL, libsql.WithAuthToken(authToken))
		if connErr != nil {
			return fmt.Errorf("libsql NewConnector error: %w", connErr)
		}
		db = sql.OpenDB(connector)
	} else if isRemote {
		db, err = sql.Open("libsql", targetURL)
		if err != nil {
			return fmt.Errorf("sql.Open(libsql) error: %w", err)
		}
	} else {
		// Mở trực tiếp bằng Standard SQL40 Engine (database/sql + modernc.org/sqlite thuần chuẩn SQL)
		cleanPath := strings.TrimPrefix(targetURL, "file://")
		cleanPath = strings.TrimPrefix(cleanPath, "file:")
		db, err = sql.Open("sqlite", cleanPath)
		if err != nil {
			return fmt.Errorf("sql.Open(sqlite/sql40) error: %w", err)
		}
	}

	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(15 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if !isRemote {
		pragmas := []string{
			"PRAGMA journal_mode=WAL;",
			"PRAGMA synchronous=NORMAL;",
			"PRAGMA foreign_keys=ON;",
			"PRAGMA busy_timeout=5000;",
			"PRAGMA temp_store=MEMORY;",
			"PRAGMA cache_size=-32000;",
		}
		for _, p := range pragmas {
			if _, err := db.ExecContext(ctx, p); err != nil {
				_ = db.Close()
				return fmt.Errorf("sql40 pragma error (%s): %w", p, err)
			}
		}
	} else {
		var one int
		if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
			_ = db.Close()
			return fmt.Errorf("remote sql ping error: %w", err)
		}
	}

	if s.db != nil {
		_ = s.db.Close()
	}
	s.db = db
	s.dbURL = targetURL
	s.authToken = authToken
	s.isRemote = isRemote
	s.isMySQL = isMySQL
	return nil
}

func (s *LibSQLStore) Reconfigure(newURL string, newAuthToken string) error {
	newURL = strings.TrimSpace(newURL)
	if newURL == "" {
		newURL = "file:" + filepath.ToSlash(s.dbFile)
	}
	if err := s.connectInternal(newURL, strings.TrimSpace(newAuthToken)); err != nil {
		return err
	}
	if err := s.initSchema(); err != nil {
		return err
	}
	if strings.HasPrefix(strings.ToLower(newURL), "mysql://") && s.dataDir != "" {
		_ = SaveEncryptedCloudSQLConfig(s.dataDir, newURL)
	}
	return nil
}

func (s *LibSQLStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		err := s.db.Close()
		s.db = nil
		return err
	}
	return nil
}

func (s *LibSQLStore) initSchema() error {
	s.mu.RLock()
	db := s.db
	isMySQL := s.isMySQL
	s.mu.RUnlock()
	if db == nil {
		return errors.New("sql database is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if isMySQL {
		var existingTables, existingOwnerCols int
		errFastCheck := db.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(1) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('muse_accounts','mart_users','chat_threads','chat_messages','oauth_sessions','routing_telemetry','muse_documents','muse_memories','muse_tasks')),
				(SELECT COUNT(1) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name IN ('chat_threads','muse_documents','muse_memories','muse_tasks') AND column_name = 'owner_id')
		`).Scan(&existingTables, &existingOwnerCols)
		if errFastCheck == nil && existingTables == 9 && existingOwnerCols == 4 {
			s.invalidateStatusCache()
			return nil
		}

		mysqlDDLs := []string{
			`CREATE TABLE IF NOT EXISTS muse_accounts (
				id VARCHAR(191) PRIMARY KEY,
				node_index BIGINT NOT NULL DEFAULT 1,
				label VARCHAR(255) NOT NULL DEFAULT '',
				plan_tier VARCHAR(128) NOT NULL DEFAULT 'Muse.ai',
				auth_type VARCHAR(64) NOT NULL DEFAULT 'cookie',
				upstream_url VARCHAR(512) NOT NULL DEFAULT 'https://muse.ai',
				proxy_url VARCHAR(512) NOT NULL DEFAULT '',
				device_profile VARCHAR(512) NOT NULL DEFAULT '',
				encrypted_credential LONGTEXT NOT NULL,
				fingerprint VARCHAR(128) NOT NULL DEFAULT '',
				has_hatch_sess INT NOT NULL DEFAULT 0,
				has_hatch_gw INT NOT NULL DEFAULT 0,
				status VARCHAR(64) NOT NULL DEFAULT 'unavailable',
				active_requests BIGINT NOT NULL DEFAULT 0,
				tokens_remaining BIGINT NOT NULL DEFAULT 0,
				tokens_quota BIGINT NOT NULL DEFAULT 0,
				tokens_used BIGINT NOT NULL DEFAULT 0,
				total_requests BIGINT NOT NULL DEFAULT 0,
				fail_count BIGINT NOT NULL DEFAULT 0,
				latency_ms BIGINT NOT NULL DEFAULT 0,
				last_used_at VARCHAR(64) NOT NULL DEFAULT '',
				status_detail VARCHAR(1024) NOT NULL DEFAULT '',
				updated_at VARCHAR(64) NOT NULL DEFAULT '',
				INDEX idx_muse_accounts_status (status)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,
			`CREATE TABLE IF NOT EXISTS mart_users (
				id VARCHAR(191) PRIMARY KEY,
				username VARCHAR(191) NOT NULL UNIQUE,
				display_name VARCHAR(255) NOT NULL DEFAULT '',
				password_hash VARCHAR(512) NOT NULL,
				role VARCHAR(64) NOT NULL DEFAULT 'user',
				is_disabled INT NOT NULL DEFAULT 0,
				created_at VARCHAR(64) NOT NULL DEFAULT '',
				last_login_at VARCHAR(64) NOT NULL DEFAULT '',
				INDEX idx_mart_users_username (username)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,
			`CREATE TABLE IF NOT EXISTS chat_threads (
				id VARCHAR(191) PRIMARY KEY,
				owner_id VARCHAR(191) NOT NULL DEFAULT '',
				title VARCHAR(512) NOT NULL DEFAULT '',
				model VARCHAR(128) NOT NULL DEFAULT '',
				workspace VARCHAR(64) NOT NULL DEFAULT 'chat',
				created_at VARCHAR(64) NOT NULL DEFAULT '',
				updated_at VARCHAR(64) NOT NULL DEFAULT '',
				INDEX idx_chat_threads_updated (updated_at),
				INDEX idx_chat_threads_owner_updated (owner_id, updated_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,
			`CREATE TABLE IF NOT EXISTS chat_messages (
				id VARCHAR(191) PRIMARY KEY,
				thread_id VARCHAR(191) NOT NULL,
				seq_num INT NOT NULL DEFAULT 0,
				role VARCHAR(64) NOT NULL DEFAULT '',
				content LONGTEXT NOT NULL,
				attachments_json LONGTEXT NOT NULL,
				node_label VARCHAR(255) NOT NULL DEFAULT '',
				model VARCHAR(128) NOT NULL DEFAULT '',
				latency_ms BIGINT NOT NULL DEFAULT 0,
				tokens BIGINT NOT NULL DEFAULT 0,
				timestamp VARCHAR(64) NOT NULL DEFAULT '',
				INDEX idx_chat_messages_thread_seq (thread_id, seq_num)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,
			`CREATE TABLE IF NOT EXISTS oauth_sessions (
				session_id VARCHAR(191) PRIMARY KEY,
				login_url VARCHAR(2048) NOT NULL DEFAULT '',
				state VARCHAR(255) NOT NULL DEFAULT '',
				app_id VARCHAR(191) NOT NULL DEFAULT '',
				redirect_uri VARCHAR(1024) NOT NULL DEFAULT '',
				cookies_json LONGTEXT NOT NULL,
				created_at BIGINT NOT NULL DEFAULT 0
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,
			`CREATE TABLE IF NOT EXISTS routing_telemetry (
				id BIGINT PRIMARY KEY AUTO_INCREMENT,
				thread_id VARCHAR(191) NOT NULL DEFAULT '',
				node_id VARCHAR(191) NOT NULL DEFAULT '',
				node_label VARCHAR(255) NOT NULL DEFAULT '',
				model VARCHAR(128) NOT NULL DEFAULT '',
				latency_ms BIGINT NOT NULL DEFAULT 0,
				tokens_used BIGINT NOT NULL DEFAULT 0,
				status VARCHAR(128) NOT NULL DEFAULT 'ok',
				created_at VARCHAR(64) NOT NULL DEFAULT '',
				INDEX idx_routing_telemetry_created (created_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,
			`CREATE TABLE IF NOT EXISTS muse_documents (
				id VARCHAR(191) PRIMARY KEY,
				owner_id VARCHAR(191) NOT NULL DEFAULT '',
				title VARCHAR(512) NOT NULL DEFAULT '',
				content LONGTEXT NOT NULL,
				source_thread_id VARCHAR(191) NOT NULL DEFAULT '',
				updated_at VARCHAR(64) NOT NULL DEFAULT '',
				INDEX idx_muse_documents_updated (updated_at),
				INDEX idx_muse_documents_owner_updated (owner_id, updated_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,
			`CREATE TABLE IF NOT EXISTS muse_memories (
				id VARCHAR(191) PRIMARY KEY,
				owner_id VARCHAR(191) NOT NULL DEFAULT '',
				title VARCHAR(512) NOT NULL DEFAULT '',
				content LONGTEXT NOT NULL,
				is_active INT NOT NULL DEFAULT 1,
				created_at VARCHAR(64) NOT NULL DEFAULT '',
				INDEX idx_muse_memories_owner (owner_id)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,
			`CREATE TABLE IF NOT EXISTS muse_tasks (
				id VARCHAR(191) PRIMARY KEY,
				owner_id VARCHAR(191) NOT NULL DEFAULT '',
				title VARCHAR(512) NOT NULL DEFAULT '',
				prompt LONGTEXT NOT NULL,
				status VARCHAR(64) NOT NULL DEFAULT 'pending',
				assigned_node_id VARCHAR(191) NOT NULL DEFAULT '',
				result_summary VARCHAR(4096) NOT NULL DEFAULT '',
				updated_at VARCHAR(64) NOT NULL DEFAULT '',
				INDEX idx_muse_tasks_status (status, updated_at),
				INDEX idx_muse_tasks_owner_status (owner_id, status, updated_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`,
		}
		for _, ddl := range mysqlDDLs {
			if _, err := db.ExecContext(ctx, ddl); err != nil {
				return fmt.Errorf("mysql schema init error: %w", err)
			}
		}
		_, _ = db.ExecContext(ctx, `ALTER TABLE chat_threads ADD COLUMN owner_id VARCHAR(191) NOT NULL DEFAULT '';`)
		_, _ = db.ExecContext(ctx, `ALTER TABLE muse_documents ADD COLUMN owner_id VARCHAR(191) NOT NULL DEFAULT '';`)
		_, _ = db.ExecContext(ctx, `ALTER TABLE muse_memories ADD COLUMN owner_id VARCHAR(191) NOT NULL DEFAULT '';`)
		_, _ = db.ExecContext(ctx, `ALTER TABLE muse_tasks ADD COLUMN owner_id VARCHAR(191) NOT NULL DEFAULT '';`)
		return nil
	}

	ddlStatements := []string{
		`CREATE TABLE IF NOT EXISTS muse_accounts (
			id TEXT PRIMARY KEY,
			node_index INTEGER NOT NULL DEFAULT 1,
			label TEXT NOT NULL,
			plan_tier TEXT NOT NULL DEFAULT 'Muse.ai',
			auth_type TEXT NOT NULL DEFAULT 'cookie',
			upstream_url TEXT NOT NULL DEFAULT 'https://muse.ai',
			proxy_url TEXT NOT NULL DEFAULT '',
			device_profile TEXT NOT NULL DEFAULT '',
			encrypted_credential TEXT NOT NULL,
			fingerprint TEXT NOT NULL DEFAULT '',
			has_hatch_sess INTEGER NOT NULL DEFAULT 0,
			has_hatch_gw INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'unavailable',
			active_requests INTEGER NOT NULL DEFAULT 0,
			tokens_remaining INTEGER NOT NULL DEFAULT 0,
			tokens_quota INTEGER NOT NULL DEFAULT 0,
			tokens_used INTEGER NOT NULL DEFAULT 0,
			total_requests INTEGER NOT NULL DEFAULT 0,
			fail_count INTEGER NOT NULL DEFAULT 0,
			latency_ms INTEGER NOT NULL DEFAULT 0,
			last_used_at TEXT NOT NULL DEFAULT '',
			status_detail TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);`,
		`CREATE INDEX IF NOT EXISTS idx_muse_accounts_status ON muse_accounts(status);`,
		`CREATE TABLE IF NOT EXISTS mart_users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE COLLATE NOCASE,
			display_name TEXT NOT NULL DEFAULT '',
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user',
			is_disabled INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			last_login_at TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_mart_users_username ON mart_users(username);`,
		`CREATE TABLE IF NOT EXISTS chat_threads (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			workspace TEXT NOT NULL DEFAULT 'chat',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_chat_threads_updated ON chat_threads(updated_at DESC);`,
		`CREATE TABLE IF NOT EXISTS chat_messages (
			id TEXT PRIMARY KEY,
			thread_id TEXT NOT NULL,
			seq_num INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			attachments_json TEXT NOT NULL DEFAULT '[]',
			node_label TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			latency_ms INTEGER NOT NULL DEFAULT 0,
			tokens INTEGER NOT NULL DEFAULT 0,
			timestamp TEXT NOT NULL,
			FOREIGN KEY (thread_id) REFERENCES chat_threads(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_chat_messages_thread_seq ON chat_messages(thread_id, seq_num);`,
		`CREATE TABLE IF NOT EXISTS oauth_sessions (
			session_id TEXT PRIMARY KEY,
			login_url TEXT NOT NULL,
			state TEXT NOT NULL DEFAULT '',
			app_id TEXT NOT NULL DEFAULT '',
			redirect_uri TEXT NOT NULL DEFAULT '',
			cookies_json TEXT NOT NULL DEFAULT '{}',
			created_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS routing_telemetry (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			thread_id TEXT NOT NULL DEFAULT '',
			node_id TEXT NOT NULL DEFAULT '',
			node_label TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			latency_ms INTEGER NOT NULL DEFAULT 0,
			tokens_used INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'ok',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);`,
		`CREATE INDEX IF NOT EXISTS idx_routing_telemetry_created ON routing_telemetry(created_at DESC);`,
		// -------------------------------------------------------
		// Bảng Tài liệu & Artifacts (📄 panel Tài liệu)
		// -------------------------------------------------------
		`CREATE TABLE IF NOT EXISTS muse_documents (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			source_thread_id TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);`,
		`CREATE INDEX IF NOT EXISTS idx_muse_documents_updated ON muse_documents(updated_at DESC);`,
		// -------------------------------------------------------
		// Bảng Bộ nhớ & Prompt mẫu (💡 panel Ý tưởng)
		// -------------------------------------------------------
		`CREATE TABLE IF NOT EXISTS muse_memories (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			is_active INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);`,
		// -------------------------------------------------------
		// Bảng Nhiệm vụ & Hàng đợi lệnh (☑️ panel Nhiệm vụ)
		// -------------------------------------------------------
		`CREATE TABLE IF NOT EXISTS muse_tasks (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL,
			prompt TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			assigned_node_id TEXT NOT NULL DEFAULT '',
			result_summary TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);`,
		`CREATE INDEX IF NOT EXISTS idx_muse_tasks_status ON muse_tasks(status, updated_at DESC);`,
	}

	for _, ddl := range ddlStatements {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("libsql schema init error: %w", err)
		}
	}

	// Tự động nâng cấp bảng hiện có (thêm cột proxy_url, device_profile và owner_id cô lập theo từng tài khoản)
	_, _ = db.ExecContext(ctx, `ALTER TABLE muse_accounts ADD COLUMN proxy_url TEXT NOT NULL DEFAULT '';`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE muse_accounts ADD COLUMN device_profile TEXT NOT NULL DEFAULT '';`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE chat_threads ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE muse_documents ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE muse_memories ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';`)
	_, _ = db.ExecContext(ctx, `ALTER TABLE muse_tasks ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';`)
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_chat_threads_owner_updated ON chat_threads(owner_id, updated_at DESC);`)
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_muse_documents_owner_updated ON muse_documents(owner_id, updated_at DESC);`)
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_muse_memories_owner ON muse_memories(owner_id);`)
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_muse_tasks_owner_status ON muse_tasks(owner_id, status, updated_at DESC);`)
	return nil
}

// MigrateLegacyJSONIfNeeded tự động chuyển dữ liệu từ accounts_vault.json & sessions_store.json / threads.json
// sang cơ sở dữ liệu chuẩn libSQL nếu bảng trong libSQL đang trống.
func (s *LibSQLStore) MigrateLegacyJSONIfNeeded(vaultJSONPath string, threadsJSONPaths ...string) {
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var accountCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM muse_accounts`).Scan(&accountCount); err == nil && accountCount == 0 {
		if raw, err := os.ReadFile(vaultJSONPath); err == nil && len(raw) > 0 {
			var legacyNodes []*MuseAccountNode
			if json.Unmarshal(raw, &legacyNodes) == nil && len(legacyNodes) > 0 {
				for _, n := range legacyNodes {
					if n != nil && n.ID != "" && n.EncryptedCredential != "" {
						_ = s.UpsertAccount(n)
					}
				}
				LogInfo("Migrated %d Muse accounts from legacy JSON into libSQL (%s)", len(legacyNodes), s.dbFile)
			}
		}
	}

	var threadCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM chat_threads`).Scan(&threadCount); err == nil && threadCount == 0 {
		for _, tPath := range threadsJSONPaths {
			if tPath == "" {
				continue
			}
			if raw, err := os.ReadFile(tPath); err == nil && len(raw) > 0 {
				var legacyThreads []*ChatThread
				if json.Unmarshal(raw, &legacyThreads) == nil && len(legacyThreads) > 0 {
					for _, t := range legacyThreads {
						if t != nil && t.ID != "" {
							_ = s.SaveThreadWithMessages(t)
						}
					}
					LogInfo("Migrated %d threads from legacy JSON (%s) into libSQL (%s)", len(legacyThreads), tPath, s.dbFile)
					break
				}
			}
		}
	}
}

// LoadAccounts đọc toàn bộ danh sách tài khoản Muse.ai từ bảng muse_accounts trong libSQL.
func (s *LibSQLStore) LoadAccounts() ([]*MuseAccountNode, error) {
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return nil, errors.New("libsql db not open")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx, `
		SELECT id, node_index, label, plan_tier, auth_type, upstream_url,
		       COALESCE(proxy_url, ''), COALESCE(device_profile, ''),
		       encrypted_credential, fingerprint, has_hatch_sess, has_hatch_gw,
		       status, tokens_remaining, tokens_quota, tokens_used,
		       total_requests, fail_count, latency_ms, last_used_at, status_detail
		FROM muse_accounts
		ORDER BY node_index ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []*MuseAccountNode
	for rows.Next() {
		n := &MuseAccountNode{}
		var hasSessInt, hasGwInt int
		if err := rows.Scan(
			&n.ID,
			&n.NodeIndex,
			&n.Label,
			&n.PlanTier,
			&n.AuthType,
			&n.UpstreamURL,
			&n.ProxyURL,
			&n.DeviceProfile,
			&n.EncryptedCredential,
			&n.Fingerprint,
			&hasSessInt,
			&hasGwInt,
			&n.Status,
			&n.TokensRemaining,
			&n.TokensQuota,
			&n.TokensUsed,
			&n.TotalRequests,
			&n.FailCount,
			&n.LatencyMs,
			&n.LastUsedAt,
			&n.StatusDetail,
		); err != nil {
			return nil, err
		}
		n.HasHatchSess = hasSessInt != 0
		n.HasHatchGw = hasGwInt != 0
		if n.Status == "running" {
			n.Status = "ready"
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// SyncAllAccounts đồng bộ danh sách tài khoản hiện tại vào bảng muse_accounts trong libSQL.
func (s *LibSQLStore) SyncAllAccounts(nodes []*MuseAccountNode) error {
	for _, n := range nodes {
		if err := s.UpsertAccount(n); err != nil {
			return err
		}
	}
	return nil
}

// UpsertAccount thêm mới hoặc cập nhật tài khoản Muse.ai vào bảng muse_accounts trong libSQL.
func (s *LibSQLStore) UpsertAccount(n *MuseAccountNode) error {
	if n == nil || n.ID == "" {
		return errors.New("invalid account node")
	}
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return errors.New("libsql db not open")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hasSess := 0
	if n.HasHatchSess {
		hasSess = 1
	}
	hasGw := 0
	if n.HasHatchGw {
		hasGw = 1
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO muse_accounts (
			id, node_index, label, plan_tier, auth_type, upstream_url,
			proxy_url, device_profile,
			encrypted_credential, fingerprint, has_hatch_sess, has_hatch_gw,
			status, active_requests, tokens_remaining, tokens_quota, tokens_used,
			total_requests, fail_count, latency_ms, last_used_at, status_detail, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET
			node_index = excluded.node_index,
			label = excluded.label,
			plan_tier = excluded.plan_tier,
			auth_type = excluded.auth_type,
			upstream_url = excluded.upstream_url,
			proxy_url = excluded.proxy_url,
			device_profile = excluded.device_profile,
			encrypted_credential = excluded.encrypted_credential,
			fingerprint = excluded.fingerprint,
			has_hatch_sess = excluded.has_hatch_sess,
			has_hatch_gw = excluded.has_hatch_gw,
			status = excluded.status,
			active_requests = excluded.active_requests,
			tokens_remaining = excluded.tokens_remaining,
			tokens_quota = excluded.tokens_quota,
			tokens_used = excluded.tokens_used,
			total_requests = excluded.total_requests,
			fail_count = excluded.fail_count,
			latency_ms = excluded.latency_ms,
			last_used_at = excluded.last_used_at,
			status_detail = excluded.status_detail,
			updated_at = datetime('now')
	`,
		n.ID,
		n.NodeIndex,
		n.Label,
		n.PlanTier,
		n.AuthType,
		n.UpstreamURL,
		n.ProxyURL,
		n.DeviceProfile,
		n.EncryptedCredential,
		n.Fingerprint,
		hasSess,
		hasGw,
		n.Status,
		n.ActiveRequests,
		n.TokensRemaining,
		n.TokensQuota,
		n.TokensUsed,
		n.TotalRequests,
		n.FailCount,
		n.LatencyMs,
		n.LastUsedAt,
		n.StatusDetail,
	)
	if err == nil {
		s.invalidateStatusCache()
	}
	return err
}

// DeleteAccount xóa tài khoản khỏi bảng muse_accounts trong libSQL.
func (s *LibSQLStore) DeleteAccount(nodeID string) error {
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return errors.New("libsql db not open")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := db.ExecContext(ctx, `DELETE FROM muse_accounts WHERE id = ?`, nodeID)
	if err == nil {
		s.invalidateStatusCache()
	}
	return err
}

// LoadThreadsWithMessages tải toàn bộ lịch sử hội thoại và tin nhắn từ libSQL.
func (s *LibSQLStore) LoadThreadsWithMessages() ([]*ChatThread, error) {
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return nil, errors.New("libsql db not open")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	tRows, err := db.QueryContext(ctx, `
		SELECT id, COALESCE(owner_id, ''), title, model, workspace, created_at, updated_at
		FROM chat_threads
		ORDER BY updated_at DESC
		LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer tRows.Close()

	var threads []*ChatThread
	threadIdxMap := make(map[string]int)
	for tRows.Next() {
		t := &ChatThread{}
		if err := tRows.Scan(&t.ID, &t.OwnerID, &t.Title, &t.Model, &t.Workspace, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.Messages = []ChatMessage{}
		threadIdxMap[t.ID] = len(threads)
		threads = append(threads, t)
	}
	if err := tRows.Err(); err != nil {
		return nil, err
	}

	if len(threads) == 0 {
		return []*ChatThread{}, nil
	}

	mRows, err := db.QueryContext(ctx, `
		SELECT thread_id, role, content, attachments_json, node_label,
		       model, latency_ms, tokens, timestamp
		FROM chat_messages
		ORDER BY thread_id ASC, seq_num ASC
	`)
	if err != nil {
		return threads, nil
	}
	defer mRows.Close()

	for mRows.Next() {
		var m ChatMessage
		var threadID string
		var attJSON string
		if err := mRows.Scan(
			&threadID,
			&m.Role,
			&m.Content,
			&attJSON,
			&m.NodeLabel,
			&m.Model,
			&m.LatencyMs,
			&m.Tokens,
			&m.Timestamp,
		); err != nil {
			continue
		}
		if attJSON != "" && attJSON != "[]" {
			_ = json.Unmarshal([]byte(attJSON), &m.Attachments)
		}
		if idx, ok := threadIdxMap[threadID]; ok {
			threads[idx].Messages = append(threads[idx].Messages, m)
		}
	}

	return threads, nil
}

// SaveThreadWithMessages lưu đồng bộ một luồng hội thoại và các tin nhắn vào libSQL trong một Transaction.
func (s *LibSQLStore) SaveThreadWithMessages(t *ChatThread) error {
	if t == nil || t.ID == "" {
		return nil
	}
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return errors.New("libsql db not open")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO chat_threads (id, owner_id, title, model, workspace, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			owner_id = CASE WHEN chat_threads.owner_id = '' THEN excluded.owner_id ELSE chat_threads.owner_id END,
			title = excluded.title,
			model = excluded.model,
			workspace = excluded.workspace,
			updated_at = excluded.updated_at
	`, t.ID, t.OwnerID, t.Title, t.Model, t.Workspace, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return err
	}

	for idx, m := range t.Messages {
		attBytes, _ := json.Marshal(m.Attachments)
		if len(attBytes) == 0 {
			attBytes = []byte("[]")
		}
		msgID := fmt.Sprintf("%s_msg_%d", t.ID, idx)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO chat_messages (
				id, thread_id, seq_num, role, content, attachments_json,
				node_label, model, latency_ms, tokens, timestamp
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				seq_num = excluded.seq_num,
				role = excluded.role,
				content = excluded.content,
				attachments_json = excluded.attachments_json,
				node_label = excluded.node_label,
				model = excluded.model,
				latency_ms = excluded.latency_ms,
				tokens = excluded.tokens,
				timestamp = excluded.timestamp
		`,
			msgID,
			t.ID,
			idx,
			m.Role,
			m.Content,
			string(attBytes),
			m.NodeLabel,
			m.Model,
			m.LatencyMs,
			m.Tokens,
			m.Timestamp,
		)
		if err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateStatusCache()
	return nil
}

// DeleteThread xóa luồng hội thoại và toàn bộ tin nhắn liên quan khỏi libSQL.
func (s *LibSQLStore) DeleteThread(threadID string) error {
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return errors.New("libsql db not open")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _ = db.ExecContext(ctx, `DELETE FROM chat_messages WHERE thread_id = ?`, threadID)
	_, err := db.ExecContext(ctx, `DELETE FROM chat_threads WHERE id = ?`, threadID)
	if err == nil {
		s.invalidateStatusCache()
	}
	return err
}

// SaveOAuthSession lưu phiên OAuth Link vào bảng oauth_sessions trong libSQL.
func (s *LibSQLStore) SaveOAuthSession(sessionID, loginURL, state, appID, redirectURI string) error {
	if sessionID == "" || loginURL == "" {
		return nil
	}
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	_, err := db.ExecContext(ctx, `
		INSERT INTO oauth_sessions (session_id, login_url, state, app_id, redirect_uri, cookies_json, created_at)
		VALUES (?, ?, ?, ?, ?, '{}', ?)
		ON CONFLICT(session_id) DO UPDATE SET
			login_url = excluded.login_url,
			state = excluded.state,
			app_id = excluded.app_id,
			redirect_uri = excluded.redirect_uri,
			created_at = excluded.created_at
	`, sessionID, loginURL, state, appID, redirectURI, time.Now().Unix())
	return err
}

// RecordRoutingTelemetry ghi nhật ký định tuyến thực tế vào bảng routing_telemetry trong libSQL.
func (s *LibSQLStore) RecordRoutingTelemetry(threadID, nodeID, nodeLabel, model string, latencyMs, tokensUsed int64, status string) {
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	_, _ = db.ExecContext(ctx, `
		INSERT INTO routing_telemetry (thread_id, node_id, node_label, model, latency_ms, tokens_used, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
	`, threadID, nodeID, nodeLabel, model, latencyMs, tokensUsed, status)
}

// GetStatus trả về thông tin chi tiết của Engine SQL40 / MySQL đang chạy (tối ưu gộp 1 round-trip + L1 Cache 3s).
func (s *LibSQLStore) GetStatus() LibSQLStatusResponse {
	s.mu.RLock()
	db := s.db
	dbURL := s.dbURL
	dbFile := s.dbFile
	isRemote := s.isRemote
	isMySQL := s.isMySQL
	s.mu.RUnlock()

	driverName := "sql40-standard (database/sql + modernc.org/sqlite)"
	if isMySQL {
		driverName = "sql40-mysql (database/sql + Aiven MySQL TLS 1.2+)"
	}

	if db == nil {
		return LibSQLStatusResponse{
			Connected:    false,
			Driver:       driverName,
			DatabaseURL:  maskDatabaseURL(dbURL),
			DatabaseFile: dbFile,
			IsRemote:     isRemote,
			Tables:       []LibSQLTableStat{},
			CheckedAt:    time.Now().Format("15:04:05 02/01/2006"),
		}
	}

	s.statusMu.RLock()
	if s.cachedStatus != nil && time.Now().Before(s.cachedStatusExp) {
		cachedCopy := *s.cachedStatus
		s.statusMu.RUnlock()
		return cachedCopy
	}
	s.statusMu.RUnlock()

	resp := LibSQLStatusResponse{
		Connected:    true,
		Driver:       driverName,
		DatabaseURL:  maskDatabaseURL(dbURL),
		DatabaseFile: dbFile,
		IsRemote:     isRemote,
		Tables:       make([]LibSQLTableStat, 0, 9),
		CheckedAt:    time.Now().Format("15:04:05 02/01/2006"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var dbVer string
	var cUsers, cAccounts, cThreads, cMessages, cDocs, cMemories, cTasks, cOAuth, cTelemetry int64

	// Gộp toàn bộ phiên bản Engine và số lượng bản ghi của 9 bảng vào 1 câu truy vấn duy nhất (1 round-trip mạng)
	batchStatusSQL := `
		SELECT sqlite_version(),
		       (SELECT COUNT(1) FROM mart_users),
		       (SELECT COUNT(1) FROM muse_accounts),
		       (SELECT COUNT(1) FROM chat_threads),
		       (SELECT COUNT(1) FROM chat_messages),
		       (SELECT COUNT(1) FROM muse_documents),
		       (SELECT COUNT(1) FROM muse_memories),
		       (SELECT COUNT(1) FROM muse_tasks),
		       (SELECT COUNT(1) FROM oauth_sessions),
		       (SELECT COUNT(1) FROM routing_telemetry)
	`
	if err := db.QueryRowContext(ctx, batchStatusSQL).Scan(
		&dbVer,
		&cUsers,
		&cAccounts,
		&cThreads,
		&cMessages,
		&cDocs,
		&cMemories,
		&cTasks,
		&cOAuth,
		&cTelemetry,
	); err == nil {
		if isMySQL {
			resp.EngineVersion = "SQL40 Standard (MySQL " + dbVer + " TLS)"
		} else {
			resp.EngineVersion = "SQL40 Standard (SQLite " + dbVer + ")"
		}
		resp.Tables = []LibSQLTableStat{
			{TableName: "mart_users", RowCount: cUsers},
			{TableName: "muse_accounts", RowCount: cAccounts},
			{TableName: "chat_threads", RowCount: cThreads},
			{TableName: "chat_messages", RowCount: cMessages},
			{TableName: "muse_documents", RowCount: cDocs},
			{TableName: "muse_memories", RowCount: cMemories},
			{TableName: "muse_tasks", RowCount: cTasks},
			{TableName: "oauth_sessions", RowCount: cOAuth},
			{TableName: "routing_telemetry", RowCount: cTelemetry},
		}
	} else {
		resp.EngineVersion = "SQL40 Standard Embedded"
		tableNames := []string{
			"mart_users",
			"muse_accounts",
			"chat_threads",
			"chat_messages",
			"muse_documents",
			"muse_memories",
			"muse_tasks",
			"oauth_sessions",
			"routing_telemetry",
		}
		for _, tbl := range tableNames {
			var cnt int64
			_ = db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(1) FROM %s", tbl)).Scan(&cnt)
			resp.Tables = append(resp.Tables, LibSQLTableStat{
				TableName: tbl,
				RowCount:  cnt,
			})
		}
	}

	if isMySQL {
		resp.JournalMode = "WAL"
	} else {
		var journalMode string
		if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err == nil {
			resp.JournalMode = strings.ToUpper(journalMode)
		}
		if resp.JournalMode == "" {
			resp.JournalMode = "WAL"
		}
	}

	if !isRemote && dbFile != "" {
		if st, err := os.Stat(dbFile); err == nil {
			resp.FileSizeBytes = st.Size()
		}
	}

	s.statusMu.Lock()
	s.cachedStatus = &resp
	s.cachedStatusExp = time.Now().Add(3 * time.Second)
	s.statusMu.Unlock()

	return resp
}

func validateSafeWebSQL(rawSQL string) error {
	trimmed := strings.TrimSpace(rawSQL)
	if trimmed == "" {
		return errors.New("câu lệnh SQL không được để trống")
	}
	upper := strings.ToUpper(trimmed)
	forbidden := []string{
		"DROP TABLE",
		"DROP DATABASE",
		"ATTACH DATABASE",
		"DETACH DATABASE",
		"PRAGMA WRITABLE_SCHEMA",
	}
	for _, f := range forbidden {
		if strings.Contains(upper, f) {
			return fmt.Errorf("từ khóa SQL %q bị chặn để bảo vệ an toàn dữ liệu hệ thống", f)
		}
	}
	return nil
}

// ExecuteSQL thực thi câu lệnh SQL có tham số trực tiếp trên kết nối libSQL.
func (s *LibSQLStore) ExecuteSQL(rawSQL string, args []interface{}) (*LibSQLQueryResult, error) {
	if err := validateSafeWebSQL(rawSQL); err != nil {
		return nil, err
	}

	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return nil, errors.New("cơ sở dữ liệu libSQL chưa được khởi tạo")
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	trimmedUpper := strings.ToUpper(strings.TrimSpace(rawSQL))
	isQuery := strings.HasPrefix(trimmedUpper, "SELECT") ||
		strings.HasPrefix(trimmedUpper, "PRAGMA") ||
		strings.HasPrefix(trimmedUpper, "WITH") ||
		strings.HasPrefix(trimmedUpper, "EXPLAIN")

	res := &LibSQLQueryResult{
		Columns:        []string{},
		Rows:           [][]interface{}{},
		DatabaseDriver: "libsql",
	}

	if isQuery {
		rows, err := db.QueryContext(ctx, rawSQL, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		cols, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		res.Columns = cols

		for rows.Next() {
			vals := make([]interface{}, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return nil, err
			}
			rowOut := make([]interface{}, len(cols))
			for i, v := range vals {
				if b, ok := v.([]byte); ok {
					sVal := string(b)
					if (cols[i] == "encrypted_credential" || cols[i] == "encrypted_secret") && len(sVal) > 28 {
						sVal = sVal[:28] + "... (AES-256-CTR)"
					}
					rowOut[i] = sVal
				} else if sVal, ok := v.(string); ok {
					if (cols[i] == "encrypted_credential" || cols[i] == "encrypted_secret") && len(sVal) > 28 {
						sVal = sVal[:28] + "... (AES-256-CTR)"
					}
					rowOut[i] = sVal
				} else {
					rowOut[i] = v
				}
			}
			res.Rows = append(res.Rows, rowOut)
		}
		res.ExecutionTimeMs = time.Since(start).Milliseconds()
		return res, rows.Err()
	}

	execRes, err := db.ExecContext(ctx, rawSQL, args...)
	if err != nil {
		return nil, err
	}
	res.RowsAffected, _ = execRes.RowsAffected()
	res.LastInsertRowID, _ = execRes.LastInsertId()
	res.ExecutionTimeMs = time.Since(start).Milliseconds()
	s.invalidateStatusCache()
	return res, nil
}

// ExecuteHranaPipeline xử lý giao thức chuẩn libSQL Hrana v2 HTTP Pipeline (/v2/pipeline)
// giúp trình duyệt Web có thể dùng trực tiếp chuẩn giao thức libSQL Client.
func (s *LibSQLStore) ExecuteHranaPipeline(req HranaPipelineRequest) HranaPipelineResponse {
	results := make([]HranaStreamResult, 0, len(req.Requests))

	for _, item := range req.Requests {
		switch item.Type {
		case "close":
			results = append(results, HranaStreamResult{
				Type: "ok",
				Response: &HranaStreamResponse{
					Type: "close",
				},
			})
		case "execute":
			if item.Stmt == nil {
				results = append(results, HranaStreamResult{
					Type: "error",
					Error: &HranaErrorObject{
						Message: "Missing stmt in execute request",
						Code:    "SQL_INPUT_ERROR",
					},
				})
				continue
			}
			args := make([]interface{}, 0, len(item.Stmt.Args))
			for _, a := range item.Stmt.Args {
				if a.Type == "null" {
					args = append(args, nil)
				} else {
					args = append(args, a.Value)
				}
			}
			qRes, err := s.ExecuteSQL(item.Stmt.Sql, args)
			if err != nil {
				results = append(results, HranaStreamResult{
					Type: "error",
					Error: &HranaErrorObject{
						Message: err.Error(),
						Code:    "SQLITE_ERROR",
					},
				})
				continue
			}

			hrCols := make([]HranaCol, 0, len(qRes.Columns))
			for _, cName := range qRes.Columns {
				hrCols = append(hrCols, HranaCol{Name: cName})
			}

			hrRows := make([][]HranaArg, 0, len(qRes.Rows))
			for _, r := range qRes.Rows {
				hrRow := make([]HranaArg, 0, len(r))
				for _, cell := range r {
					switch v := cell.(type) {
					case nil:
						hrRow = append(hrRow, HranaArg{Type: "null"})
					case int64:
						hrRow = append(hrRow, HranaArg{Type: "integer", Value: fmt.Sprintf("%d", v)})
					case int:
						hrRow = append(hrRow, HranaArg{Type: "integer", Value: fmt.Sprintf("%d", v)})
					case float64:
						hrRow = append(hrRow, HranaArg{Type: "float", Value: v})
					default:
						hrRow = append(hrRow, HranaArg{Type: "text", Value: fmt.Sprintf("%v", v)})
					}
				}
				hrRows = append(hrRows, hrRow)
			}

			lastRowStr := fmt.Sprintf("%d", qRes.LastInsertRowID)
			results = append(results, HranaStreamResult{
				Type: "ok",
				Response: &HranaStreamResponse{
					Type: "execute",
					Result: &HranaStmtResult{
						Cols:             hrCols,
						Rows:             hrRows,
						AffectedRowCount: qRes.RowsAffected,
						LastInsertRowid:  &lastRowStr,
					},
				},
			})
		default:
			results = append(results, HranaStreamResult{
				Type: "ok",
				Response: &HranaStreamResponse{
					Type: item.Type,
				},
			})
		}
	}

	return HranaPipelineResponse{
		Baton:   nil,
		BaseUrl: nil,
		Results: results,
	}
}

// SyncFromStore đồng bộ toàn bộ dữ liệu từ một kho SQL nguồn (ví dụ: SQLite local) lên kho SQL đích (ví dụ: Aiven MySQL Cloud).
func (s *LibSQLStore) SyncFromStore(src *LibSQLStore) (map[string]int, error) {
	if s == nil || src == nil {
		return nil, errors.New("null sql store")
	}
	stats := make(map[string]int)

	// 1. Đồng bộ muse_accounts
	nodes, err := src.LoadAccounts()
	if err != nil {
		return stats, fmt.Errorf("load src accounts: %w", err)
	}
	for _, n := range nodes {
		if err := s.UpsertAccount(n); err != nil {
			return stats, fmt.Errorf("upsert account %s: %w", n.ID, err)
		}
		stats["muse_accounts"]++
	}

	// 2. Đồng bộ chat_threads & chat_messages
	threads, err := src.LoadThreadsWithMessages()
	if err != nil {
		return stats, fmt.Errorf("load src threads: %w", err)
	}
	for _, t := range threads {
		if err := s.SaveThreadWithMessages(t); err != nil {
			return stats, fmt.Errorf("save thread %s: %w", t.ID, err)
		}
		stats["chat_threads"]++
		stats["chat_messages"] += len(t.Messages)
	}

	// 3. Đồng bộ mart_users
	if res, err := src.ExecuteSQL("SELECT id, username, display_name, password_hash, role, is_disabled, created_at, last_login_at FROM mart_users", nil); err == nil {
		for _, r := range queryRowsToMaps(res) {
			isDis, _ := strconv.Atoi(r["is_disabled"])
			_, errExec := s.ExecuteSQL(
				`INSERT INTO mart_users (id, username, display_name, password_hash, role, is_disabled, created_at, last_login_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(id) DO UPDATE SET
				   display_name = excluded.display_name,
				   password_hash = excluded.password_hash,
				   role = excluded.role,
				   is_disabled = excluded.is_disabled,
				   last_login_at = excluded.last_login_at`,
				[]interface{}{r["id"], r["username"], r["display_name"], r["password_hash"], r["role"], isDis, r["created_at"], r["last_login_at"]},
			)
			if errExec == nil {
				stats["mart_users"]++
			}
		}
	}

	// 4. Đồng bộ muse_documents
	if res, err := src.ExecuteSQL("SELECT id, owner_id, title, content, source_thread_id, updated_at FROM muse_documents", nil); err == nil {
		for _, r := range queryRowsToMaps(res) {
			_, errExec := s.ExecuteSQL(
				`INSERT INTO muse_documents (id, owner_id, title, content, source_thread_id, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?)
				 ON CONFLICT(id) DO UPDATE SET
				   owner_id = excluded.owner_id,
				   title = excluded.title,
				   content = excluded.content,
				   source_thread_id = excluded.source_thread_id,
				   updated_at = excluded.updated_at`,
				[]interface{}{r["id"], r["owner_id"], r["title"], r["content"], r["source_thread_id"], r["updated_at"]},
			)
			if errExec == nil {
				stats["muse_documents"]++
			}
		}
	}

	// 5. Đồng bộ muse_memories
	if res, err := src.ExecuteSQL("SELECT id, owner_id, title, content, is_active, created_at FROM muse_memories", nil); err == nil {
		for _, r := range queryRowsToMaps(res) {
			isAct, _ := strconv.Atoi(r["is_active"])
			_, errExec := s.ExecuteSQL(
				`INSERT INTO muse_memories (id, owner_id, title, content, is_active, created_at)
				 VALUES (?, ?, ?, ?, ?, ?)
				 ON CONFLICT(id) DO UPDATE SET
				   owner_id = excluded.owner_id,
				   title = excluded.title,
				   content = excluded.content,
				   is_active = excluded.is_active`,
				[]interface{}{r["id"], r["owner_id"], r["title"], r["content"], isAct, r["created_at"]},
			)
			if errExec == nil {
				stats["muse_memories"]++
			}
		}
	}

	// 6. Đồng bộ muse_tasks
	if res, err := src.ExecuteSQL("SELECT id, owner_id, title, prompt, status, assigned_node_id, result_summary, updated_at FROM muse_tasks", nil); err == nil {
		for _, r := range queryRowsToMaps(res) {
			_, errExec := s.ExecuteSQL(
				`INSERT INTO muse_tasks (id, owner_id, title, prompt, status, assigned_node_id, result_summary, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(id) DO UPDATE SET
				   owner_id = excluded.owner_id,
				   title = excluded.title,
				   prompt = excluded.prompt,
				   status = excluded.status,
				   result_summary = excluded.result_summary,
				   updated_at = excluded.updated_at`,
				[]interface{}{r["id"], r["owner_id"], r["title"], r["prompt"], r["status"], r["assigned_node_id"], r["result_summary"], r["updated_at"]},
			)
			if errExec == nil {
				stats["muse_tasks"]++
			}
		}
	}

	return stats, nil
}
