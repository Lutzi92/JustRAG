package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/safego"
)

// IPExtractor resolves a request's client IP from RemoteAddr +
// optionally X-Forwarded-For / X-Real-IP. trustedProxyHops controls how
// many XFF entries on the right were inserted by trusted proxies. With
// value N, the client IP is taken from XFF[len-N] (the N-th entry from
// the right, 1-indexed — the leftmost of the trusted-inserted suffix).
// With value 0, XFF and X-Real-IP are ignored entirely and only the
// TCP-level RemoteAddr is used — preventing clients from spoofing the
// rate-limit key by setting their own X-Forwarded-For header.
//
// The hop count is atomic so SetHops is safe to call from any goroutine.
type IPExtractor struct {
	trustedProxyHops atomic.Int32
}

// NewIPExtractor returns an extractor configured with hops trusted
// proxies in front of this server. n ≤ 0 disables XFF/X-Real-IP trust.
// Behind a single reverse proxy (nginx, ALB, Cloudflare → server),
// pass 1.
func NewIPExtractor(hops int) *IPExtractor {
	e := &IPExtractor{}
	e.SetHops(hops)
	return e
}

// SetHops updates the trusted-proxy hop count. Safe to call at any time
// but typically invoked once at startup.
func (e *IPExtractor) SetHops(n int) {
	if n < 0 {
		n = 0
	}
	e.trustedProxyHops.Store(int32(n))
}

// defaultIPExtractor is the package-wide extractor that the in-memory
// and Redis rate limiters consult by default. Operators configure it
// once at server startup via SetTrustedProxyHops; future code that
// needs a private hop policy can construct its own IPExtractor via
// NewIPExtractor without touching this global.
var defaultIPExtractor = &IPExtractor{}

// SetTrustedProxyHops configures the package-wide default extractor.
// Must be called once at startup. n ≤ 0 disables XFF/X-Real-IP trust
// (RemoteAddr only). Behind a single reverse proxy, set to 1.
func SetTrustedProxyHops(n int) {
	defaultIPExtractor.SetHops(n)
}

type visitor struct {
	count       int
	windowStart time.Time
}

// defaultMaxVisitors caps the in-memory rate-limiter map between the
// periodic cleanup ticks. A pathological client population (e.g. a botnet
// or a forwarder leaking unique X-Forwarded-For values) can otherwise grow
// this map unbounded for a full window before the ticker fires. The cap
// triggers an opportunistic cleanup + eviction when reached so memory is
// always bounded regardless of cleanup cadence. For high-traffic public
// services where the cap matters in normal operation, prefer the
// Redis-backed limiter (ratelimit_redis.go) which scales horizontally.
const defaultMaxVisitors = 100_000

type RateLimiter struct {
	mu          sync.RWMutex
	visitors    map[string]*visitor
	window      time.Duration
	max         int
	maxVisitors int
	cancel      context.CancelFunc
}

// rateLimitBody is the pre-encoded JSON response for 429 errors.
var rateLimitBody = []byte(`{"error":"Too many requests from this IP, please try again later."}`)

// NewRateLimiter creates a rate limiter whose cleanup goroutine runs until
// Shutdown is called or ctx is cancelled. Pass context.Background when the
// caller manages the lifetime via Shutdown alone.
func NewRateLimiter(ctx context.Context, cfg config.RateLimitConfig) *RateLimiter {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	rl := &RateLimiter{
		visitors:    make(map[string]*visitor),
		window:      cfg.Window,
		max:         cfg.Max,
		maxVisitors: defaultMaxVisitors,
		cancel:      cancel,
	}

	safego.Go(func() {
		ticker := time.NewTicker(rl.window)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rl.mu.Lock()
				now := time.Now()
				for ip, v := range rl.visitors {
					if now.Sub(v.windowStart) > rl.window {
						delete(rl.visitors, ip)
					}
				}
				rl.mu.Unlock()
			}
		}
	})

	return rl
}

