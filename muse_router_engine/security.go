package main

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const LogPrefix = "[ENGINE]"

// SanitizeLog loáº¡i bá» hoÃ n toÃ n \r, \n, \x00 chá»‘ng CRLF Log Injection (PHAN 3)
func SanitizeLog(input string) string {
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\x00", "")
	return replacer.Replace(input)
}

func LogInfo(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "%s %s [INFO] %s\n", LogPrefix, time.Now().Format("15:04:05"), SanitizeLog(msg))
}

func LogWarn(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "%s %s [WARN] %s\n", LogPrefix, time.Now().Format("15:04:05"), SanitizeLog(msg))
}

func LogError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "%s %s [ERROR] %s\n", LogPrefix, time.Now().Format("15:04:05"), SanitizeLog(msg))
}

// ValidateAndSanitizeInput kiá»ƒm tra SQLi, XSS, null bytes, path traversal táº¡i Go Engine (PHAN 3)
func ValidateAndSanitizeInput(raw string, maxLen int) (string, error) {
	if len(raw) > maxLen {
		return "", errors.New("ERR_INPUT_TOO_LARGE")
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", errors.New("ERR_NULL_BYTE_DETECTED")
	}
	lower := strings.ToLower(raw)
	forbiddenPatterns := []string{
		"<script",
		"javascript:",
		"onerror=",
		"onload=",
		"../",
		"..\\",
		"union select",
		"drop table",
		";--",
	}
	for _, pattern := range forbiddenPatterns {
		if strings.Contains(lower, pattern) {
			return "", errors.New("ERR_MALICIOUS_INPUT_DETECTED")
		}
	}
	return strings.TrimSpace(raw), nil
}

