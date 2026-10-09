package app

// Tests for the rate limit on GET /api/search.
//
// Oracle: the route's documented contract (API.md, "Search") — within one
// window, the first searchRateLimitPerMinute requests from one USER reach the
// handler and the next one is answered 429 with a Retry-After header and the
// limiter's fixed JSON body, without reaching the handler; the budget is per
// user, not per IP; a request without a valid token never reaches the limiter.
// None of that is produced by the search handler: the store below is a fake
// that counts its calls and returns a canned body, so "the handler ran" and
// "the limiter answered" are told apart by the call count and by the body,
// never by anything the search code computes.
//
// The tests drive the REAL wiring — registerSearchRoutes, the function
// routes.go calls, with the real auth.Middleware and the real
// middleware.RedisRateLimiter on miniredis — so a change to the chain order
// or the budget in production code changes these outcomes. Only the search
// store is faked, because there is no database here.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/globalsearch"
	"github.com/justrag/go-backend/internal/redisclient"
)

const searchTestSecret = "test-secret-at-least-32-characters-long-x"

// cannedTopic is what the fake store returns; seeing it in a response body
// proves the request reached the search handler.
const cannedTopic = "canned-by-fake-store"

type countingSearchStore struct{ calls int }

func (s *countingSearchStore) Search(_ context.Context, _ globalsearch.Caller, q globalsearch.Query) (*globalsearch.Response, error) {
	s.calls++
	return &globalsearch.Response{
		Query:  q.Text,
		Topics: []globalsearch.TopicHit{{ID: "t1", Name: cannedTopic, Visibility: "public", Role: "view", Match: globalsearch.MatchPrefix}},
	}, nil
}

// searchRig is the real search wiring over miniredis. authRedis backs the
// token blacklist, limitRedis the rate limiter — separate instances so the
// fail-open test can take the limiter's Redis down without also failing
// authentication.
type searchRig struct {
	mux        *http.ServeMux
	store      *countingSearchStore
	limitRedis *miniredis.Miniredis
}

func newSearchRig(t *testing.T) *searchRig {
	t.Helper()
	authRedis := miniredis.RunT(t)
	authClient := redis.NewClient(&redis.Options{Addr: authRedis.Addr()})
	t.Cleanup(func() { _ = authClient.Close() })
	blacklist := auth.NewBlacklist((&redisclient.Client{Client: authClient}).NewBlacklistAdapter(), false)

	limitRedis := miniredis.RunT(t)
	// Fast failure once limitRedis is closed (fail-open test): no retries and
	// a short dial timeout, instead of go-redis's multi-second defaults.
	limitClient := redis.NewClient(&redis.Options{
		Addr: limitRedis.Addr(), MaxRetries: -1, DialTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = limitClient.Close() })

	rc := &routeCtx{mux: http.NewServeMux(), authMw: auth.NewMiddleware(searchTestSecret, blacklist)}
	store := &countingSearchStore{}
	registerSearchRoutes(rc, newSearchRateLimiter(limitClient), store)
	return &searchRig{mux: rc.mux, store: store, limitRedis: limitRedis}
}

// get sends GET /api/search?q=abc from remoteAddr; token "" means no
// Authorization header.
func (r *searchRig) get(t *testing.T, token, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=abc", nil)
	req.RemoteAddr = remoteAddr
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

// assertLimited checks the limiter's 429 contract: status, a Retry-After of
// at most one window, and the limiter's own body (not the search handler's).
func assertLimited(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body %s)", rec.Code, rec.Body)
	}
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || retry < 1 || retry > 60 {
		t.Errorf("Retry-After = %q, want an integer number of seconds in [1, 60]", rec.Header().Get("Retry-After"))
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body is not JSON: %s", rec.Body)
	}
	if want := "Too many requests, please try again later."; body["error"] != want {
		t.Errorf("429 error = %q, want the limiter's %q", body["error"], want)
	}
	if strings.Contains(rec.Body.String(), cannedTopic) {
		t.Error("429 body carries the search handler's output")
	}
}

func assertReachedHandler(t *testing.T, rec *httptest.ResponseRecorder, n int) {
	t.Helper()
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), cannedTopic) {
		t.Fatalf("request %d: status = %d body = %s, want 200 from the search handler", n, rec.Code, rec.Body)
	}
}

