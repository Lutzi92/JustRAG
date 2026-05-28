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

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestAuthenticate_ValidKey(t *testing.T) {
	hash := makeHash(testToken)
	expires := time.Now().Add(time.Hour)

	store := &mockStore{
		candidates: []apikeyauth.ApiKeyCandidate{
			{ID: "key-1", UserID: "user-1", KeyHash: hash, ExpiresAt: &expires},
		},
		user: &apikeyauth.UserInfo{ID: "user-1", Username: "alice", Role: "admin"},
	}
	mw := apikeyauth.NewMiddleware(store)

	var gotUser *auth.Claims
	rec := doRequest(newHandler(mw, &gotUser), "Bearer "+testToken)

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
