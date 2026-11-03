//go:build windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

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
		LogInfo("Rust Core DLL not found locally; using native FIPS 197 AES-256-CTR + HMAC-SHA256 vault engine")
		return &RustCoreBridge{masterKey: masterKey}, nil
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
	if ptr != 0 && r.procFreeRustString != nil {
		_, _, _ = r.procFreeRustString.Call(ptr)
	}
}

func (r *RustCoreBridge) EncryptSecret(plaintext string) (string, error) {
	if r.dll == nil || r.procEncryptSecret == nil {
		return encryptSecretNative(plaintext, r.masterKey)
	}
	if plaintext == "" {
		return encryptSecretNative(plaintext, r.masterKey)
	}
	pBytes := cStringBytes(plaintext)
	kBytes := cStringBytes(r.masterKey)

	retPtr, _, _ := r.procEncryptSecret.Call(
		uintptr(unsafe.Pointer(&pBytes[0])),
		uintptr(unsafe.Pointer(&kBytes[0])),
	)
	if retPtr == 0 {
		return encryptSecretNative(plaintext, r.masterKey)
	}
	// PHAN 7 BAT BUOC: Chủ động gọi defer free_rust_string sau khi nhận kết quả từ Rust
	defer r.freeRustString(retPtr)

	return ptrToGoString(retPtr), nil
}

func (r *RustCoreBridge) DecryptSecret(encrypted string) (string, error) {
	if r.dll == nil || r.procDecryptSecret == nil {
		return decryptSecretNative(encrypted, r.masterKey)
	}
	if encrypted == "" {
		return decryptSecretNative(encrypted, r.masterKey)
	}
	eBytes := cStringBytes(encrypted)
	kBytes := cStringBytes(r.masterKey)

	retPtr, _, _ := r.procDecryptSecret.Call(
		uintptr(unsafe.Pointer(&eBytes[0])),
		uintptr(unsafe.Pointer(&kBytes[0])),
	)
	if retPtr == 0 {
		return decryptSecretNative(encrypted, r.masterKey)
	}
	// PHAN 7 BAT BUOC: Chủ động gọi defer free_rust_string
	defer r.freeRustString(retPtr)

	return ptrToGoString(retPtr), nil
}

func (r *RustCoreBridge) ComputeFingerprint(secret string) string {
	if r.dll == nil || r.procComputeFingerprint == nil {
		return computeFingerprintNative(secret)
	}
	if secret == "" {
		return "fp_empty"
	}
	sBytes := cStringBytes(secret)
	retPtr, _, _ := r.procComputeFingerprint.Call(uintptr(unsafe.Pointer(&sBytes[0])))
	if retPtr == 0 {
		return computeFingerprintNative(secret)
	}
	defer r.freeRustString(retPtr)
	return ptrToGoString(retPtr)
}
