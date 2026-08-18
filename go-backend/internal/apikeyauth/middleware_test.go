package apikeyauth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/justrag/go-backend/internal/apikeyauth"
	"github.com/justrag/go-backend/internal/auth"
)

// ---------------------------------------------------------------------------
// helpers & mock store
// ---------------------------------------------------------------------------

const testToken = "jrag_abcdef1234567890abcdef1234567890ab"

func makeHash(token string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	return string(hash)
}

var _ apikeyauth.Store = (*mockStore)(nil)

type mockStore struct {
	candidates []apikeyauth.ApiKeyCandidate
	user       *apikeyauth.UserInfo
	userErr    error
	prefixErr  error
	lastUsedID string
}

func (m *mockStore) GetApiKeysByPrefix(_ context.Context, _ string) ([]apikeyauth.ApiKeyCandidate, error) {
	return m.candidates, m.prefixErr
}

func (m *mockStore) GetUserByID(_ context.Context, _ string) (*apikeyauth.UserInfo, error) {
	return m.user, m.userErr
}

func (m *mockStore) UpdateApiKeyLastUsed(_ context.Context, id string) error {
	m.lastUsedID = id
	return nil
}

// newHandler creates a handler chain: Authenticate → 200 OK, setting gotUser.
func newHandler(mw *apikeyauth.Middleware, gotUser **auth.Claims) http.Handler {
	return mw.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotUser != nil {
			*gotUser = auth.UserFromContext(r.Context())
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func doRequest(handler http.Handler, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/test", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// newTestMiddlewareWithKey builds a Middleware backed by a mock store with one
// valid, non-expiring API key, plus the bearer token that authenticates as it
// and the id of that key. Shared by every test that just needs "a valid
// request came in via API key" rather than a specific failure mode.
func newTestMiddlewareWithKey(t *testing.T) (mw *apikeyauth.Middleware, token string, keyID string) {
	t.Helper()
	hash := makeHash(testToken)
	expires := time.Now().Add(time.Hour)

	store := &mockStore{
		candidates: []apikeyauth.ApiKeyCandidate{
			{ID: "key-1", UserID: "user-1", KeyHash: hash, ExpiresAt: &expires},
		},
		user: &apikeyauth.UserInfo{ID: "user-1", Username: "alice", Role: "admin"},
	}
	return apikeyauth.NewMiddleware(store), testToken, "key-1"
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestAuthenticate_ValidKey(t *testing.T) {
	mw, token, _ := newTestMiddlewareWithKey(t)

	var gotUser *auth.Claims
	rec := doRequest(newHandler(mw, &gotUser), "Bearer "+token)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if gotUser == nil {
		t.Fatal("expected user claims in context, got nil")
	}
	if gotUser.ID != "user-1" {
		t.Errorf("expected user ID %q, got %q", "user-1", gotUser.ID)
	}
	if gotUser.Username != "alice" {
		t.Errorf("expected username %q, got %q", "alice", gotUser.Username)
	}
	if gotUser.Role != "admin" {
		t.Errorf("expected role %q, got %q", "admin", gotUser.Role)
	}
}

func TestAuthenticate_MissingAuthHeader(t *testing.T) {
	mw := apikeyauth.NewMiddleware(&mockStore{})

	rec := doRequest(newHandler(mw, nil), "")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthenticate_NoJragPrefix(t *testing.T) {
	mw := apikeyauth.NewMiddleware(&mockStore{})

	rec := doRequest(newHandler(mw, nil), "Bearer sk_notajragkey1234567890")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthenticate_NoMatchingPrefix(t *testing.T) {
	store := &mockStore{
		candidates: []apikeyauth.ApiKeyCandidate{}, // empty — prefix not found
	}
	mw := apikeyauth.NewMiddleware(store)

	rec := doRequest(newHandler(mw, nil), "Bearer "+testToken)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthenticate_WrongHash(t *testing.T) {
	otherHash := makeHash("jrag_differenttoken1234567890abcdef12")

	store := &mockStore{
		candidates: []apikeyauth.ApiKeyCandidate{
			{ID: "key-2", UserID: "user-2", KeyHash: otherHash},
		},
		user: &apikeyauth.UserInfo{ID: "user-2", Username: "bob", Role: "user"},
	}
	mw := apikeyauth.NewMiddleware(store)

	rec := doRequest(newHandler(mw, nil), "Bearer "+testToken)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on hash mismatch, got %d", rec.Code)
	}
}

func TestAuthenticate_ExpiredKey(t *testing.T) {
	hash := makeHash(testToken)
	expired := time.Now().Add(-time.Hour) // in the past

	store := &mockStore{
		candidates: []apikeyauth.ApiKeyCandidate{
			{ID: "key-3", UserID: "user-3", KeyHash: hash, ExpiresAt: &expired},
		},
		user: &apikeyauth.UserInfo{ID: "user-3", Username: "carol", Role: "user"},
	}
	mw := apikeyauth.NewMiddleware(store)

	rec := doRequest(newHandler(mw, nil), "Bearer "+testToken)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired key, got %d", rec.Code)
	}
}

func TestAuthenticate_NoExpiry(t *testing.T) {
	// ExpiresAt == nil means the key never expires.
	hash := makeHash(testToken)

	store := &mockStore{
		candidates: []apikeyauth.ApiKeyCandidate{
			{ID: "key-4", UserID: "user-4", KeyHash: hash, ExpiresAt: nil},
		},
		user: &apikeyauth.UserInfo{ID: "user-4", Username: "dave", Role: "user"},
	}
	mw := apikeyauth.NewMiddleware(store)

	rec := doRequest(newHandler(mw, nil), "Bearer "+testToken)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for non-expiring key, got %d", rec.Code)
	}
}

// TestAuthenticate_ExposesAPIKeyID pins that the authenticated key's id reaches
// the handler. Before this, Authenticate injected only auth.Claims and the key
// id never left the middleware, which made usage_events.api_key_id unfillable.
func TestAuthenticate_ExposesAPIKeyID(t *testing.T) {
	// newTestMiddlewareWithKey wires a Middleware over one valid key ("key-1")
	// and returns its bearer token alongside that id, so the assertion below
	// can check the exact value round-tripped through the context.
	mw, token, wantKeyID := newTestMiddlewareWithKey(t)

	var gotKeyID *string
	handler := mw.Authenticate(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotKeyID = auth.APIKeyIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kb", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if gotKeyID == nil {
		t.Fatal("api key id missing from context")
	}
	if *gotKeyID != wantKeyID {
		t.Errorf("api key id: got %q, want %q", *gotKeyID, wantKeyID)
	}
}

// TestAPIKeyIDFromContext_NilWithoutAPIKeyAuth pins the web case: a JWT request
// has no key id, and usage_events.api_key_id must end up NULL rather than "".
func TestAPIKeyIDFromContext_NilWithoutAPIKeyAuth(t *testing.T) {
	if got := auth.APIKeyIDFromContext(context.Background()); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}
