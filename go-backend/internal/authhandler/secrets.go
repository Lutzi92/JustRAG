package authhandler

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// secretEncryptedPrefix marks values that have already been encrypted with the
// configured AES-GCM key. Detecting an existing prefix lets the write path
// stay idempotent — admins can PATCH a provider without re-supplying the
// client_secret, and we won't double-encrypt the masked placeholder.
const secretEncryptedPrefix = "enc:"

// authProviderSecretEnvKey is the env var holding the base64-encoded 32-byte
// AES-256-GCM key used to wrap auth-provider secrets at rest — OIDC
// client_secret and LDAP bindCredentials. Required once any OIDC row exists or
// any LDAP provider is created/updated with bind credentials. Intentionally
// env-only: keeping the key out of the database means a DB-only leak (backup,
// replica, injection) does not also yield the plaintext secret.
const authProviderSecretEnvKey = "AUTH_PROVIDER_SECRET_KEY"

// secretEncryptionKey lazily loads the 32-byte key from env. Called once per
// encrypt/decrypt; keep allocation cheap.
func secretEncryptionKey() ([]byte, error) {
	raw := os.Getenv(authProviderSecretEnvKey)
	if raw == "" {
		return nil, fmt.Errorf("%s is not set; required to encrypt OIDC client_secret", authProviderSecretEnvKey)
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", authProviderSecretEnvKey, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s must decode to exactly 32 bytes (got %d)", authProviderSecretEnvKey, len(key))
	}
	return key, nil
}

// EncryptProviderSecret wraps a plaintext secret with AES-256-GCM, returning
// the `enc:` prefix + base64 ciphertext form persisted in auth_providers.config.
// If plaintext already carries the prefix it is returned unchanged so PATCH
// requests that omit the field don't re-encrypt the masked placeholder.
func EncryptProviderSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if strings.HasPrefix(plaintext, secretEncryptedPrefix) {
		return plaintext, nil
	}
	key, err := secretEncryptionKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return secretEncryptedPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// DecryptProviderSecret reverses EncryptProviderSecret. Values without the
// `enc:` prefix are returned verbatim so freshly imported / legacy plaintext
// values keep working until they're rotated.
func DecryptProviderSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, secretEncryptedPrefix) {
		return value, nil
	}
	key, err := secretEncryptionKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	sealed, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, secretEncryptedPrefix))
	if err != nil {
		return "", fmt.Errorf("decrypt provider secret: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(sealed) < nonceSize {
		return "", errors.New("decrypt provider secret: ciphertext shorter than nonce")
	}
	nonce, ct := sealed[:nonceSize], sealed[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt provider secret: %w", err)
	}
	return string(plaintext), nil
}
