package main

import "fmt"

// Vault is the Go-native authenticated secret store. The wire format remains
// compatible with the legacy implementation so existing account records keep
// working after the migration.
type Vault struct{ masterKey string }

func NewVault(masterKey string) (*Vault, error) {
	if masterKey == "" {
		return nil, fmt.Errorf("MUSE_VAULT_MASTER_KEY is required")
	}
	return &Vault{masterKey: masterKey}, nil
}

func (v *Vault) EncryptSecret(value string) (string, error) {
	return encryptSecretNative(value, v.masterKey)
}

func (v *Vault) DecryptSecret(value string) (string, error) {
	return decryptSecretNative(value, v.masterKey)
}

func (v *Vault) ComputeFingerprint(value string) string {
	return computeFingerprintNative(value)
}

func (v *Vault) ConstantTimeCompare(a, b string) bool {
	return ConstantTimeStringEquals(a, b)
}
