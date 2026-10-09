package app

import (
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/globalsearch"
	"github.com/justrag/go-backend/internal/middleware"
)

// searchRateLimitPerMinute is GET /api/search's budget per user. 60: the
// header search field debounces at 250 ms, so fast typing sends roughly 1-3
// requests per search, and a person searching all the time stays far below
// one per second. The limit only bites on a script or a stuck client.
const searchRateLimitPerMinute = 60

// newSearchRateLimiter builds the search limiter from the shared Redis
// limiter: fixed window, keyed on the authenticated user id (searchRateKey),
// and fail OPEN on a Redis outage (the limiter's default) — search is not a
// security boundary the way login is, so a Redis blip should not take the
// header search down. A fail-open is still counted in the rate-limit
// fallback metric and logged at warn.
func newSearchRateLimiter(rdb redis.Cmdable) *middleware.RedisRateLimiter {
	return middleware.NewRedisRateLimiter(rdb, middleware.RedisRateLimitConfig{
		Max: searchRateLimitPerMinute, Window: time.Minute, Category: "search",
		Key: searchRateKey,
	})
}

// searchRateKey counts a search against the user who sent it
// (rl:search:user:<id>), not against the client IP: users behind one campus
// NAT or VPN egress must not share — and exhaust — one budget. The user is
// only known after Authenticate, which is why the limiter sits inside it
// (registerSearchRoutes). Without a user the limiter falls back to the IP;
// on this route that cannot happen, since Authenticate answers 401 first.
func searchRateKey(r *http.Request) string {
	if u := auth.UserFromContext(r.Context()); u != nil && u.ID != "" {
		return "user:" + u.ID
	}
	return ""
}

// registerSearchRoutes wires the shell header's global search. Split out of
// registerKBRoutes so its test can drive the real wiring with a fake store.
//
// Chain order: Authenticate OUTSIDE, the rate limiter INSIDE.
//
//	Authenticate → searchRL → search handler
//
// This is the reverse of the usual convention (limiter outside auth), for
// one reason: the limiter keys on the user id, and the user exists only once
// Authenticate has run. A side effect is that a request without a valid
// token is answered 401 before it touches any counter, so it cannot spend a
// real user's budget; such requests cost only a JWT check and a Redis
// blacklist lookup, never a search query.
//
// Authentication only, like GET /api/kb and GET /api/kb/catalog: there is no
// KB in the path. Visibility is a per-row SQL predicate,
// kbaccess.VisibleKBsCTE, the SQL mirror of kbaccess.EffectiveRole; a kb_id
// the caller cannot see is 404.
func registerSearchRoutes(rc *routeCtx, searchRL *middleware.RedisRateLimiter, store globalsearch.Store) {
	searchHandler := globalsearch.NewHandler(store)
	rc.mux.Handle("GET /api/search",
		rc.authMw.Authenticate(searchRL.Middleware(http.HandlerFunc(searchHandler.Search))))
}