// ConstantTimeStringEquals sá»­ dá»¥ng subtle.ConstantTimeCompare chá»‘ng Timing Attack (PHAN 3)
func ConstantTimeStringEquals(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ============================================================================
// L1 RAM CACHE Vá»šI TTL (PHAN 7 BAT BUOC)
// ============================================================================

type cacheItem struct {
	value     interface{}
	expiresAt int64
}

type TTLCache struct {
	store sync.Map
}

func NewTTLCache() *TTLCache {
	return &TTLCache{}
}

func (c *TTLCache) Set(key string, value interface{}, ttl time.Duration) {
	c.store.Store(key, cacheItem{
		value:     value,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}

func (c *TTLCache) Get(key string) (interface{}, bool) {
	val, ok := c.store.Load(key)
	if !ok {
		return nil, false
	}
	item, ok := val.(cacheItem)
	if !ok {
		return nil, false
	}
	if time.Now().UnixNano() > item.expiresAt {
		c.store.Delete(key)
		return nil, false
	}
	return item.value, true
}

// ============================================================================
// JWT Báº¤T Äá»I Xá»¨NG RS256 (PHAN 3 BAT BUOC - TUYET DOI CAM HS256)
// ============================================================================

type RS256JWTManager struct {
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	cache      *TTLCache
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type jwtClaims struct {
	Sub      string `json:"sub"`
	Username string `json:"username,omitempty"`
	Name     string `json:"name,omitempty"`
	Role     string `json:"role,omitempty"`
	Iss      string `json:"iss"`
	Iat      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
}

func NewRS256JWTManager(cache *TTLCache) (*RS256JWTManager, error) {
	return NewRS256JWTManagerWithKeyFile(cache, "", "")
}

func NewRS256JWTManagerWithKeyFile(cache *TTLCache, keyPath string, masterKey string) (*RS256JWTManager, error) {
	var priv *rsa.PrivateKey
	if keyPath != "" && masterKey != "" {
		if rawEnc, errRead := os.ReadFile(keyPath); errRead == nil && len(rawEnc) > 0 {
			if pemStr, errDec := decryptSecretNative(strings.TrimSpace(string(rawEnc)), masterKey); errDec == nil {
				if blk, _ := pem.Decode([]byte(pemStr)); blk != nil {
					if parsed, errParse := x509.ParsePKCS1PrivateKey(blk.Bytes); errParse == nil {
						priv = parsed
					}
				}
			}
		}
	}
	if priv == nil {
		var err error
		priv, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		if keyPath != "" && masterKey != "" {
			_ = os.MkdirAll(filepath.Dir(keyPath), 0750)
			pemBytes := pem.EncodeToMemory(&pem.Block{
				Type:  "RSA PRIVATE KEY",
				Bytes: x509.MarshalPKCS1PrivateKey(priv),
			})
			if enc, errEnc := encryptSecretNative(string(pemBytes), masterKey); errEnc == nil {
				_ = os.WriteFile(keyPath, []byte(enc), 0600)
			}
		}
	}
	mgr := &RS256JWTManager{
		privateKey: priv,
		publicKey:  &priv.PublicKey,
		cache:      cache,
	}
	// Cache JWT Public Key trong L1 Cache vá»›i TTL >= 60s (PHAN 7)
	cache.Set("jwt_rs256_public_key", &priv.PublicKey, 1*time.Hour)
	return mgr, nil
}

// SyncWithSQLStore Ä‘á»“ng bá»™ khÃ³a kÃ½ JWT RS256 (Ä‘Ã£ mÃ£ hÃ³a AES-256-CTR+HMAC-SHA256) lÃªn cÆ¡ sá»Ÿ dá»¯ liá»‡u SQL40 / Aiven MySQL
// Ä‘á»ƒ cÃ¡c phiÃªn Ä‘Äƒng nháº­p trÃªn Render Cloud khÃ´ng bá»‹ máº¥t hiá»‡u lá»±c khi container khá»Ÿi Ä‘á»™ng láº¡i.
func (m *RS256JWTManager) SyncWithSQLStore(store *MySQLStore, masterKey string) {
	if m == nil || store == nil || masterKey == "" {
		return
	}
	const jwtKeyRowID = "__system_jwt_rs256_key__"
	res, err := store.ExecuteSQL(
		"SELECT cookies_json FROM oauth_sessions WHERE session_id = ? LIMIT 1",
		[]interface{}{jwtKeyRowID},
	)
	if err == nil && res != nil && len(res.Rows) > 0 && len(res.Rows[0]) > 0 {
		encStr := strings.TrimSpace(fmt.Sprintf("%v", res.Rows[0][0]))
		if pemStr, errDec := decryptSecretNative(encStr, masterKey); errDec == nil {
			if blk, _ := pem.Decode([]byte(pemStr)); blk != nil {
				if parsed, errParse := x509.ParsePKCS1PrivateKey(blk.Bytes); errParse == nil {
					m.privateKey = parsed
					m.publicKey = &parsed.PublicKey
					m.cache.Set("jwt_rs256_public_key", &parsed.PublicKey, 1*time.Hour)
					return
				}
			}
		}
	}

	if m.privateKey == nil {
		return
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(m.privateKey),
	})
	enc, errEnc := encryptSecretNative(string(pemBytes), masterKey)
	if errEnc != nil {
		return
	}
	_, _ = store.ExecuteSQL(
		`INSERT INTO oauth_sessions (session_id, login_url, state, app_id, redirect_uri, cookies_json, created_at)
		 VALUES (?, 'system://jwt_rs256', 'active', 'mart_ai', '', ?, ?)
		 ON DUPLICATE KEY UPDATE
		   cookies_json = VALUES(cookies_json),
		   created_at = VALUES(created_at)`,
		[]interface{}{jwtKeyRowID, enc, time.Now().Unix()},
	)
}

func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func base64URLDecode(str string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(str)
}

func (m *RS256JWTManager) IssueToken(subject string, ttl time.Duration) (string, error) {
	return m.IssueUserToken(subject, subject, subject, "admin", ttl)
}

func (m *RS256JWTManager) IssueUserToken(userID, username, displayName, role string, ttl time.Duration) (string, error) {
	hdrBytes, err := json.Marshal(jwtHeader{Alg: "RS256", Typ: "JWT"})
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	claimsBytes, err := json.Marshal(jwtClaims{
		Sub:      userID,
		Username: username,
		Name:     displayName,
		Role:     role,
		Iss:      "muse_router_engine",
		Iat:      now,
		Exp:      now + int64(ttl.Seconds()),
	})
	if err != nil {
		return "", err
	}

	signingInput := base64URLEncode(hdrBytes) + "." + base64URLEncode(claimsBytes)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, m.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64URLEncode(signature), nil
}

func (m *RS256JWTManager) VerifyToken(token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("ERR_INVALID_JWT_FORMAT")
	}

	hdrBytes, err := base64URLDecode(parts[0])
	if err != nil {
		return nil, errors.New("ERR_INVALID_JWT_HEADER")
	}
	var hdr jwtHeader
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil {
		return nil, errors.New("ERR_INVALID_JWT_HEADER_JSON")
	}
	// Báº¯t buá»™c kiá»ƒm tra Ä‘Ãºng thuáº­t toÃ¡n RS256, chá»‘ng downgrade attack sang HS256/none
	if hdr.Alg != "RS256" {
		return nil, errors.New("ERR_UNSUPPORTED_JWT_ALG")
	}

	signingInput := parts[0] + "." + parts[1]
	sigBytes, err := base64URLDecode(parts[2])
	if err != nil {
		return nil, errors.New("ERR_INVALID_JWT_SIGNATURE")
	}

	pubKey := m.publicKey
	if cached, ok := m.cache.Get("jwt_rs256_public_key"); ok {
		if pk, valid := cached.(*rsa.PublicKey); valid {
			pubKey = pk
		}
	}

	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, digest[:], sigBytes); err != nil {
		return nil, errors.New("ERR_JWT_SIGNATURE_MISMATCH")
	}

	claimsBytes, err := base64URLDecode(parts[1])
	if err != nil {
		return nil, errors.New("ERR_INVALID_JWT_CLAIMS")
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil, errors.New("ERR_INVALID_JWT_CLAIMS_JSON")
	}
	if time.Now().Unix() > claims.Exp {
		return nil, errors.New("ERR_JWT_EXPIRED")
	}
	return &claims, nil
}

// ============================================================================
// MÃƒ HÃ“A CHUáº¨N FIPS 197 AES-256-CTR + HMAC-SHA256 TÆ¯Æ NG THÃCH 100% Vá»šI RUST CORE
// ============================================================================

func aes256CtrCryptNative(data []byte, key [32]byte, nonce [12]byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	var iv [16]byte
	copy(iv[:12], nonce[:])
	binary.BigEndian.PutUint32(iv[12:16], 1) // Counter báº¯t Ä‘áº§u tá»« 1 giá»‘ng há»‡t muse_router_core/src/lib.rs
	out := make([]byte, len(data))
	stream := cipher.NewCTR(block, iv[:])
	stream.XORKeyStream(out, data)
	return out, nil
}

func hmacSha256Native(key []byte, message []byte) [32]byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(message)
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}

