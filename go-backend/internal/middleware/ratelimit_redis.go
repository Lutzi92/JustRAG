package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/observability"
)

// rlIncrScript atomically increments a rate-limit key and sets its expiry on
// creation. KEYS[1] is the counter key; ARGV[1] is the window in milliseconds.
// Returns the new count.
var rlIncrScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
	redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return count
`)

// rlGetScript reads the current counter without incrementing. Used by
// CheckOnly so the read path doesn't consume the rate-limit budget.
var rlGetScript = redis.NewScript(`return tonumber(redis.call("GET", KEYS[1]) or "0")`)

// rlSlidingCardScript trims entries older than the window and returns the
// current count without inserting anything. Used by the sliding-window
// CheckOnly path (login). KEYS[1] is the sorted-set key; ARGV[1] is the
// current time in ms; ARGV[2] is the window length in ms.
var rlSlidingCardScript = redis.NewScript(`
local cutoff = tonumber(ARGV[1]) - tonumber(ARGV[2])
redis.call("ZREMRANGEBYSCORE", KEYS[1], 0, cutoff)
return redis.call("ZCARD", KEYS[1])
`)

// rlSlidingAddScript trims expired entries, appends a new (now, member)
// pair, refreshes the TTL, and returns the new count. ARGV[3] is a unique
// member name supplied by the caller — using only the timestamp would
// drop concurrent attempts that share a millisecond.
var rlSlidingAddScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local cutoff = now - window
redis.call("ZREMRANGEBYSCORE", KEYS[1], 0, cutoff)
redis.call("ZADD", KEYS[1], now, ARGV[3])
redis.call("PEXPIRE", KEYS[1], window)
return redis.call("ZCARD", KEYS[1])
`)

// RateLimitMode selects the counting strategy for a RedisRateLimiter.
// Zero value (ModeFixedWindow) preserves the prior behaviour so existing
// call sites don't have to opt in.
type RateLimitMode int

const (
	// ModeFixedWindow uses a single counter per Window. Cheap, but two
	// bursts on opposite sides of a window boundary can together hit
	// 2*Max within seconds. Fine for chat / generate / api buckets
	// where the cap is generous.
	ModeFixedWindow RateLimitMode = iota
	// ModeSlidingWindow tracks each request as a sorted-set entry and
	// counts only entries within the last Window. Pays a small ZADD +
	// ZREMRANGEBYSCORE per recorded event but eliminates the boundary
	// burst — use for security-sensitive buckets like login where the
	// 2x worst case meaningfully changes the attacker's success rate.
	ModeSlidingWindow
)

// RedisRateLimitConfig configures a per-category Redis-backed rate limiter.
type RedisRateLimitConfig struct {
	Max      int           // maximum requests allowed in the window
	Window   time.Duration // window duration
	Category string        // key namespace, e.g. "login", "chat"
	Mode     RateLimitMode // counting strategy; default ModeFixedWindow

	// FailClosed flips the Redis-outage policy. Default false = fail open:
	// when the Redis call errors (or the client is nil), the request is
	// allowed through, recordFailOpen increments the Prometheus fallback
	// counter, and a warn line is emitted with category + error. Set true
	// to fail closed (503 instead). Fail-open is the right default for a
	// rate limiter — denying all traffic during a Redis blip is worse than
	// briefly skipping the cap — but it does mean operators must watch the
	// fallback metric to notice an outage.
	FailClosed bool
}

// RedisRateLimiter implements fixed-window rate limiting backed by Redis.
// Each (category, IP) pair gets its own counter key with an expiring window.
// Note: this is a fixed-window counter, not a true sliding window — requests
// near a window boundary may see up to 2x the limit in a short burst.
type RedisRateLimiter struct {
	rdb    redis.Cmdable
	config RedisRateLimitConfig
}

// NewRedisRateLimiter creates a rate limiter. If rdb is nil, behaviour depends
// on FailClosed: open → all requests pass; closed → all requests are rejected
// with 503.
func NewRedisRateLimiter(rdb redis.Cmdable, cfg RedisRateLimitConfig) *RedisRateLimiter {
	return &RedisRateLimiter{rdb: rdb, config: cfg}
}

