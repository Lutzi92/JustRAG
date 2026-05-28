package confluence

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// DeriveKey creates a 32-byte AES-256 key from the JWT secret via SHA-256.
func DeriveKey(jwtSecret string) []byte {
	h := sha256.Sum256([]byte(jwtSecret))
	return h[:]
}

// EncryptToken encrypts plaintext using AES-256-GCM.
// Returns format: "{iv-hex}:{authTag-hex}:{ciphertext-hex}"
// Compatible with the Node.js encryption implementation.
func EncryptToken(plaintext, jwtSecret string) (string, error) {
	key := DeriveKey(jwtSecret)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Seal appends the auth tag to the ciphertext: result = ciphertext || authTag
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)

	// Split: last 16 bytes are the auth tag, the rest is ciphertext
	tagSize := gcm.Overhead() // always 16 for standard GCM
	ciphertext := sealed[:len(sealed)-tagSize]
	authTag := sealed[len(sealed)-tagSize:]

	return hex.EncodeToString(nonce) + ":" + hex.EncodeToString(authTag) + ":" + hex.EncodeToString(ciphertext), nil
}

// DecryptToken decrypts the format produced by EncryptToken (or the Node.js equivalent).
// Expected format: "{iv-hex}:{authTag-hex}:{ciphertext-hex}"
func DecryptToken(encrypted, jwtSecret string) (string, error) {
	parts := strings.Split(encrypted, ":")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid encrypted format: expected 3 colon-separated parts, got %d", len(parts))
	}

	iv, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("failed to decode IV: %w", err)
	}

	authTag, err := hex.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to decode auth tag: %w", err)
	}

	ciphertext, err := hex.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("failed to decode ciphertext: %w", err)
	}

	key := DeriveKey(jwtSecret)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Go's GCM Open expects ciphertext || authTag
	sealedData := append(ciphertext, authTag...)

	plaintext, err := gcm.Open(nil, iv, sealedData, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed: %w", err)
	}

	return string(plaintext), nil
}

// MaskToken returns a masked display version of a token.
// Shows "••••••••" + last 4 chars if token is longer than 8 characters.
func MaskToken(token string) string {
	if len(token) <= 8 {
		return "••••••••"
	}
	return "••••••••" + token[len(token)-4:]
}