func encryptSecretNative(plaintext string, masterKey string) (string, error) {
	if plaintext == "" {
		return "", errors.New("ERR_EMPTY_SECRET")
	}
	if masterKey == "" {
		return "", errors.New("ERR_EMPTY_MASTER_KEY")
	}
	encKey := sha256.Sum256([]byte("ENC:" + masterKey))
	macKey := sha256.Sum256([]byte("MAC:" + masterKey))

	var randSeed [16]byte
	_, _ = rand.Read(randSeed[:])
	binary.LittleEndian.PutUint64(randSeed[:8], uint64(time.Now().UnixNano()))
	seedBuf := append(randSeed[:], []byte(masterKey)...)
	seedBuf = append(seedBuf, []byte(plaintext)...)
	seedDigest := sha256.Sum256(seedBuf)
	var nonce [12]byte
	copy(nonce[:], seedDigest[:12])

	ciphertext, err := aes256CtrCryptNative([]byte(plaintext), encKey, nonce)
	if err != nil {
		return "", err
	}
	authPayload := make([]byte, 0, 12+len(ciphertext))
	authPayload = append(authPayload, nonce[:]...)
	authPayload = append(authPayload, ciphertext...)
	tag := hmacSha256Native(macKey[:], authPayload)

	return fmt.Sprintf(
		"v1:%s:%s:%s",
		hex.EncodeToString(nonce[:]),
		hex.EncodeToString(ciphertext),
		hex.EncodeToString(tag[:]),
	), nil
}

func decryptSecretNative(encrypted string, masterKey string) (string, error) {
	if encrypted == "" {
		return "", errors.New("ERR_EMPTY_ENCRYPTED_SECRET")
	}
	parts := strings.Split(encrypted, ":")
	if len(parts) != 4 || parts[0] != "v1" {
		return "", errors.New("ERR_INVALID_ENCRYPTED_FORMAT")
	}
	nonceSlice, err := hex.DecodeString(parts[1])
	if err != nil || len(nonceSlice) != 12 {
		return "", errors.New("ERR_INVALID_NONCE")
	}
	ciphertext, err := hex.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("ERR_INVALID_CIPHERTEXT")
	}
	expectedTag, err := hex.DecodeString(parts[3])
	if err != nil || len(expectedTag) != 32 {
		return "", errors.New("ERR_INVALID_TAG")
	}

	encKey := sha256.Sum256([]byte("ENC:" + masterKey))
	macKey := sha256.Sum256([]byte("MAC:" + masterKey))

	authPayload := make([]byte, 0, 12+len(ciphertext))
	authPayload = append(authPayload, nonceSlice...)
	authPayload = append(authPayload, ciphertext...)
	computedTag := hmacSha256Native(macKey[:], authPayload)

	if subtle.ConstantTimeCompare(computedTag[:], expectedTag) != 1 {
		return "", errors.New("ERR_RUST_DECRYPT_FAILED")
	}

	var nonce [12]byte
	copy(nonce[:], nonceSlice)
	plainBytes, err := aes256CtrCryptNative(ciphertext, encKey, nonce)
	if err != nil {
		return "", err
	}
	return string(plainBytes), nil
}

