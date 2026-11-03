package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const LogPrefix = "[ENGINE]"

// SanitizeLog loại bỏ hoàn toàn \r, \n, \x00 chống CRLF Log Injection (PHAN 3)
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

// ValidateAndSanitizeInput kiểm tra SQLi, XSS, null bytes, path traversal tại Go Engine (PHAN 3)
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

// ConstantTimeStringEquals sử dụng subtle.ConstantTimeCompare chống Timing Attack (PHAN 3)
func ConstantTimeStringEquals(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ============================================================================
// L1 RAM CACHE VỚI TTL (PHAN 7 BAT BUOC)
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
// JWT BẤT ĐỐI XỨNG RS256 (PHAN 3 BAT BUOC - TUYET DOI CAM HS256)
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
	Sub string `json:"sub"`
	Iss string `json:"iss"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

func NewRS256JWTManager(cache *TTLCache) (*RS256JWTManager, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	mgr := &RS256JWTManager{
		privateKey: priv,
		publicKey:  &priv.PublicKey,
		cache:      cache,
	}
	// Cache JWT Public Key trong L1 Cache với TTL >= 60s (PHAN 7)
	cache.Set("jwt_rs256_public_key", &priv.PublicKey, 1*time.Hour)
	return mgr, nil
}

func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func base64URLDecode(str string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(str)
}

func (m *RS256JWTManager) IssueToken(subject string, ttl time.Duration) (string, error) {
	hdrBytes, err := json.Marshal(jwtHeader{Alg: "RS256", Typ: "JWT"})
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	claimsBytes, err := json.Marshal(jwtClaims{
		Sub: subject,
		Iss: "muse_router_engine",
		Iat: now,
		Exp: now + int64(ttl.Seconds()),
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
	// Bắt buộc kiểm tra đúng thuật toán RS256, chống downgrade attack sang HS256/none
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
// GO -> RUST CORE FFI BRIDGE + DEFER FREE_RUST_STRING (PHAN 2 & PHAN 7)
// ============================================================================

type RustCoreBridge struct {
	dll                     *syscall.LazyDLL
	procEncryptSecret       *syscall.LazyProc
	procDecryptSecret       *syscall.LazyProc
	procComputeFingerprint  *syscall.LazyProc
	procConstantTimeCompare *syscall.LazyProc
	procFreeRustString      *syscall.LazyProc
	masterKey               string
}

func NewRustCoreBridge(masterKey string) (*RustCoreBridge, error) {
	candidates := []string{
		filepath.Join("..", "muse_router_core", "muse_router_core.dll"),
		filepath.Join("muse_router_core", "muse_router_core.dll"),
		filepath.Join("..", "muse_router_core", "target", "release", "muse_router_core.dll"),
		"muse_router_core.dll",
	}
	var selected string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, errAbs := filepath.Abs(c)
			if errAbs == nil {
				selected = abs
			} else {
				selected = c
			}
			break
		}
	}
	if selected == "" {
		return nil, errors.New("ERR_RUST_DLL_NOT_FOUND")
	}

	dll := syscall.NewLazyDLL(selected)
	bridge := &RustCoreBridge{
		dll:                     dll,
		procEncryptSecret:       dll.NewProc("core_encrypt_secret"),
		procDecryptSecret:       dll.NewProc("core_decrypt_secret"),
		procComputeFingerprint:  dll.NewProc("core_compute_token_fingerprint"),
		procConstantTimeCompare: dll.NewProc("core_constant_time_compare"),
		procFreeRustString:      dll.NewProc("free_rust_string"),
		masterKey:               masterKey,
	}
	LogInfo("Connected Go Engine FFI to Rust Core DLL at %s", selected)
	return bridge, nil
}

func cStringBytes(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

func ptrToGoString(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	var bytes []byte
	for i := uintptr(0); i < 65536; i++ {
		b := *(*byte)(unsafe.Pointer(ptr + i))
		if b == 0 {
			break
		}
		bytes = append(bytes, b)
	}
	return string(bytes)
}

func (r *RustCoreBridge) freeRustString(ptr uintptr) {
	if ptr != 0 {
		_, _, _ = r.procFreeRustString.Call(ptr)
	}
}

func (r *RustCoreBridge) EncryptSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", errors.New("ERR_EMPTY_SECRET")
	}
	pBytes := cStringBytes(plaintext)
	kBytes := cStringBytes(r.masterKey)

	retPtr, _, _ := r.procEncryptSecret.Call(
		uintptr(unsafe.Pointer(&pBytes[0])),
		uintptr(unsafe.Pointer(&kBytes[0])),
	)
	if retPtr == 0 {
		return "", errors.New("ERR_RUST_ENCRYPT_FAILED")
	}
	// PHAN 7 BAT BUOC: Chủ động gọi defer free_rust_string sau khi nhận kết quả từ Rust
	defer r.freeRustString(retPtr)

	return ptrToGoString(retPtr), nil
}

func (r *RustCoreBridge) DecryptSecret(encrypted string) (string, error) {
	if encrypted == "" {
		return "", errors.New("ERR_EMPTY_ENCRYPTED_SECRET")
	}
	eBytes := cStringBytes(encrypted)
	kBytes := cStringBytes(r.masterKey)

	retPtr, _, _ := r.procDecryptSecret.Call(
		uintptr(unsafe.Pointer(&eBytes[0])),
		uintptr(unsafe.Pointer(&kBytes[0])),
	)
	if retPtr == 0 {
		return "", errors.New("ERR_RUST_DECRYPT_FAILED")
	}
	// PHAN 7 BAT BUOC: Chủ động gọi defer free_rust_string
	defer r.freeRustString(retPtr)

	return ptrToGoString(retPtr), nil
}

func (r *RustCoreBridge) ComputeFingerprint(secret string) string {
	if secret == "" {
		return "fp_empty"
	}
	sBytes := cStringBytes(secret)
	retPtr, _, _ := r.procComputeFingerprint.Call(uintptr(unsafe.Pointer(&sBytes[0])))
	if retPtr == 0 {
		return "fp_unknown"
	}
	defer r.freeRustString(retPtr)
	return ptrToGoString(retPtr)
}

// ============================================================================
// SECURITY HEADERS & EXACT CORS MIDDLEWARE (PHAN 3 BAT BUOC)
// ============================================================================

var allowedOrigins = map[string]bool{
	"http://127.0.0.1:8088": true,
	"http://localhost:8088": true,
}

func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ẩn header Server và X-Powered-By
		w.Header().Del("Server")
		w.Header().Del("X-Powered-By")

		// Các Security Headers bắt buộc theo PHAN 3
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'")
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		// CORS Exact String Match (TUYỆT ĐỐI CẤM wildcard *)
		origin := r.Header.Get("Origin")
		if origin != "" {
			if allowedOrigins[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Idempotency-Key")
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
	_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"%s"}`, SanitizeLog(errCode))))
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
