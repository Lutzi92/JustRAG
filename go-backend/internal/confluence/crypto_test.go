package confluence

import (
	"strings"
	"testing"
)

const testSecret = "test-jwt-secret"

func TestEncryptDecryptRoundtrip(t *testing.T) {
	plaintext := "my-confluence-api-token"

	encrypted, err := EncryptToken(plaintext, testSecret)
	if err != nil {
		t.Fatalf("EncryptToken failed: %v", err)
	}

	decrypted, err := DecryptToken(encrypted, testSecret)
	if err != nil {
		t.Fatalf("DecryptToken failed: %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("roundtrip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestDifferentPlaintextsDifferentCiphertexts(t *testing.T) {
	enc1, err := EncryptToken("token-one", testSecret)
	if err != nil {
		t.Fatalf("EncryptToken (1) failed: %v", err)
	}

	enc2, err := EncryptToken("token-two", testSecret)
	if err != nil {
		t.Fatalf("EncryptToken (2) failed: %v", err)
	}

	if enc1 == enc2 {
		t.Error("different plaintexts produced identical ciphertexts")
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	encrypted, err := EncryptToken("my-token", testSecret)
	if err != nil {
		t.Fatalf("EncryptToken failed: %v", err)
	}

	_, err = DecryptToken(encrypted, "wrong-jwt-secret")
	if err == nil {
		t.Error("expected decryption with wrong key to fail, but it succeeded")
	}
}

func TestDecryptMalformedInputFails(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"only one part", "deadbeef"},
		{"only two parts", "deadbeef:cafebabe"},
		{"four parts", "a:b:c:d"},
		{"invalid hex in IV", "zzzz:cafebabe:deadbeef"},
		{"invalid hex in tag", "deadbeef:zzzz:cafebabe"},
		{"invalid hex in ciphertext", "deadbeef:cafebabe:zzzz"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecryptToken(tc.input, testSecret)
			if err == nil {
				t.Errorf("expected error for malformed input %q, but got nil", tc.input)
			}
		})
	}
}

func TestMaskTokenLong(t *testing.T) {
	token := "abcdefghijklmnop" // 16 chars, last 4 = "mnop"
	masked := MaskToken(token)

	if !strings.HasSuffix(masked, "mnop") {
		t.Errorf("expected masked token to end with last 4 chars 'mnop', got %q", masked)
	}
	if !strings.HasPrefix(masked, "••••••••") {
		t.Errorf("expected masked token to start with dots, got %q", masked)
	}
}

func TestMaskTokenShort(t *testing.T) {
	cases := []string{"", "abc", "12345678"} // all <= 8 chars

	for _, token := range cases {
		masked := MaskToken(token)
		if masked != "••••••••" {
			t.Errorf("MaskToken(%q) = %q, want %q", token, masked, "••••••••")
		}
	}
}

func TestDeriveKeyLength(t *testing.T) {
	key := DeriveKey("any-secret")
	if len(key) != 32 {
		t.Errorf("DeriveKey produced %d bytes, want 32", len(key))
	}
}