func computeFingerprintNative(secret string) string {
	if secret == "" {
		return "fp_empty"
	}
	digest := sha256.Sum256([]byte(secret))
	return "fp_" + hex.EncodeToString(digest[:])[:16]
}

// ============================================================================
// PBKDF2-HMAC-SHA256 PASSWORD HASHING & CONSTANT-TIME VERIFY (PHAN 3 BAT BUOC)
// ============================================================================

const pbkdf2Iterations = 120000

func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	numBlocks := (keyLen + sha256.Size - 1) / sha256.Size
	dk := make([]byte, 0, numBlocks*sha256.Size)
	var blockBuf [4]byte
	prf := hmac.New(sha256.New, password)
	u := make([]byte, sha256.Size)
	t := make([]byte, sha256.Size)
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		_, _ = prf.Write(salt)
		binary.BigEndian.PutUint32(blockBuf[:], uint32(block))
		_, _ = prf.Write(blockBuf[:])
		u = prf.Sum(u[:0])
		copy(t, u)
		for i := 2; i <= iter; i++ {
			prf.Reset()
			_, _ = prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

func HashPasswordPBKDF2(password string) (string, error) {
	if len(password) < 6 {
		return "", errors.New("ERR_PASSWORD_TOO_SHORT")
	}
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	dk := pbkdf2SHA256([]byte(password), salt[:], pbkdf2Iterations, 32)
	return fmt.Sprintf(
		"pbkdf2_sha256$%d$%s$%s",
		pbkdf2Iterations,
		hex.EncodeToString(salt[:]),
		hex.EncodeToString(dk),
	), nil
}

func VerifyPasswordPBKDF2(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 10000 || iter > 1000000 {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil || len(salt) < 8 {
		return false
	}
	expectedDK, err := hex.DecodeString(parts[3])
	if err != nil || len(expectedDK) != 32 {
		return false
	}
	computedDK := pbkdf2SHA256([]byte(password), salt, iter, 32)
	return subtle.ConstantTimeCompare(computedDK, expectedDK) == 1
}

// ============================================================================
// SECURITY HEADERS & EXACT CORS MIDDLEWARE (PHAN 3 BAT BUOC)
// ============================================================================

var allowedOrigins = map[string]bool{
	"http://127.0.0.1:8088":        true,
	"http://localhost:8088":        true,
	"https://mart-ai.onrender.com": true,
	"http://mart-ai.onrender.com":  true,
	// Extension Chrome "Mart AI" (ID cá»‘ Ä‘á»‹nh nhá» trÆ°á»ng "key" trong manifest.json)
	"chrome-extension://mjlkhmjhbhkjfccffeoimoindodmglaj": true,
}

func isAllowedCORSOrigin(origin string, host string) bool {
	if allowedOrigins[origin] {
		return true
	}
	renderURL := strings.TrimRight(strings.TrimSpace(os.Getenv("RENDER_EXTERNAL_URL")), "/")
	if renderURL != "" && ConstantTimeStringEquals(origin, renderURL) {
		return true
	}
	cleanHost := strings.TrimSpace(host)
	if cleanHost != "" {
		if ConstantTimeStringEquals(origin, "https://"+cleanHost) || ConstantTimeStringEquals(origin, "http://"+cleanHost) {
			return true
		}
	}
	return false
}

func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// áº¨n header Server vÃ  X-Powered-By
		w.Header().Del("Server")
		w.Header().Del("X-Powered-By")

		// CÃ¡c Security Headers báº¯t buá»™c theo PHAN 3
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data: https: blob:; connect-src 'self' ws: wss: https:;")
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		// CORS Exact String Match (TUYá»†T Äá»I Cáº¤M wildcard *)
		origin := r.Header.Get("Origin")
		if origin != "" {
			if isAllowedCORSOrigin(origin, r.Host) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Idempotency-Key, X-Mart-Client, X-Mart-Ext-Relay, Accept, Cache-Control, Last-Event-ID")
				w.Header().Set("Access-Control-Expose-Headers", "Content-Type, Retry-After, X-Accel-Buffering")
			} else {
				LogWarn("Blocked disallowed CORS origin: %s", origin)
				WriteJSONError(w, http.StatusForbidden, "ERR_CORS_ORIGIN_FORBIDDEN")
				return
			}
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func WriteJSONError(w http.ResponseWriter, status int, errCode string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	body, err := json.Marshal(map[string]string{"error": SanitizeLog(errCode)})
	if err != nil {
		_, _ = w.Write([]byte(`{"error":"ERR_INTERNAL"}`))
		return
	}
	_, _ = w.Write(body)
}

func WriteJSONResponse(w http.ResponseWriter, status int, payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		LogError("JSON marshal error: %v", err)
		WriteJSONError(w, http.StatusInternalServerError, "ERR_INTERNAL_SERIALIZATION")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
