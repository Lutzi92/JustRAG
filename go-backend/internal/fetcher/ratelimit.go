package fetcher

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
	"golang.org/x/time/rate"

	"github.com/justrag/go-backend/internal/safego"
)

// hostLimiterIdleTTL is how long an unused per-host limiter/semaphore is kept
// before the cleaner evicts it. hostLimiterCleanInterval is the cleaner tick.
const (
	hostLimiterIdleTTL       = 10 * time.Minute
	hostLimiterCleanInterval = 1 * time.Minute
)

// hostKey returns the registrable domain (eTLD+1) for rawURL, or the IP
// literal when the host is one. Used as the key for per-host rate limiters
// so subdomains share a budget.
func hostKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	etld1, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return etld1
}

// hostLimiter combines a per-host token bucket with a per-host concurrency
// semaphore and a global concurrency cap.
//
// The background cleaner goroutine is tracked through cleanerWg and
// torn down through cleanerCancel so Fetcher.Close() can join it
// explicitly instead of relying on the caller eventually cancelling
// the application-lifetime context. Without an explicit join, the
// cleaner would keep touching h.mu / h.limiters / h.hostSems for the
// shutdown window after Close() returns.
type hostLimiter struct {
	rps           float64
	perHost       int
	globalSem     chan struct{}
	mu            sync.Mutex
	limiters      map[string]*rate.Limiter
	hostSems      map[string]chan struct{}
	lastSeen      map[string]time.Time
	cleanerCancel context.CancelFunc
	cleanerWg     sync.WaitGroup
}

// newHostLimiter constructs a hostLimiter and starts the background
// cleaner. The cleaner exits when ctx is canceled OR when stop() is
// called — stop() is the deterministic teardown path used by
// Fetcher.Close().
func newHostLimiter(ctx context.Context, rps float64, perHostConc, globalConc int) *hostLimiter {
	if perHostConc < 1 {
		perHostConc = 1
	}
	if globalConc < 1 {
		globalConc = 1
	}
	// rate.Limit(0) generates no tokens and blocks forever in Wait. Clamp
	// to a small positive value so a Config{} literal that forgot to call
	// DefaultConfig still makes progress, slowly.
	if rps <= 0 {
		rps = 1
	}
	cleanerCtx, cleanerCancel := context.WithCancel(ctx)
	h := &hostLimiter{
		rps:           rps,
		perHost:       perHostConc,
		globalSem:     make(chan struct{}, globalConc),
		limiters:      make(map[string]*rate.Limiter),
		hostSems:      make(map[string]chan struct{}),
		lastSeen:      make(map[string]time.Time),
		cleanerCancel: cleanerCancel,
	}
	h.cleanerWg.Add(1)
	safego.Go(func() {
		defer h.cleanerWg.Done()
		h.cleanLoop(cleanerCtx, hostLimiterCleanInterval, hostLimiterIdleTTL)
	})
	return h
}

// stop cancels the cleaner goroutine and blocks until it has returned.
// Safe to call multiple times — cleanerCancel is idempotent and the
// second Wait returns immediately on an already-drained WaitGroup.
func (h *hostLimiter) stop() {
	if h == nil {
		return
	}
	h.cleanerCancel()
	h.cleanerWg.Wait()
}

// cleanLoop periodically evicts per-host limiter/semaphore entries that
// haven't been touched within ttl. Idle host semaphores (no in-flight
// holders) are safe to drop because acquire will recreate them on demand.
func (h *hostLimiter) cleanLoop(ctx context.Context, interval, ttl time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			h.evictIdle(now, ttl)
		}
	}
}

func (h *hostLimiter) evictIdle(now time.Time, ttl time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, seen := range h.lastSeen {
		if now.Sub(seen) < ttl {
			continue
		}
		// Only drop a host semaphore when no callers are currently
		// holding it; otherwise the in-flight release would deadlock
		// against a freshly recreated channel.
		if sem, ok := h.hostSems[k]; ok && len(sem) > 0 {
			continue
		}
		delete(h.limiters, k)
		delete(h.hostSems, k)
		delete(h.lastSeen, k)
	}
}

// acquire blocks until the call may proceed for rawURL. The returned
// release func must be called when the request completes.
func (h *hostLimiter) acquire(ctx context.Context, rawURL string) (func(), error) {
	key := hostKey(rawURL)
	if key == "" {
		return nil, fmt.Errorf("ratelimit: empty host key for %q", rawURL)
	}

	h.mu.Lock()
	lim, ok := h.limiters[key]
	if !ok {
		// Burst of 2 so the first request doesn't wait.
		lim = rate.NewLimiter(rate.Limit(h.rps), 2)
		h.limiters[key] = lim
	}
	sem, ok := h.hostSems[key]
	if !ok {
		sem = make(chan struct{}, h.perHost)
		h.hostSems[key] = sem
	}
	h.lastSeen[key] = time.Now()
	h.mu.Unlock()

	// Order: global → host → token. Cancellable at every step.
	select {
	case h.globalSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		<-h.globalSem
		return nil, ctx.Err()
	}
	if err := lim.Wait(ctx); err != nil {
		<-sem
		<-h.globalSem
		return nil, err
	}
	return func() {
		<-sem
		<-h.globalSem
	}, nil
}
