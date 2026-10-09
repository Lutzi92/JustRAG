package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestRedisRateLimiter_UnderLimit(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	rl := NewRedisRateLimiter(rdb, RedisRateLimitConfig{
		Max:      3,
		Window:   time.Minute,
		Category: "test",
	})

	handler := rl.Middleware(okHandler())

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}
}

func TestRedisRateLimiter_OverLimit(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	rl := NewRedisRateLimiter(rdb, RedisRateLimitConfig{
		Max:      2,
		Window:   time.Minute,
		Category: "test",
	})

	handler := rl.Middleware(okHandler())

	// First two should pass.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// Third should be blocked.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}

func TestRedisRateLimiter_NilRedis_PassThrough(t *testing.T) {
	rl := NewRedisRateLimiter(nil, RedisRateLimitConfig{
		Max:      1,
		Window:   time.Minute,
		Category: "test",
	})

	handler := rl.Middleware(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (pass-through), got %d", rec.Code)
	}
}

func TestRedisRateLimiter_NilRedis_FailClosed(t *testing.T) {
	rl := NewRedisRateLimiter(nil, RedisRateLimitConfig{
		Max:        1,
		Window:     time.Minute,
		Category:   "test",
		FailClosed: true,
	})

	handler := rl.Middleware(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestRedisRateLimiter_SeparateCategories(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	rl1 := NewRedisRateLimiter(rdb, RedisRateLimitConfig{
		Max:      1,
		Window:   time.Minute,
		Category: "cat1",
	})
	rl2 := NewRedisRateLimiter(rdb, RedisRateLimitConfig{
		Max:      1,
		Window:   time.Minute,
		Category: "cat2",
	})

	h1 := rl1.Middleware(okHandler())
	h2 := rl2.Middleware(okHandler())

	// Both should pass once.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h1.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cat1 first: expected 200, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cat2 first: expected 200, got %d", rec.Code)
	}

	// cat1 should now be blocked; cat2 should also be blocked.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	h1.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("cat1 second: expected 429, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("cat2 second: expected 429, got %d", rec.Code)
	}
}

// RedisRateLimitConfig.Key. Oracle: the field's documented contract, not the
// limiter's output — one budget per key value, an empty key falls back to the
// client IP, nil keeps the IP key and the IP-worded body every existing
// limiter has. The counters are read back from miniredis under the
// documented "rl:<category>:<key>" names, so a key that silently stayed the
// IP would show up there, not just in a status code.
func TestRedisRateLimiter_KeyFunc(t *testing.T) {
	const ipBody = `{"error":"Too many requests from this IP, please try again later."}`
	const keyedBody = `{"error":"Too many requests, please try again later."}`

	send := func(h http.Handler, remoteAddr, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remoteAddr
		if user != "" {
			req.Header.Set("X-Test-User", user)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	newLimiter := func(t *testing.T, key func(*http.Request) string) (http.Handler, *miniredis.Miniredis) {
		t.Helper()
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		rl := NewRedisRateLimiter(rdb, RedisRateLimitConfig{Max: 1, Window: time.Minute, Category: "test", Key: key})
		return rl.Middleware(okHandler()), mr
	}
	byUser := func(r *http.Request) string {
		if u := r.Header.Get("X-Test-User"); u != "" {
			return "user:" + u
		}
		return ""
	}

	t.Run("one budget per key, not per IP", func(t *testing.T) {
		h, mr := newLimiter(t, byUser)
		// Two users behind the same IP each get their own budget of 1.
		for _, u := range []string{"alice", "bob"} {
			if rec := send(h, "192.0.2.1:1000", u); rec.Code != http.StatusOK {
				t.Fatalf("%s first request: %d, want 200", u, rec.Code)
			}
		}
		for _, u := range []string{"alice", "bob"} {
			rec := send(h, "192.0.2.1:1000", u)
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("%s second request: %d, want 429", u, rec.Code)
			}
			if got := rec.Body.String(); got != keyedBody {
				t.Errorf("%s 429 body = %s, want %s (a per-user budget must not blame the IP)", u, got, keyedBody)
			}
		}
		// The same user from another IP is still over budget: the key, not
		// the IP, is what is counted.
		if rec := send(h, "198.51.100.7:1000", "alice"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("alice from another IP: %d, want 429", rec.Code)
		}
		assertKeys(t, mr, "rl:test:user:alice", "rl:test:user:bob")
	})

	t.Run("empty key falls back to the IP", func(t *testing.T) {
		h, mr := newLimiter(t, byUser)
		if rec := send(h, "192.0.2.1:1000", ""); rec.Code != http.StatusOK {
			t.Fatalf("first: %d, want 200", rec.Code)
		}
		if rec := send(h, "192.0.2.1:2000", ""); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second from the same IP: %d, want 429", rec.Code)
		}
		assertKeys(t, mr, "rl:test:192.0.2.1")
	})

	t.Run("nil key keeps the IP default", func(t *testing.T) {
		h, mr := newLimiter(t, nil)
		if rec := send(h, "192.0.2.1:1000", "alice"); rec.Code != http.StatusOK {
			t.Fatalf("first: %d, want 200", rec.Code)
		}
		rec := send(h, "192.0.2.1:2000", "bob")
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("another user from the same IP: %d, want 429", rec.Code)
		}
		if got := rec.Body.String(); got != ipBody {
			t.Errorf("429 body = %s, want the unchanged %s", got, ipBody)
		}
		assertKeys(t, mr, "rl:test:192.0.2.1")
	})
}

func assertKeys(t *testing.T, mr *miniredis.Miniredis, want ...string) {
	t.Helper()
	got := mr.Keys() // sorted
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("redis keys = %v, want %v", got, want)
	}
}
