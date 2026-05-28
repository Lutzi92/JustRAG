package authhandler

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

// genTestKey returns a 32-byte base64-encoded key for use in tests that
// exercise the secret-encryption helpers.
func genTestKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	t.Setenv(authProviderSecretEnvKey, genTestKey(t))

	plaintext := "super-secret-client-credential-#42"
	enc, err := EncryptProviderSecret(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(enc, secretEncryptedPrefix) {
		t.Fatalf("expected enc: prefix, got %q", enc)
	}
	if enc == plaintext {
		t.Fatalf("expected ciphertext != plaintext")
	}

	dec, err := DecryptProviderSecret(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != plaintext {
		t.Fatalf("round-trip mismatch: got %q, want %q", dec, plaintext)
	}
}

func TestEncryptIsIdempotentOnAlreadyEncrypted(t *testing.T) {
	t.Setenv(authProviderSecretEnvKey, genTestKey(t))

	enc1, err := EncryptProviderSecret("hello")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	enc2, err := EncryptProviderSecret(enc1)
	if err != nil {
		t.Fatalf("re-encrypt: %v", err)
	}
	if enc1 != enc2 {
		t.Fatalf("re-encrypting an already-enc: value should be a no-op (got different ciphertext)")
	}
}

func TestDecryptPassThroughForPlaintext(t *testing.T) {
	// Decrypt without prefix returns the value verbatim — supports legacy
	// plaintext rows that haven't been rotated yet.
	got, err := DecryptProviderSecret("legacy-plaintext")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "legacy-plaintext" {
		t.Fatalf("expected pass-through, got %q", got)
	}
}

func TestEncryptFailsWithoutKey(t *testing.T) {
	t.Setenv(authProviderSecretEnvKey, "")
	if _, err := EncryptProviderSecret("hello"); err == nil {
		t.Fatalf("expected error when %s is unset", authProviderSecretEnvKey)
	}
}

func TestEncryptFailsWithWrongKeyLength(t *testing.T) {
	// 16-byte key — valid base64 but wrong length.
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	t.Setenv(authProviderSecretEnvKey, short)
	if _, err := EncryptProviderSecret("hello"); err == nil {
		t.Fatalf("expected length validation error")
	}
}

func TestDecryptFailsForCorruptedCiphertext(t *testing.T) {
	t.Setenv(authProviderSecretEnvKey, genTestKey(t))
	enc, err := EncryptProviderSecret("something")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// Flip a base64 character so the AEAD verification fails.
	tampered := enc[:len(enc)-2] + "AA"
	if _, err := DecryptProviderSecret(tampered); err == nil {
		t.Fatalf("expected tampered ciphertext to fail")
	}
}
