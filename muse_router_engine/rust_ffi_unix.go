//go:build !windows

package main

type RustCoreBridge struct {
	masterKey string
}

func NewRustCoreBridge(masterKey string) (*RustCoreBridge, error) {
	LogInfo("Initialized cross-platform FIPS 197 AES-256-CTR + HMAC-SHA256 vault engine (Linux/Cloud runtime)")
	return &RustCoreBridge{
		masterKey: masterKey,
	}, nil
}

func (r *RustCoreBridge) EncryptSecret(plaintext string) (string, error) {
	return encryptSecretNative(plaintext, r.masterKey)
}

func (r *RustCoreBridge) DecryptSecret(encrypted string) (string, error) {
	return decryptSecretNative(encrypted, r.masterKey)
}

func (r *RustCoreBridge) ComputeFingerprint(secret string) string {
	return computeFingerprintNative(secret)
}
