//go:build integration

// Query-cache tests exercise the live pgvector store via TEST_VECTOR_DSN.
// Gated by the `integration` build tag so `go test ./...` without the tag
// stays runnable on machines without docker.

package vector

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// openTestVectorPool reads TEST_VECTOR_DSN and returns a pool against the
// vector DB. Skips the test when the env var is unset — keeps `go test ./...`
// green on dev machines without a vector DB while still letting CI run the
// integration tests. Callers don't need a nil check.
func openTestVectorPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_VECTOR_DSN")
	if dsn == "" {
		t.Skip("TEST_VECTOR_DSN unset; skipping vector integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New(TEST_VECTOR_DSN): %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newQueryCacheTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := openTestVectorPool(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "TRUNCATE query_cache")
	})
	return pool
}

func TestQueryCache_StoreThenLookupHit(t *testing.T) {
	pool := newQueryCacheTestPool(t)
	cache := NewQueryCache(pool)
	ctx := context.Background()
	kbID := "11111111-1111-1111-1111-111111111111"

	emb := make([]float64, 2560)
	for i := range emb {
		emb[i] = 1.0 / float64(i+1)
	}
	hash := []byte("01234567890123456789012345678901")
	resultBytes, _ := json.Marshal(map[string]string{"foo": "bar"})

	if err := cache.Store(ctx, kbID, hash, emb, "Wer ist der Ansprechpartner?", resultBytes, time.Hour); err != nil {
		t.Fatalf("Store: %v", err)
	}

	rj, hit, dist, err := cache.Lookup(ctx, kbID, hash, emb, 0.96)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !hit {
		t.Fatalf("expected hit immediately after store; got miss (distance=%v)", dist)
	}
	if string(rj) != string(resultBytes) {
		t.Errorf("result mismatch: want %s, got %s", resultBytes, rj)
	}
}

func TestQueryCache_LookupMissBelowThreshold(t *testing.T) {
	pool := newQueryCacheTestPool(t)
	cache := NewQueryCache(pool)
	ctx := context.Background()
	kbID := "22222222-2222-2222-2222-222222222222"

	emb := make([]float64, 2560)
	emb[0] = 1.0
	hash := []byte("01234567890123456789012345678901")
	if err := cache.Store(ctx, kbID, hash, emb, "q1", []byte(`{}`), time.Hour); err != nil {
		t.Fatalf("Store: %v", err)
	}

	other := make([]float64, 2560)
	other[1] = 1.0
	_, hit, _, err := cache.Lookup(ctx, kbID, hash, other, 0.96)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if hit {
		t.Errorf("orthogonal embedding should not hit (cosine distance 1.0 > 1-0.96)")
	}
}

func TestQueryCache_LookupMissOnExpired(t *testing.T) {
	pool := newQueryCacheTestPool(t)
	cache := NewQueryCache(pool)
	ctx := context.Background()
	kbID := "33333333-3333-3333-3333-333333333333"

	emb := make([]float64, 2560)
	emb[0] = 1.0
	hash := []byte("01234567890123456789012345678901")
	if err := cache.Store(ctx, kbID, hash, emb, "q", []byte(`{}`), -time.Hour); err != nil {
		t.Fatalf("Store: %v", err)
	}

	_, hit, _, err := cache.Lookup(ctx, kbID, hash, emb, 0.96)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if hit {
		t.Errorf("expired entry must not hit")
	}
}

func TestQueryCache_InvalidateByKB(t *testing.T) {
	pool := newQueryCacheTestPool(t)
	cache := NewQueryCache(pool)
	ctx := context.Background()
	kbA := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	kbB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	emb := make([]float64, 2560)
	emb[0] = 1.0
	hash := []byte("01234567890123456789012345678901")
	if err := cache.Store(ctx, kbA, hash, emb, "qa", []byte(`{}`), time.Hour); err != nil {
		t.Fatalf("Store kbA: %v", err)
	}
	if err := cache.Store(ctx, kbB, hash, emb, "qb", []byte(`{}`), time.Hour); err != nil {
		t.Fatalf("Store kbB: %v", err)
	}

	if err := cache.Invalidate(ctx, kbA); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	if _, hitA, _, _ := cache.Lookup(ctx, kbA, hash, emb, 0.96); hitA {
		t.Errorf("kbA should be invalidated")
	}
	if _, hitB, _, _ := cache.Lookup(ctx, kbB, hash, emb, 0.96); !hitB {
		t.Errorf("kbB should still be present")
	}
}
