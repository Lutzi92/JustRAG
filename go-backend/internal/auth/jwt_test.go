package auth_test

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/justrag/go-backend/internal/auth"
)

func makeToken(t *testing.T, secret string, claims auth.Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":       claims.ID,
		"username": claims.Username,
		"role":     claims.Role,
		"jti":      claims.JTI,
		"iat":      claims.IssuedAt,
		"exp":      claims.ExpiresAt,
	})
	s, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("makeToken: %v", err)
	}
	return s
}

func TestParseToken_Valid(t *testing.T) {
	secret := "test-secret-that-is-at-least-32-characters-long"
	now := time.Now()
	claims := auth.Claims{
		ID:        "user-123",
		Username:  "testuser",
		Role:      "admin",
		JTI:       "jti-456",
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(24 * time.Hour).Unix(),
	}

	tokenStr := makeToken(t, secret, claims)
	parsed, err := auth.ParseToken(tokenStr, secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.ID != "user-123" {
		t.Errorf("expected ID user-123, got %s", parsed.ID)
	}
	if parsed.Username != "testuser" {
		t.Errorf("expected username testuser, got %s", parsed.Username)
	}
	if parsed.Role != "admin" {
		t.Errorf("expected role admin, got %s", parsed.Role)
	}
	if parsed.JTI != "jti-456" {
		t.Errorf("expected JTI jti-456, got %s", parsed.JTI)
	}
}

func TestParseToken_Expired(t *testing.T) {
	secret := "test-secret-that-is-at-least-32-characters-long"
	past := time.Now().Add(-2 * time.Hour)
	claims := auth.Claims{
		ID:        "user-123",
		Username:  "testuser",
		Role:      "user",
		JTI:       "jti-789",
		IssuedAt:  past.Add(-24 * time.Hour).Unix(),
		ExpiresAt: past.Unix(),
	}

	tokenStr := makeToken(t, secret, claims)
	_, err := auth.ParseToken(tokenStr, secret)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestParseToken_WrongSecret(t *testing.T) {
	claims := auth.Claims{
		ID: "user-123", Username: "testuser", Role: "user", JTI: "jti-1",
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	tokenStr := makeToken(t, "correct-secret-that-is-32-chars-long!!", claims)

	_, err := auth.ParseToken(tokenStr, "wrong-secret-that-is-also-32-chars-long")
	if err == nil {
		t.Fatal("expected error for wrong secret")
	}
}

func TestParseToken_MissingRequiredClaims(t *testing.T) {
	secret := "test-secret-that-is-at-least-32-characters-long"
	exp := time.Now().Add(time.Hour).Unix()

	cases := []struct {
		name   string
		claims jwt.MapClaims
	}{
		{"missing id", jwt.MapClaims{"role": "user", "jti": "j1", "exp": exp}},
		{"missing role", jwt.MapClaims{"id": "u1", "jti": "j1", "exp": exp}},
		{"missing jti", jwt.MapClaims{"id": "u1", "role": "user", "exp": exp}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			token := jwt.NewWithClaims(jwt.SigningMethodHS256, tc.claims)
			s, err := token.SignedString([]byte(secret))
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			if _, err := auth.ParseToken(s, secret); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if _, err := auth.DecodeTokenUnverified(s); err == nil {
				t.Fatalf("expected DecodeTokenUnverified error for %s", tc.name)
			}
		})
	}
}

func TestDecodeTokenUnverified(t *testing.T) {
	secret := "test-secret-that-is-at-least-32-characters-long"
	now := time.Now()
	claims := auth.Claims{
		ID: "user-123", Username: "test", Role: "user", JTI: "jti-abc",
		IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
	}
	tokenStr := makeToken(t, secret, claims)

	parsed, err := auth.DecodeTokenUnverified(tokenStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.JTI != "jti-abc" {
		t.Errorf("expected JTI jti-abc, got %s", parsed.JTI)
	}
}
