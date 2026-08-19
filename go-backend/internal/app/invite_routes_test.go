package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/database"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/middleware"
	"github.com/justrag/go-backend/internal/redisclient"
)

// mintJWT builds a real, validly-signed token for ParseToken to accept —
// mirrors auth's own jwt_test.go makeToken helper (unexported there, so not
// importable), because this test needs to drive the real
// rc.authMw.Authenticate wrapper end to end, not bypass it with
// auth.WithUser.
func mintJWT(t *testing.T, secret, userID, role string) string {
	t.Helper()
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":       userID,
		"username": "joiner",
		"role":     role,
		"jti":      "jti-" + userID,
		"iat":      now.Unix(),
		"exp":      now.Add(time.Hour).Unix(),
	})
	s, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("mintJWT: %v", err)
	}
	return s
}

// TestRedeemRouteIsNotKBGated pins the one wiring mistake that would break
// the whole invite-link feature silently: a person redeeming an invite link
// has NO kb_members row on the target KB — that is the entire point of
// redeeming. Registering the redeem route on a KB-gated chain (kbViewChain,
// kbAdminChain, ...) would 403 them before they could ever join, and no
// other unit test would notice, because every existing chain test only
// exercises callers who already have a KB role.
//
// Unlike kbadmin_chain_test.go's helpers, this does not mirror the chain
// composition locally — a mirror only pins how a chain built from scratch
// behaves, not which chain routes.go actually wired the redeem route to. It
// calls the real registerKBRoutes(rc, inviteRL) and sends requests at the
// resulting rc.mux, so a change to the registration line under test (moving
// POST /api/invites/{token}/redeem onto a kbaccess-gated chain) changes this
// test's outcome. Confirmed by mutation: swapping the registration to
// rc.kbViewChain(...) turns this test red (see task-5-report.md).
//
// The KB-admin-gated invite-links route (registered two lines above redeem
// in routes.go) supplies the contrast: same non-member caller, same rc.mux,
// clean 403 with no DB access (kbaccess rejects before the handler runs).
// The redeem side has no kb_members row to gate on and no test double for
// the DB pool, so a *correctly* wired redeem reaches the real
// kbinvites.PGStore.Redeem, which then panics dereferencing a nil
// *pgxpool.Pool — that panic, recovered here, is the positive signal that
// nothing upstream of the handler rejected the request. A wrongly-wired
// redeem (kbaccess in the chain) instead returns a clean 403 with no panic,
// exactly like the invite-links contrast route.
func TestRedeemRouteIsNotKBGated(t *testing.T) {
	const secret = "test-secret-at-least-32-characters-long-x"

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	blacklist := auth.NewBlacklist((&redisclient.Client{Client: rdb}).NewBlacklistAdapter(), false)
	authMw := auth.NewMiddleware(secret, blacklist)

	// No kb_members row at all — the exact state a would-be joiner is in.
	kbMw := kbaccess.NewMiddleware(kbStoreStub{
		kb:   &kbaccess.KnowledgeBase{ID: "kb-1", IsGlobal: false, IsPublished: false},
		role: "",
	})

	rc := &routeCtx{
		mux:    http.NewServeMux(),
		infra:  &serverInfra{db: &database.DB{}, rdb: &redisclient.Client{Client: rdb}},
		cfg:    &config.Config{JWTSecret: secret},
		authMw: authMw,
		kbMw:   kbMw,
		kbViewChain: func(h http.HandlerFunc) http.Handler {
			return authMw.Authenticate(kbMw.RequireKBRole(kbaccess.RoleView)(http.HandlerFunc(h)))
		},
		kbEditChain: func(h http.HandlerFunc) http.Handler {
			return authMw.Authenticate(kbMw.RequireKBRole(kbaccess.RoleEdit)(http.HandlerFunc(h)))
		},
		kbAdminChain: func(h http.HandlerFunc) http.Handler {
			return authMw.Authenticate(kbMw.RequireKBRole(kbaccess.RoleAdmin)(http.HandlerFunc(h)))
		},
		kbAdvancedChain: func(h http.HandlerFunc) http.Handler {
			return authMw.Authenticate(kbAdvancedInner(authMw, kbMw)(h))
		},
		analyticsChain: func(h http.HandlerFunc) http.Handler {
			return authMw.Authenticate(kbMw.RequireKBRole(kbaccess.RoleView)(kbMw.RequireAnalyticsAccess(http.HandlerFunc(h))))
		},
		adminChain:      auth.RoleChain(authMw, auth.RoleAdmin, auth.RoleSuperAdmin),
		superadminChain: auth.RoleChain(authMw, auth.RoleSuperAdmin),
		apiKeyChain:     auth.RoleChain(authMw, auth.RoleAdmin, auth.RoleSuperAdmin, auth.RoleAPIUser),
	}

	inviteRL := middleware.NewRedisRateLimiter(rdb, middleware.RedisRateLimitConfig{
		Max: 10, Window: time.Minute, Category: "invite-test",
	})

	// The real registration this test exists to pin.
	registerKBRoutes(rc, inviteRL)

	token := mintJWT(t, secret, "u-joiner", auth.RoleUser)

	// Contrast: the KB-admin-gated invite-links route (registered right next
	// to redeem in routes.go) must reject this caller outright.
	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/invite-links", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	rc.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("kbAdminChain gave the non-member %d, want 403 — the premise of this test is wrong", rec.Code)
	}

	// The redeem route must NOT reject this caller the same way. There is no
	// DB in this test, so a *correctly* wired redeem (auth only, no kbaccess)
	// reaches the real store and panics on the nil pool — that panic is the
	// signal that nothing rejected the request upstream. A 403 here (no
	// panic) means kbaccess middleware intercepted it, i.e. the route was
	// wired wrong.
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("redeem route did not reach the handler: got %d with no panic — "+
					"a KB-gated chain likely rejected the non-member before the handler ran", rec.Code)
			}
			t.Logf("redeem reached the real handler and panicked on the nil test DB pool, as expected: %v", r)
		}()
		req = httptest.NewRequest(http.MethodPost, "/api/invites/tok/redeem", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec = httptest.NewRecorder()
		rc.mux.ServeHTTP(rec, req)
	}()
}
