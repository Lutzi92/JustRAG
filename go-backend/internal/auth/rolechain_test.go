package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/justrag/go-backend/internal/auth"
)

// ---------------------------------------------------------------------------
// Smoke tests for the role-chain wiring used by app.setupRoutes.
//
// These tests live in the auth package — instead of the app package — so they
// run independently of the rest of the backend's dependency tree (which boots
// pgx, redis, asynq, etc.). RoleChain is the single composition helper every
// role-gated route in routes.go uses, so a regression here would mean a
// production admin endpoint silently accepts unauthenticated traffic — the
// highest-blast-radius routing bug we can have.
// ---------------------------------------------------------------------------

const roleChainTestSecret = "test-secret-that-is-at-least-32-characters-long"

func issueRoleChainToken(t *testing.T, role string) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":       "user-1",
		"username": "alice",
		"role":     role,
		"jti":      "jti-1",
		"iat":      now.Unix(),
		"exp":      now.Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString([]byte(roleChainTestSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

func newRoleChainServer(t *testing.T, chain func(http.HandlerFunc) http.Handler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /protected", chain(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func roleChainGet(t *testing.T, url, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestRoleChain pins the invariants the setupRoutes wiring assumes for every
// admin / superadmin / api-key route group:
//
//   - unauthenticated request → 401
//   - authenticated user with the wrong role → 403
//   - authenticated user with a permitted role → 200
//   - RequireRole's escalation rule: superadmin is always accepted, but no
//     other role inherits superadmin's privileges
//   - invalid / malformed bearer token → 401 (Authenticate rejects before
//     RequireRole gets a chance)
func TestRoleChain(t *testing.T) {
	t.Parallel()
	mw := auth.NewMiddleware(roleChainTestSecret, auth.NewBlacklist(newMockRedis(), false))

	cases := []struct {
		name       string
		chain      func(http.HandlerFunc) http.Handler
		token      string
		wantStatus int
	}{
		{
			name:       "admin_chain_rejects_unauth",
			chain:      auth.RoleChain(mw, "admin", "superadmin"),
			token:      "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "admin_chain_rejects_user_role",
			chain:      auth.RoleChain(mw, "admin", "superadmin"),
			token:      issueRoleChainToken(t, "user"),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "admin_chain_accepts_admin",
			chain:      auth.RoleChain(mw, "admin", "superadmin"),
			token:      issueRoleChainToken(t, "admin"),
			wantStatus: http.StatusOK,
		},
		{
			name:       "admin_chain_accepts_superadmin",
			chain:      auth.RoleChain(mw, "admin", "superadmin"),
			token:      issueRoleChainToken(t, "superadmin"),
			wantStatus: http.StatusOK,
		},
		{
			// superadmin chain accepts ONLY superadmin. RequireRole's
			// escalation rule lets superadmin pass everywhere but does
			// not promote admin into a superadmin-only chain.
			name:       "superadmin_chain_rejects_admin",
			chain:      auth.RoleChain(mw, "superadmin"),
			token:      issueRoleChainToken(t, "admin"),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "superadmin_chain_accepts_superadmin",
			chain:      auth.RoleChain(mw, "superadmin"),
			token:      issueRoleChainToken(t, "superadmin"),
			wantStatus: http.StatusOK,
		},
		{
			name:       "apikey_chain_accepts_api_user",
			chain:      auth.RoleChain(mw, "admin", "superadmin", "api-user"),
			token:      issueRoleChainToken(t, "api-user"),
			wantStatus: http.StatusOK,
		},
		{
			name:       "apikey_chain_rejects_plain_user",
			chain:      auth.RoleChain(mw, "admin", "superadmin", "api-user"),
			token:      issueRoleChainToken(t, "user"),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "rejects_invalid_token",
			chain:      auth.RoleChain(mw, "admin", "superadmin"),
			token:      "not-a-jwt",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newRoleChainServer(t, tc.chain)
			got := roleChainGet(t, srv.URL+"/protected", tc.token)
			if got != tc.wantStatus {
				t.Errorf("status: got %d, want %d", got, tc.wantStatus)
			}
		})
	}
}
