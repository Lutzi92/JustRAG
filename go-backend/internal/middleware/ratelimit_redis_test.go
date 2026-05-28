package middleware

import (
	"net/http"
	"net/http/httptest"
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