// Request 1 gets 200, request N+1 gets 429, with N the production budget
// (searchRateLimitPerMinute, 60).
func TestSearchRouteRateLimit(t *testing.T) {
	rig := newSearchRig(t)
	token := mintJWT(t, searchTestSecret, "u-search-a", auth.RoleUser)

	for i := 1; i <= searchRateLimitPerMinute; i++ {
		assertReachedHandler(t, rig.get(t, token, "192.0.2.10:4000"), i)
	}
	assertLimited(t, rig.get(t, token, "192.0.2.10:4000"))
	if rig.store.calls != searchRateLimitPerMinute {
		t.Fatalf("store called %d times, want exactly %d: the rejected request must not reach the handler",
			rig.store.calls, searchRateLimitPerMinute)
	}

	// The window is one minute: once it has passed, the budget is back.
	rig.limitRedis.FastForward(time.Minute + time.Second)
	assertReachedHandler(t, rig.get(t, token, "192.0.2.10:4000"), searchRateLimitPerMinute+2)
}

// An unauthenticated request must not burn a real user's budget. The limiter
// sits inside Authenticate, so a flood without a token is answered 401 and
// never touches the counter.
func TestSearchRouteUnauthenticatedDoesNotSpendBudget(t *testing.T) {
	rig := newSearchRig(t)

	for i := 0; i < searchRateLimitPerMinute+5; i++ {
		for _, tok := range []string{"", "not-a-jwt"} {
			if rec := rig.get(t, tok, "192.0.2.10:4000"); rec.Code != http.StatusUnauthorized {
				t.Fatalf("token %q: status = %d, want 401", tok, rec.Code)
			}
		}
	}
	if keys := rig.limitRedis.Keys(); len(keys) != 0 {
		t.Fatalf("unauthenticated requests created limiter keys %v; they must not reach the limiter", keys)
	}

	// A real user behind the same IP still has the full budget.
	token := mintJWT(t, searchTestSecret, "u-search-a", auth.RoleUser)
	for i := 1; i <= searchRateLimitPerMinute; i++ {
		assertReachedHandler(t, rig.get(t, token, "192.0.2.10:4000"), i)
	}
	assertLimited(t, rig.get(t, token, "192.0.2.10:4000"))
}

// The budget is per user, not per IP: two users behind one IP (a campus NAT,
// a VPN egress) each have the full budget, and a user who has spent theirs
// is still limited from another IP. The counter lives under the user's key,
// never under the IP.
func TestSearchRouteBudgetIsPerUser(t *testing.T) {
	rig := newSearchRig(t)
	alice := mintJWT(t, searchTestSecret, "u-search-alice", auth.RoleUser)
	bob := mintJWT(t, searchTestSecret, "u-search-bob", auth.RoleUser)

	for i := 1; i <= searchRateLimitPerMinute; i++ {
		assertReachedHandler(t, rig.get(t, alice, "192.0.2.10:4000"), i)
	}
	assertLimited(t, rig.get(t, alice, "192.0.2.10:4000"))
	// Bob shares Alice's IP but has not searched: his budget is untouched.
	for i := 1; i <= searchRateLimitPerMinute; i++ {
		assertReachedHandler(t, rig.get(t, bob, "192.0.2.10:5000"), i)
	}
	assertLimited(t, rig.get(t, bob, "192.0.2.10:5000"))
	// Alice from a different IP is still over her budget.
	assertLimited(t, rig.get(t, alice, "198.51.100.7:4000"))

	want := []string{"rl:search:user:u-search-alice", "rl:search:user:u-search-bob"}
	if got := rig.limitRedis.Keys(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("limiter keys = %v, want exactly the two per-user keys %v", got, want)
	}
}

// Fail open. With the limiter's Redis down, search keeps working. The
// first half spends the whole budget while Redis is up, so the requests that
// then pass can only pass because the limiter failed open, not because
// budget was left.
func TestSearchRouteFailsOpenWithoutRedis(t *testing.T) {
	rig := newSearchRig(t)
	token := mintJWT(t, searchTestSecret, "u-search-a", auth.RoleUser)
	for i := 1; i <= searchRateLimitPerMinute; i++ {
		assertReachedHandler(t, rig.get(t, token, "192.0.2.10:4000"), i)
	}
	assertLimited(t, rig.get(t, token, "192.0.2.10:4000"))

	rig.limitRedis.Close()
	for i := 1; i <= 3; i++ {
		assertReachedHandler(t, rig.get(t, token, "192.0.2.10:4000"), i)
	}
}