// Shutdown stops the cleanup goroutine.
func (rl *RateLimiter) Shutdown() {
	rl.cancel()
}

// SetMaxVisitorsForTest overrides the visitor-map size cap. Test-only —
// production code uses defaultMaxVisitors set at construction.
func (rl *RateLimiter) SetMaxVisitorsForTest(n int) {
	rl.mu.Lock()
	rl.maxVisitors = n
	rl.mu.Unlock()
}

// VisitorCountForTest returns the current size of the visitor map. Test-only.
func (rl *RateLimiter) VisitorCountForTest() int {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	return len(rl.visitors)
}

// evictIfFullLocked drops expired entries when the visitor map has reached
// its size cap, and as a last resort evicts the single oldest live entry
// so a brand-new visitor can always be inserted. Caller must hold rl.mu.
//
// Eviction policy:
//  1. If len < maxVisitors, no-op.
//  2. Drop every entry whose window has expired (same predicate as the
//     periodic cleanup goroutine, just runs early).
//  3. If still at cap, drop the entry with the oldest windowStart. This
//     is O(n) per overflow eviction but only fires after step 2 fails to
//     reclaim space, which means every entry is currently rate-limited
//     in-window — a pathological burst of unique IPs, not normal traffic.
func (rl *RateLimiter) evictIfFullLocked(now time.Time) {
	if len(rl.visitors) < rl.maxVisitors {
		return
	}
	for ip, v := range rl.visitors {
		if now.Sub(v.windowStart) > rl.window {
			delete(rl.visitors, ip)
		}
	}
	if len(rl.visitors) < rl.maxVisitors {
		return
	}
	var oldestIP string
	var oldestStart time.Time
	first := true
	for ip, v := range rl.visitors {
		if first || v.windowStart.Before(oldestStart) {
			oldestIP = ip
			oldestStart = v.windowStart
			first = false
		}
	}
	if oldestIP != "" {
		delete(rl.visitors, oldestIP)
	}
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)

		rl.mu.Lock()
		v, exists := rl.visitors[ip]
		now := time.Now()

		if !exists || now.Sub(v.windowStart) > rl.window {
			rl.evictIfFullLocked(now)
			rl.visitors[ip] = &visitor{count: 1, windowStart: now}
			rl.mu.Unlock()
			next.ServeHTTP(w, r)
			return
		}

		v.count++
		if v.count > rl.max {
			count := v.count
			retryAfter := rl.windowEndsAfterLocked(v, now)
			rl.mu.Unlock()
			logRateLimitRejection(r, ip, "memory", count, rl.max)
			writeMemoryRateLimit429(w, retryAfter)
			return
		}

		rl.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// windowEndsAfterLocked returns the time remaining in the visitor's current
// window. Caller must hold rl.mu (read or write). Floors at 1 second so the
// Retry-After header isn't 0 — RFC 6585 expects a hint, not a tautology.
func (rl *RateLimiter) windowEndsAfterLocked(v *visitor, now time.Time) time.Duration {
	remaining := v.windowStart.Add(rl.window).Sub(now)
	if remaining < time.Second {
		return time.Second
	}
	return remaining
}

