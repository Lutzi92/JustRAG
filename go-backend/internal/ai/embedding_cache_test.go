package ai

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestCache(t *testing.T) (*EmbeddingCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return NewEmbeddingCache(rdb, 1*time.Hour), mr
}

func TestEmbeddingCache_Miss(t *testing.T) {
	cache, _ := newTestCache(t)
	ctx := context.Background()

	vec, ok := cache.Get(ctx, "model-a", "hello world")
	if ok {
		t.Fatal("expected cache miss, got hit")
	}
	if vec != nil {
		t.Fatalf("expected nil vector on miss, got %v", vec)
	}
}

func TestEmbeddingCache_SetThenGet(t *testing.T) {
	cache, _ := newTestCache(t)
	ctx := context.Background()

	want := []float64{0.1, 0.2, 0.3, 0.4}
	cache.Set(ctx, "model-a", "hello world", want)

	got, ok := cache.Get(ctx, "model-a", "hello world")
	if !ok {
		t.Fatal("expected cache hit, got miss")
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d elements, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("element %d: want %f, got %f", i, want[i], got[i])
		}
	}
}

func TestEmbeddingCache_DifferentModel(t *testing.T) {
	cache, _ := newTestCache(t)
	ctx := context.Background()

	cache.Set(ctx, "model-a", "hello world", []float64{1, 2, 3})

	// Same text, different model should miss.
	vec, ok := cache.Get(ctx, "model-b", "hello world")
	if ok {
		t.Fatalf("expected miss for different model, got hit with %v", vec)
	}
}

func TestEmbeddingCache_GetMany(t *testing.T) {
	cache, _ := newTestCache(t)
	ctx := context.Background()

	a := []float64{0.1, 0.2, 0.3}
	c := []float64{0.7, 0.8, 0.9}
	cache.Set(ctx, "model-a", "alpha", a)
	cache.Set(ctx, "model-a", "gamma", c)

	// "beta" is never stored — it must read back as a nil (miss) slot, while
	// hits decode to their stored vectors, all in one round-trip.
	got := cache.GetMany(ctx, "model-a", []string{"alpha", "beta", "gamma"})
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d", len(got))
	}
	if got[1] != nil {
		t.Errorf("expected miss (nil) for uncached text, got %v", got[1])
	}
	for i, want := range [][]float64{a, nil, c} {
		if want == nil {
			continue
		}
		if len(got[i]) != len(want) {
			t.Fatalf("slot %d: want %d elements, got %d", i, len(want), len(got[i]))
		}
		for j := range want {
			if got[i][j] != want[j] {
				t.Errorf("slot %d elem %d: want %f, got %f", i, j, want[j], got[i][j])
			}
		}
	}
}

func TestEmbeddingCache_GetMany_DifferentModelMisses(t *testing.T) {
	cache, _ := newTestCache(t)
	ctx := context.Background()

	cache.Set(ctx, "model-a", "alpha", []float64{1, 2, 3})

	got := cache.GetMany(ctx, "model-b", []string{"alpha"})
	if len(got) != 1 || got[0] != nil {
		t.Fatalf("expected single miss for different model, got %v", got)
	}
}

func TestEmbeddingCache_GetMany_NilAndEmpty(t *testing.T) {
	ctx := context.Background()

	var nilCache *EmbeddingCache
	if got := nilCache.GetMany(ctx, "m", []string{"a", "b"}); len(got) != 2 || got[0] != nil || got[1] != nil {
		t.Fatalf("nil cache: expected all-miss slice, got %v", got)
	}

	cache, _ := newTestCache(t)
	if got := cache.GetMany(ctx, "m", nil); len(got) != 0 {
		t.Fatalf("empty input: expected empty slice, got %v", got)
	}
}

func TestEmbeddingCache_NilRedis(t *testing.T) {
	cache := NewEmbeddingCache(nil, 1*time.Hour)
	ctx := context.Background()

	// Should not panic.
	cache.Set(ctx, "model", "text", []float64{1, 2})
	vec, ok := cache.Get(ctx, "model", "text")
	if ok {
		t.Fatal("expected miss with nil redis, got hit")
	}
	if vec != nil {
		t.Fatalf("expected nil vector with nil redis, got %v", vec)
	}
}

func TestEmbeddingCache_NilCache(t *testing.T) {
	var cache *EmbeddingCache
	ctx := context.Background()

	// Should not panic on nil receiver.
	cache.Set(ctx, "model", "text", []float64{1, 2})
	vec, ok := cache.Get(ctx, "model", "text")
	if ok {
		t.Fatal("expected miss with nil cache, got hit")
	}
	if vec != nil {
		t.Fatalf("expected nil vector with nil cache, got %v", vec)
	}
}
