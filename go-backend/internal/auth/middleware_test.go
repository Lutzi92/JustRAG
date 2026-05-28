package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
)

const testSecret = "test-secret-that-is-at-least-32-characters-long"

func TestMiddleware_NoAuthHeader(t *testing.T) {
	store := newMockRedis()
	bl := auth.NewBlacklist(store, false)
	mw := auth.NewMiddleware(testSecret, bl)

	handler := mw.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_ValidToken(t *testing.T) {
	store := newMockRedis()
	bl := auth.NewBlacklist(store, false)
	mw := auth.NewMiddleware(testSecret, bl)

	claims := auth.Claims{
		ID: "user-1", Username: "testuser", Role: "admin", JTI: "jti-1",
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	tokenStr := makeToken(t, testSecret, claims)

	var gotUser *auth.Claims
	handler := mw.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = auth.UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if gotUser == nil || gotUser.ID != "user-1" {
		t.Error("expected user claims in context")
	}
}

func TestMiddleware_BlacklistedToken(t *testing.T) {
	store := newMockRedis()
	bl := auth.NewBlacklist(store, false)
	mw := auth.NewMiddleware(testSecret, bl)

	claims := auth.Claims{
		ID: "user-1", Username: "testuser", Role: "user", JTI: "jti-blacklisted",
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	tokenStr := makeToken(t, testSecret, claims)

	bl.Add(context.Background(), "jti-blacklisted", time.Now().Add(time.Hour))

	handler := mw.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for blacklisted token, got %d", rec.Code)
	}
}

func TestMiddleware_RequireRole(t *testing.T) {
	store := newMockRedis()
	bl := auth.NewBlacklist(store, false)
	mw := auth.NewMiddleware(testSecret, bl)

	claims := auth.Claims{
		ID: "user-1", Username: "testuser", Role: "user", JTI: "jti-2",
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	tokenStr := makeToken(t, testSecret, claims)

	handler := mw.Authenticate(mw.RequireRole("admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for wrong role, got %d", rec.Code)
	}
}