// writeMemoryRateLimit429 sends the standard 429 response with a Retry-After
// header so RFC-6585-compliant clients can back off correctly. retryAfter is
// rounded up to whole seconds.
func writeMemoryRateLimit429(w http.ResponseWriter, retryAfter time.Duration) {
	secs := max(int((retryAfter+time.Second-1)/time.Second), 1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write(rateLimitBody)
}

// logRateLimitRejection emits a single warn line per 429 so operators can
// correlate misbehaving IPs with the path/category they're hitting, and bumps
// the per-limiter rejection counter so the same signal is queryable as a time
// series for abuse detection / capacity planning. Pulled out so both the
// in-memory and Redis limiters share the same log + metric shape.
func logRateLimitRejection(r *http.Request, ip, source string, count, max int) {
	observability.RecordRateLimitRejected(source)
	logctx.From(r.Context()).Warn("middleware.ratelimit.rejected",
		slog.String("ip", ip),
		slog.String("source", source),
		slog.String("path", r.URL.Path),
		slog.String("method", r.Method),
		slog.Int("count", count),
		slog.Int("limit", max),
	)
}

// CheckOnly returns an http.Handler that checks the current count without
// incrementing. Use with RecordFailure to count only failed attempts.
func (rl *RateLimiter) CheckOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)

		rl.mu.RLock()
		v, exists := rl.visitors[ip]
		now := time.Now()

		if !exists || now.Sub(v.windowStart) > rl.window {
			rl.mu.RUnlock()
			next.ServeHTTP(w, r)
			return
		}

		if v.count >= rl.max {
			count := v.count
			retryAfter := rl.windowEndsAfterLocked(v, now)
			rl.mu.RUnlock()
			logRateLimitRejection(r, ip, "memory_check_only", count, rl.max)
			writeMemoryRateLimit429(w, retryAfter)
			return
		}

		rl.mu.RUnlock()
		next.ServeHTTP(w, r)
	})
}

// RecordFailure increments the rate-limit counter for the given request's IP.
// Call this after a failed login attempt rather than on every request.
func (rl *RateLimiter) RecordFailure(r *http.Request) {
	ip := extractIP(r)
	now := time.Now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, exists := rl.visitors[ip]
	if !exists || now.Sub(v.windowStart) > rl.window {
		rl.evictIfFullLocked(now)
		rl.visitors[ip] = &visitor{count: 1, windowStart: now}
		return
	}
	v.count++
}

func (rl *RateLimiter) MiddlewareForPrefix(prefix string, next http.Handler) http.Handler {
	// Wrap once — not per-request.
	limited := rl.Middleware(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, prefix) {
			limited.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractIP returns the client IP for r using the package-wide default
// extractor. Code paths that need a private hop policy should construct
// their own IPExtractor via NewIPExtractor and call ExtractIP directly.
func extractIP(r *http.Request) string {
	return defaultIPExtractor.ExtractIP(r)
}

// ExtractIP returns the client IP for r honoring e's trusted-proxy
// configuration.
func (e *IPExtractor) ExtractIP(r *http.Request) string {
	hops := int(e.trustedProxyHops.Load())

	// When no proxies are trusted, XFF / X-Real-IP are client-controlled
	// and must be ignored — using them would let any client spoof the
	// rate-limit key. Fall straight through to the TCP peer. If a forwarding
	// header is nonetheless present, the server is likely behind a proxy it
	// hasn't been told about, so the rate-limit / audit key is the proxy IP,
	// not the client — bump a counter so that silent misconfiguration shows
	// up on the dashboard instead of only in the one-shot startup warning.
	if hops <= 0 {
		if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" {
			observability.RecordXFFUntrusted()
		}
	}
	if hops > 0 {
		// X-Forwarded-For is appended-to by each proxy, so the rightmost
		// `hops` entries were inserted by trusted intermediaries. The
		// hops-th entry from the right (1-indexed) — i.e. parts[len-hops],
		// the leftmost of those trusted-inserted entries — is the real
		// client IP.
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if idx := len(parts) - hops; idx >= 0 && idx < len(parts) {
				candidate := strings.TrimSpace(parts[idx])
				if ip := net.ParseIP(candidate); ip != nil {
					return ip.String()
				}
			}
			// Chain shorter than expected (proxy didn't add an entry, or
			// client passed fewer entries than the hop count expects).
			// Fall through to X-Real-IP / RemoteAddr rather than trusting
			// a header value that didn't come from a known position.
		}

		// X-Real-IP is single-valued and only meaningful when set by a
		// trusted proxy — exposed only when hops > 0 for the same reason.
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			if ip := net.ParseIP(strings.TrimSpace(xri)); ip != nil {
				return ip.String()
			}
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
