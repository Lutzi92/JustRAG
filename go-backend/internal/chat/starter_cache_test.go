package chat

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a settable time source for the cache's expiry checks.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestStarterCache(maxEntries int, ttl, negTTL time.Duration) (*starterCache, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	c := newStarterCache(maxEntries, ttl, negTTL, time.Second)
	c.now = clk.now
	return c, clk
}

// asLoad adapts a plain question loader to the cache's load signature (a
// non-degraded outcome).
func asLoad(fn func(context.Context) ([]string, error)) func(context.Context) (starterOutcome, error) {
	return func(ctx context.Context) (starterOutcome, error) {
		q, err := fn(ctx)
		return starterOutcome{questions: q}, err
	}
}

// countingLoad returns a load func that answers []string{key} and counts its
// invocations per key.
func countingLoad(calls map[string]int, mu *sync.Mutex, key string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) {
		mu.Lock()
		calls[key]++
		mu.Unlock()
		return []string{key}, nil
	}
}

// Oracle: the size cap and LRU order are fixed by the call sequence below —
// with room for two, touching "a" before inserting "c" must evict "b", and
// "a" must still be served without a load.
func TestStarterCache_BoundedLRU(t *testing.T) {
	c, _ := newTestStarterCache(2, time.Hour, time.Minute)
	calls, mu := map[string]int{}, &sync.Mutex{}
	ctx := context.Background()
	get := func(k string) {
		if _, err := c.get(ctx, k, asLoad(countingLoad(calls, mu, k))); err != nil {
			t.Fatalf("get %s: %v", k, err)
		}
	}

	get("a")
	get("b")
	get("a") // hit: a becomes most recently used
	get("c") // evicts b, the least recently used
	if c.size() != 2 {
		t.Fatalf("cache holds %d entries, want the cap of 2", c.size())
	}
	get("a")
	get("b")
	if calls["a"] != 1 || calls["b"] != 2 || calls["c"] != 1 {
		t.Fatalf("loads %v, want a=1 (kept), b=2 (evicted, reloaded), c=1", calls)
	}
}

// Oracle: the two TTLs set on the cache. A success lives for ttl; a failure
// is answered from the cache (errStarterCachedFailure, no load) until negTTL
// passes, which is much shorter than ttl.
func TestStarterCache_TTLAndNegativeCaching(t *testing.T) {
	c, clk := newTestStarterCache(10, time.Hour, time.Minute)
	ctx := context.Background()
	var loads int
	boom := errors.New("model down")
	failing := func(context.Context) ([]string, error) { loads++; return nil, boom }
	working := func(context.Context) ([]string, error) { loads++; return []string{"q?"}, nil }

	if _, err := c.get(ctx, "k", asLoad(failing)); !errors.Is(err, boom) {
		t.Fatalf("first failure: got %v, want %v", err, boom)
	}
	if _, err := c.get(ctx, "k", asLoad(working)); !errors.Is(err, errStarterCachedFailure) || loads != 1 {
		t.Fatalf("within negTTL: got %v after %d loads, want the cached failure and 1 load", err, loads)
	}
	clk.advance(time.Minute + time.Second)
	if q, err := c.get(ctx, "k", asLoad(working)); err != nil || len(q) != 1 || loads != 2 {
		t.Fatalf("after negTTL: got %v, %v after %d loads, want a fresh load", q, err, loads)
	}
	clk.advance(59 * time.Minute)
	if _, err := c.get(ctx, "k", asLoad(failing)); err != nil || loads != 2 {
		t.Fatalf("within ttl: got %v after %d loads, want a hit", err, loads)
	}
	clk.advance(2 * time.Minute)
	if _, err := c.get(ctx, "k", asLoad(working)); err != nil || loads != 3 {
		t.Fatalf("after ttl: got %v after %d loads, want a reload", err, loads)
	}
}

// Oracle: a load counter. With caching disabled (zero TTLs) only singleflight
// can keep identical concurrent callers from each loading: all of them join
// before the single load is released (afterJoin), so exactly one load may run
// and every caller must get its result.
func TestStarterCache_SingleflightCollapsesConcurrentMisses(t *testing.T) {
	c, _ := newTestStarterCache(10, 0, 0)
	const callers = 8
	var joined sync.WaitGroup
	joined.Add(callers)
	c.afterJoin = joined.Done

	release := make(chan struct{})
	var loads atomic.Int32
	load := func(context.Context) ([]string, error) {
		loads.Add(1)
		<-release
		return []string{"q?"}, nil
	}

	var done sync.WaitGroup
	results := make([][]string, callers)
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			results[i], _ = c.get(context.Background(), "k", asLoad(load))
		}()
	}
	joined.Wait()
	close(release)
	done.Wait()

	if n := loads.Load(); n != 1 {
		t.Fatalf("%d loads for %d concurrent callers, want 1", n, callers)
	}
	for i, r := range results {
		if len(r) != 1 || r[0] != "q?" {
			t.Fatalf("caller %d got %v", i, r)
		}
	}
}

// Oracle: the shared result must not alias between callers — a caller that
// edits its slice must not change what the next caller is served.
func TestStarterCache_ReturnsCopies(t *testing.T) {
	c, _ := newTestStarterCache(10, time.Hour, time.Minute)
	ctx := context.Background()
	load := func(context.Context) ([]string, error) { return []string{"q?"}, nil }
	first, _ := c.get(ctx, "k", asLoad(load))
	first[0] = "mutated"
	if second, _ := c.get(ctx, "k", asLoad(load)); second[0] != "q?" {
		t.Fatalf("cached entry was mutated through a returned slice: %q", second[0])
	}
}

// Oracle: a panicking load must come back as an error (and be negatively
// cached), never as a process-wide panic from singleflight's DoChan.
func TestStarterCache_PanicBecomesError(t *testing.T) {
	c, _ := newTestStarterCache(10, time.Hour, time.Minute)
	ctx := context.Background()
	if _, err := c.get(ctx, "k", asLoad(func(context.Context) ([]string, error) { panic("boom") })); err == nil {
		t.Fatal("want an error from a panicking load")
	}
	if _, err := c.get(ctx, "k", asLoad(func(context.Context) ([]string, error) { return []string{"q?"}, nil })); !errors.Is(err, errStarterCachedFailure) {
		t.Fatalf("want the panic negatively cached, got %v", err)
	}
}

// Oracle: the negTTL set on the cache. A degraded success is served like any
// other, but must be reloaded once negTTL has passed — long before ttl.
func TestStarterCache_DegradedResultCachedBriefly(t *testing.T) {
	c, clk := newTestStarterCache(10, time.Hour, time.Minute)
	ctx := context.Background()
	var loads int
	degraded := func(context.Context) (starterOutcome, error) {
		loads++
		return starterOutcome{questions: []string{"q?"}, degraded: true}, nil
	}
	if q, err := c.get(ctx, "k", degraded); err != nil || len(q) != 1 {
		t.Fatalf("degraded result not served: %v, %v", q, err)
	}
	if _, err := c.get(ctx, "k", degraded); err != nil || loads != 1 {
		t.Fatalf("within negTTL: %v after %d loads, want a hit", err, loads)
	}
	clk.advance(time.Minute + time.Second)
	if _, err := c.get(ctx, "k", degraded); err != nil || loads != 2 {
		t.Fatalf("after negTTL: %v after %d loads, want a reload", err, loads)
	}
}