// Middleware returns an http.Handler that enforces the rate limit before
// delegating to next. Every request increments the counter. When Redis is
// unavailable behaviour is governed by FailClosed — see the field doc on
// RedisRateLimitConfig; default is fail open with a metric + warn log.
func (rl *RedisRateLimiter) Middleware(next http.Handler) http.Handler {
	window := rl.config.Window

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rl.rdb == nil {
			if rl.config.FailClosed {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, "Rate limiter unavailable")
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		ip := extractIP(r)
		key := "rl:" + rl.config.Category + ":" + ip

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		// Atomic INCR + EXPIRE via Lua to avoid the race where EXPIRE
		// could fail after INCR, leaving the key without a TTL.
		count, err := rlIncrScript.Run(ctx, rl.rdb, []string{key}, window.Milliseconds()).Int64()
		if err != nil {
			if rl.config.FailClosed {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, "Rate limiter unavailable")
				return
			}
			rl.recordFailOpen(r.Context(), err)
			next.ServeHTTP(w, r)
			return
		}

		if int(count) > rl.config.Max {
			logRateLimitRejection(r, ip, "redis:"+rl.config.Category, int(count), rl.config.Max)
			rl.writeRetryAfter(w, r, key, window)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// recordFailOpen instruments a Redis-side fail-open: increments the counter
// and emits a single warn line so a Redis outage that silently disables rate
// limiting is observable instead of invisible.
func (rl *RedisRateLimiter) recordFailOpen(ctx context.Context, err error) {
	observability.RecordRateLimitFallback(rl.config.Category)
	slog.WarnContext(ctx, "redis rate limiter unavailable, failing open",
		"category", rl.config.Category, "error", err)
}

// CheckOnly returns an http.Handler that checks the current count without
// incrementing. Use with RecordFailure to count only failed attempts. The
// same FailClosed policy applies as Middleware (default fail open).
//
// Dispatches to the fixed- or sliding-window script based on
// config.Mode; the sorted-set path costs one extra ZREMRANGEBYSCORE per
// request but lets the rejection boundary track the actual look-back
// instead of a coarse window edge.
func (rl *RedisRateLimiter) CheckOnly(next http.Handler) http.Handler {
	window := rl.config.Window

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rl.rdb == nil {
			if rl.config.FailClosed {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, "Rate limiter unavailable")
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		ip := extractIP(r)
		key := "rl:" + rl.config.Category + ":" + ip

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		var (
			count int64
			err   error
		)
		switch rl.config.Mode {
		case ModeSlidingWindow:
			count, err = rlSlidingCardScript.Run(ctx, rl.rdb, []string{key},
				time.Now().UnixMilli(), window.Milliseconds()).Int64()
		default:
			count, err = rlGetScript.Run(ctx, rl.rdb, []string{key}).Int64()
		}
		if err != nil {
			if rl.config.FailClosed {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, "Rate limiter unavailable")
				return
			}
			rl.recordFailOpen(r.Context(), err)
			next.ServeHTTP(w, r)
			return
		}

		if int(count) >= rl.config.Max {
			logRateLimitRejection(r, ip, "redis_check_only:"+rl.config.Category, int(count), rl.config.Max)
			rl.writeRetryAfter(w, r, key, window)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// RecordFailure increments the rate-limit counter for the given request's IP.
// Call this after a failed login attempt rather than on every request.
//
// Sliding-window mode appends a unique sorted-set member per attempt and
// trims expired entries; using only the millisecond timestamp as the
// member would silently drop concurrent failures that share a tick, so
// we mix in 8 bytes of crypto-random to keep each attempt distinct.
func (rl *RedisRateLimiter) RecordFailure(r *http.Request) {
	if rl.rdb == nil {
		return
	}
	window := rl.config.Window
	ip := extractIP(r)
	key := "rl:" + rl.config.Category + ":" + ip

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	// Best-effort write: a Redis hiccup here just means one failed login
	// won't be counted, which the next failure (or a healthy Redis) recovers.
	// Route the error through recordFailOpen so a Redis outage during login
	// failure-counting surfaces in metrics + logs instead of going silent —
	// matches the symmetry of the request-path Middleware.
	var err error
	switch rl.config.Mode {
	case ModeSlidingWindow:
		now := time.Now().UnixMilli()
		err = rlSlidingAddScript.Run(ctx, rl.rdb, []string{key},
			now, window.Milliseconds(), uniqueMember(now)).Err()
	default:
		err = rlIncrScript.Run(ctx, rl.rdb, []string{key}, window.Milliseconds()).Err()
	}
	if err != nil {
		rl.recordFailOpen(r.Context(), err)
	}
}

// uniqueMember returns a sorted-set member that won't collide with
// concurrent attempts in the same millisecond. crypto/rand is overkill
// for uniqueness alone but keeps the bytes unpredictable so a Redis
// observer can't reconstruct the attempt timeline by inspecting members.
func uniqueMember(nowMs int64) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return strconv.FormatInt(nowMs, 10) + "-" + hex.EncodeToString(b[:])
}

// writeRetryAfter sends a 429 response with a Retry-After header.
func (rl *RedisRateLimiter) writeRetryAfter(w http.ResponseWriter, r *http.Request, key string, window time.Duration) {
	retrySec := int(window.Seconds())
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if ttl, err := rl.rdb.PTTL(ctx, key).Result(); err == nil && ttl > 0 {
		retrySec = int((ttl + time.Second - 1) / time.Second)
	}
	w.Header().Set("Retry-After", strconv.Itoa(retrySec))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write(rateLimitBody)
}
