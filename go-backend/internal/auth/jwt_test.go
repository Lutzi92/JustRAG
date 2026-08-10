package auth_test

import (
	"encoding/base64"
	"strconv"
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

// A token with no `exp` claim must be rejected outright. Every token this
// service issues sets exp (authhandler.signToken), so this is a hard guard
// against a future issuance path that forgets it: without WithExpirationRequired
// the jwt library treats a missing exp as "never expires".
func TestParseToken_MissingExpirationRejected(t *testing.T) {
	t.Parallel()
	secret := "test-secret-that-is-at-least-32-characters-long"

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":   "u1",
		"role": "user",
		"jti":  "j1",
		"iat":  time.Now().Unix(),
	})
	s, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if _, err := auth.ParseToken(s, secret); err == nil {
		t.Fatal("expected error for token without exp claim")
	}
}

// alg=none must be refused before the keyfunc ever runs. The keyfunc already
// rejects non-HMAC methods; WithValidMethods makes the rejection structural so
// it survives a future keyfunc refactor.
func TestParseToken_RejectsNoneAlgorithm(t *testing.T) {
	t.Parallel()
	secret := "test-secret-that-is-at-least-32-characters-long"

	claims := jwt.MapClaims{
		"id":   "u1",
		"role": "admin",
		"jti":  "j1",
		"exp":  time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	s, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	if _, err := auth.ParseToken(s, secret); err == nil {
		t.Fatal("expected error for alg=none token")
	}
}

// A token signed with an asymmetric method must not be verifiable by handing
// the HMAC secret to the RSA verifier (classic algorithm-confusion shape).
func TestParseToken_RejectsNonHMACAlgorithm(t *testing.T) {
	t.Parallel()
	secret := "test-secret-that-is-at-least-32-characters-long"

	// Hand-craft an RS256 header over otherwise-valid claims. The signature is
	// garbage on purpose: parsing must fail on the algorithm, not the signature.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(
		`{"id":"u1","role":"admin","jti":"j1","exp":` +
			strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + `}`))
	forged := header + "." + body + "." + base64.RawURLEncoding.EncodeToString([]byte("sig"))

	if _, err := auth.ParseToken(forged, secret); err == nil {
		t.Fatal("expected error for RS256 token")
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
