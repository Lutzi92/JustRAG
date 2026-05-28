package authhandler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/justrag/go-backend/internal/adminproviders"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/authhandler"
	"github.com/justrag/go-backend/internal/users"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var (
	_ authhandler.Store   = (*mockStore)(nil)
	_ auth.BlacklistStore = (*mockBlacklistStore)(nil)
)

type mockStore struct {
	user      *users.UserRow
	providers []adminproviders.AuthProviderRow
}

func (m *mockStore) GetUserByUsername(_ context.Context, _ string) (*users.UserRow, error) {
	return m.user, nil
}

func (m *mockStore) CreateUser(_ context.Context, data users.UserCreate) (*users.UserRow, error) {
	return &users.UserRow{
		ID:       "new-user-id",
		Username: data.Username,
		Role:     data.Role,
	}, nil
}

func (m *mockStore) GetActiveAuthProviders(_ context.Context) ([]adminproviders.AuthProviderRow, error) {
	return m.providers, nil
}

// ---------------------------------------------------------------------------
// Mock blacklist store
// ---------------------------------------------------------------------------

type mockBlacklistStore struct {
	data map[string]string
}

func newMockBlacklistStore() *mockBlacklistStore {
	return &mockBlacklistStore{data: map[string]string{}}
}

func (m *mockBlacklistStore) Get(_ context.Context, key string) (string, error) {
	v, ok := m.data[key]
	if !ok {
		return "", auth.ErrKeyNotFound
	}
	return v, nil
}

func (m *mockBlacklistStore) Set(_ context.Context, key string, value any, _ time.Duration) error {
	m.data[key] = value.(string)
	return nil
}

func (m *mockBlacklistStore) Exists(_ context.Context, keys ...string) (int64, error) {
	var count int64
	for _, k := range keys {
		if _, ok := m.data[k]; ok {
			count++
		}
	}
	return count, nil
}

func (m *mockBlacklistStore) Pipeline(_ context.Context, _ func(pipe auth.PipelineExecer) error) error {
	return fmt.Errorf("pipeline not supported in mock")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const testSecret = "test-secret-key"

func newHandler(store authhandler.Store) *authhandler.Handler {
	bl := auth.NewBlacklist(newMockBlacklistStore(), false)
	return authhandler.NewHandler(store, testSecret, bl)
}

func makeRequest(method, path, body string, authToken string) *http.Request {
	bodyReader := bytes.NewReader([]byte(body))
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	return req
}

func parseToken(t *testing.T, tokenStr string) jwt.MapClaims {
	t.Helper()
	tok, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
		return []byte(testSecret), nil
	})
	if err != nil {
		t.Fatalf("failed to parse token: %v", err)
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("claims are not MapClaims")
	}
	return claims
}

// signTestToken creates a valid JWT signed with testSecret for use in logout/refresh tests.
func signTestToken(id, username, role, jti string) string {
	now := time.Now()
	exp := now.Add(24 * time.Hour)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":       id,
		"username": username,
		"role":     role,
		"jti":      jti,
		"iat":      now.Unix(),
		"exp":      exp.Unix(),
	})
	signed, _ := tok.SignedString([]byte(testSecret))
	return signed
}

// withUserContext injects auth.Claims into the request context (simulates auth middleware).
func withUserContext(r *http.Request, claims *auth.Claims) *http.Request {
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// 1. Login with valid local credentials → 200 + JWT + user object.
func TestLogin_ValidCredentials(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)

	store := &mockStore{
		user: &users.UserRow{
			ID:           "user-123",
			Username:     "alice",
			PasswordHash: string(hash),
			Role:         "user",
		},
	}
	h := newHandler(store)

	body := `{"username":"alice","password":"correct-password"}`
	req := makeRequest(http.MethodPost, "/api/auth/login", body, "")
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Token string `json:"token"`
		User  struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Token == "" {
		t.Fatal("expected non-empty token")
	}
	if resp.User.ID != "user-123" {
		t.Errorf("expected user ID user-123, got %q", resp.User.ID)
	}
	if resp.User.Username != "alice" {
		t.Errorf("expected username alice, got %q", resp.User.Username)
	}

	// Verify the JWT is parseable with the test secret.
	claims := parseToken(t, resp.Token)
	if claims["username"] != "alice" {
		t.Errorf("JWT username mismatch: %v", claims["username"])
	}
	if claims["jti"] == "" {
		t.Error("JWT missing jti")
	}
}

