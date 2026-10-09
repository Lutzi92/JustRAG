package chat

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// errStarterCachedFailure is returned while a recent failed generation for the
// same key is still negatively cached. The original error was logged when it
// happened.
var errStarterCachedFailure = errors.New("starter questions: recent generation failed")

// starterCache holds generated starter questions per key (KB, language and
// content fingerprint), so opening empty chats does not cost a model call
// each time. It is:
//
//   - bounded: at most maxEntries entries, least-recently-used evicted first;
//   - collapsing: identical concurrent misses share one load (singleflight);
//   - failure-aware: a failed load is remembered for negTTL, so a broken
//     model endpoint is not retried on every empty chat that opens.
//
// Process-local: every server replica keeps its own copy.
type starterCache struct {
	maxEntries int
	ttl        time.Duration
	negTTL     time.Duration
	// loadTimeout bounds a load, which runs on a context detached from the
	// first caller's: if that caller disconnects, the waiters it shares the
	// load with still get the result.
	loadTimeout time.Duration
	now         func() time.Time

	mu      sync.Mutex
	order   *list.List               // front = most recently used
	entries map[string]*list.Element // value: *starterCacheEntry
	flight  singleflight.Group

	// afterJoin, when set (tests only), runs after a caller has joined the
	// in-flight load for its key, before it waits for the result.
	afterJoin func()
}

// starterOutcome is what a successful load produced. degraded marks
// questions generated from partial input (e.g. file names only because the
// excerpt read failed): they are served, but cached only for negTTL so the
// full input is retried soon.
type starterOutcome struct {
	questions []string
	degraded  bool
}

type starterCacheEntry struct {
	key       string
	questions []string
	failed    bool
	expires   time.Time
}

func newStarterCache(maxEntries int, ttl, negTTL, loadTimeout time.Duration) *starterCache {
	return &starterCache{
		maxEntries:  maxEntries,
		ttl:         ttl,
		negTTL:      negTTL,
		loadTimeout: loadTimeout,
		now:         time.Now,
		order:       list.New(),
		entries:     make(map[string]*list.Element),
	}
}

// get returns the cached questions for key, or runs load — once per key
// across concurrent callers — and caches its outcome: questions for ttl,
// degraded questions and failures for negTTL. The returned slice is the
// caller's own copy.
func (c *starterCache) get(ctx context.Context, key string, load func(context.Context) (starterOutcome, error)) ([]string, error) {
	if q, ok, err := c.lookup(key); ok {
		return q, err
	}
	ch := c.flight.DoChan(key, func() (v any, err error) {
		// Another flight may have filled the entry between our miss and
		// this flight starting.
		if q, ok, cerr := c.lookup(key); ok {
			return q, cerr
		}
		defer func() {
			// A panic inside a DoChan function is re-raised on a fresh
			// goroutine and would take the process down; turn it into an
			// ordinary (negatively cached) failure instead.
			if r := recover(); r != nil {
				err = fmt.Errorf("starter questions: load panicked: %v", r)
				c.store(key, nil, true, c.negTTL)
			}
		}()
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.loadTimeout)
		defer cancel()
		out, lerr := load(lctx)
		if lerr != nil {
			c.store(key, nil, true, c.negTTL)
			return nil, lerr
		}
		ttl := c.ttl
		if out.degraded {
			ttl = c.negTTL
		}
		c.store(key, out.questions, false, ttl)
		return out.questions, nil
	})
	if c.afterJoin != nil {
		c.afterJoin()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		q, _ := res.Val.([]string)
		return slices.Clone(q), nil
	}
}

// lookup reports a live entry for key (ok) and marks it recently used. A
// negatively cached entry yields errStarterCachedFailure. Expired entries are
// dropped on the way.
func (c *starterCache) lookup(key string) ([]string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false, nil
	}
	e := el.Value.(*starterCacheEntry)
	if !c.now().Before(e.expires) {
		c.order.Remove(el)
		delete(c.entries, key)
		return nil, false, nil
	}
	c.order.MoveToFront(el)
	if e.failed {
		return nil, true, errStarterCachedFailure
	}
	return slices.Clone(e.questions), true, nil
}

// store records an outcome for key, live for ttl, and evicts
// least-recently-used entries beyond maxEntries.
func (c *starterCache) store(key string, questions []string, failed bool, ttl time.Duration) {
	e := &starterCacheEntry{key: key, questions: slices.Clone(questions), failed: failed, expires: c.now().Add(ttl)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		el.Value = e
		c.order.MoveToFront(el)
	} else {
		c.entries[key] = c.order.PushFront(e)
	}
	for c.order.Len() > c.maxEntries {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*starterCacheEntry).key)
	}
}

// size reports the number of entries held, expired or not (tests).
func (c *starterCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
