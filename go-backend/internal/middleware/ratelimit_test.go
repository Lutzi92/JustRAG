package middleware_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/middleware"
)

func TestRateLimiter_AllowsUnderLimit(t *testing.T) {
	cfg := config.RateLimitConfig{Window: 60 * time.Second, Max: 5}
	limiter := middleware.NewRateLimiter(t.Context(), cfg)

	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := range 5 {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, rec.Code)
		}
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	cfg := config.RateLimitConfig{Window: 60 * time.Second, Max: 2}
	limiter := middleware.NewRateLimiter(t.Context(), cfg)

	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := range 3 {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if i < 2 && rec.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i, rec.Code)
		}
		if i == 2 && rec.Code != http.StatusTooManyRequests {
			t.Errorf("request %d: expected 429, got %d", i, rec.Code)
		}
	}
}

func TestRateLimiter_VisitorMapIsBounded(t *testing.T) {
	cfg := config.RateLimitConfig{Window: 60 * time.Second, Max: 1}
	limiter := middleware.NewRateLimiter(t.Context(), cfg)

	// Override the cap via the exported test hook so we exercise eviction
	// without queuing 100k requests.
	limiter.SetMaxVisitorsForTest(8)

	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := range 50 {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = fmt.Sprintf("10.0.%d.%d:1111", (i/255)+1, (i%255)+1)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("request %d from new IP: expected 200, got %d", i, rec.Code)
		}
	}

	if got := limiter.VisitorCountForTest(); got > 8 {
		t.Errorf("visitor map exceeded cap: got %d, want ≤ 8", got)
	}
}

func TestRateLimiter_IsolatesClients(t *testing.T) {
	cfg := config.RateLimitConfig{Window: 60 * time.Second, Max: 1}
	limiter := middleware.NewRateLimiter(t.Context(), cfg)

	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "10.0.0.1:1111"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "10.0.0.2:2222"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("expected second client to be allowed, got %d", rec2.Code)
	}
}

// TestRateLimiter_RedactsInviteTokenInRejectionLog guards against
// logRateLimitRejection's "path" field (ratelimit.go) regressing back to the
// raw r.URL.Path. This is the log an operator would export or paste into a
// ticket while investigating someone hammering a leaked invite link, so it
// must never carry the raw, permanent, non-expiring token.
//
// This test proves the CALL SITE redacts, not just that the helper works
// (TestRedactSecretPath in metrics_test.go already covers the helper in
// isolation). Mutation-tested: reverting the call site to r.URL.Path turns
// this RED (verified manually; see the second-fix report).
func TestRateLimiter_RedactsInviteTokenInRejectionLog(t *testing.T) {
	const token = "kR3x9vQ2mN8pL5wZ7bT1cY6dF0hJ4sA-eG_uI2oK9rV3nB5"
	path := "/api/invites/" + token + "/redeem"

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	cfg := config.RateLimitConfig{Window: 60 * time.Second, Max: 1}
	limiter := middleware.NewRateLimiter(t.Context(), cfg)

	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request establishes the bucket.
	req1 := httptest.NewRequest("POST", path, nil)
	req1.RemoteAddr = "192.168.9.9:12345"
	handler.ServeHTTP(httptest.NewRecorder(), req1)

	// Second request from the same IP is rejected — this is what triggers
	// logRateLimitRejection.
	req2 := httptest.NewRequest("POST", path, nil)
	req2.RemoteAddr = "192.168.9.9:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req2)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}

	out := buf.String()
	if out == "" {
		t.Fatal("expected a rate-limit rejection log line, got none")
	}
	if strings.Contains(out, token) {
		t.Fatalf("rate-limit rejection log leaked the invite token verbatim: %s", out)
	}

	var logLine struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &logLine); err != nil {
		t.Fatalf("rejection log line is not valid JSON (%q): %v", out, err)
	}
	if logLine.Path != "/api/invites/{token}/redeem" {
		t.Errorf("path = %q, want %q", logLine.Path, "/api/invites/{token}/redeem")
	}
}

// TestRateLimiter_IgnoresXFFByDefault verifies that with TrustedProxyHops=0
// (the secure default) clients cannot escape rate limiting by setting their
// own X-Forwarded-For header — every request from the same TCP peer counts
// against the same bucket regardless of XFF.
func TestRateLimiter_IgnoresXFFByDefault(t *testing.T) {
	middleware.SetTrustedProxyHops(0)
	defer middleware.SetTrustedProxyHops(0) // restore default for sibling tests

	cfg := config.RateLimitConfig{Window: 60 * time.Second, Max: 1}
	limiter := middleware.NewRateLimiter(t.Context(), cfg)

	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request from .1 establishes the bucket.
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "10.0.0.1:1111"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rec1.Code)
	}

	// Same TCP peer sends a spoofed XFF — should still be limited.
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "10.0.0.1:1111"
	req2.Header.Set("X-Forwarded-For", "1.2.3.4")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("spoofed-XFF request: expected 429, got %d", rec2.Code)
	}
	if got := rec2.Header().Get("Retry-After"); got == "" {
		t.Errorf("expected Retry-After header on 429, got empty")
	}
}

// TestRateLimiter_HonorsXFFWithTrustedProxy verifies that with hops=1 the
// rightmost-but-one XFF entry is used as the client identity, which is the
// standard convention for "single trusted reverse proxy in front".
func TestRateLimiter_HonorsXFFWithTrustedProxy(t *testing.T) {
	middleware.SetTrustedProxyHops(1)
	defer middleware.SetTrustedProxyHops(0) // restore default for sibling tests

	cfg := config.RateLimitConfig{Window: 60 * time.Second, Max: 1}
	limiter := middleware.NewRateLimiter(t.Context(), cfg)

	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Two requests with the same TCP peer (the proxy) but distinct
	// client IPs in XFF — both should succeed because each is a
	// different rate-limit identity.
	for _, clientIP := range []string{"203.0.113.10", "203.0.113.20"} {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.99:443" // proxy
		req.Header.Set("X-Forwarded-For", clientIP)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("client %s: expected 200, got %d", clientIP, rec.Code)
		}
	}

	// Same XFF client repeats — exhausts that bucket.
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "10.0.0.99:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("repeat client: expected 429, got %d", rec.Code)
	}
}