// 2. Login missing username → 400.
func TestLogin_MissingUsername(t *testing.T) {
	h := newHandler(&mockStore{})

	body := `{"password":"somepass"}`
	req := makeRequest(http.MethodPost, "/api/auth/login", body, "")
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// 3. Login wrong password → 401.
func TestLogin_WrongPassword(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)

	store := &mockStore{
		user: &users.UserRow{
			ID:           "user-123",
			Username:     "alice",
			PasswordHash: string(hash),
			Role:         "user",
		},
		providers: []adminproviders.AuthProviderRow{}, // no LDAP providers
	}
	h := newHandler(store)

	body := `{"username":"alice","password":"wrong-password"}`
	req := makeRequest(http.MethodPost, "/api/auth/login", body, "")
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if !strings.Contains(resp["error"], "Invalid username or password") {
		t.Errorf("unexpected error message: %q", resp["error"])
	}
}

// 4. Logout → 200 + message.
func TestLogout(t *testing.T) {
	h := newHandler(&mockStore{})

	tokenStr := signTestToken("user-123", "alice", "user", "some-jti")
	req := makeRequest(http.MethodPost, "/api/auth/logout", "", tokenStr)
	// Simulate auth middleware having already run by injecting context.
	req = withUserContext(req, &auth.Claims{
		ID: "user-123", Username: "alice", Role: "user", JTI: "some-jti",
	})

	rr := httptest.NewRecorder()
	h.Logout(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["message"] != "Logged out" {
		t.Errorf("unexpected message: %q", resp["message"])
	}
}

// 5. Local auth disabled + superadmin → breakglass login succeeds.
func TestLogin_LocalDisabled_SuperadminBreakglass(t *testing.T) {
	t.Setenv("DISABLE_LOCAL_AUTH", "true")
	hash, _ := bcrypt.GenerateFromPassword([]byte("admin-pass"), bcrypt.MinCost)

	store := &mockStore{
		user: &users.UserRow{
			ID:           "su-1",
			Username:     "root",
			PasswordHash: string(hash),
			Role:         "superadmin",
		},
		providers: []adminproviders.AuthProviderRow{},
	}
	h := newHandler(store)

	body := `{"username":"root","password":"admin-pass"}`
	req := makeRequest(http.MethodPost, "/api/auth/login", body, "")
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (breakglass), got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Token == "" {
		t.Error("expected non-empty token in breakglass response")
	}
}

// 6. Local auth disabled + regular user → rejected with 401.
func TestLogin_LocalDisabled_RegularUserRejected(t *testing.T) {
	t.Setenv("DISABLE_LOCAL_AUTH", "true")
	hash, _ := bcrypt.GenerateFromPassword([]byte("user-pass"), bcrypt.MinCost)

	store := &mockStore{
		user: &users.UserRow{
			ID:           "u-1",
			Username:     "alice",
			PasswordHash: string(hash),
			Role:         "user",
		},
		providers: []adminproviders.AuthProviderRow{},
	}
	h := newHandler(store)

	body := `{"username":"alice","password":"user-pass"}`
	req := makeRequest(http.MethodPost, "/api/auth/login", body, "")
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (regular local user blocked), got %d: %s", rr.Code, rr.Body.String())
	}
}

// 7. Refresh → 200 + new token with different jti.
func TestRefresh(t *testing.T) {
	h := newHandler(&mockStore{})

	originalJTI := "original-jti"
	tokenStr := signTestToken("user-123", "alice", "user", originalJTI)
	req := makeRequest(http.MethodPost, "/api/auth/refresh", "", tokenStr)
	req = withUserContext(req, &auth.Claims{
		ID: "user-123", Username: "alice", Role: "user", JTI: originalJTI,
	})

	rr := httptest.NewRecorder()
	h.Refresh(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Token string `json:"token"`
		User  struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Token == "" {
		t.Fatal("expected non-empty token")
	}

	newClaims := parseToken(t, resp.Token)
	newJTI, _ := newClaims["jti"].(string)
	if newJTI == "" {
		t.Error("new token missing jti")
	}
	if newJTI == originalJTI {
		t.Error("refresh should produce a new jti, got the same one")
	}
	if newClaims["username"] != "alice" {
		t.Errorf("expected username alice in refreshed token, got %v", newClaims["username"])
	}
}
