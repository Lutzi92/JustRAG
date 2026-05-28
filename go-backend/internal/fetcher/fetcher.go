package fetcher

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/imroc/req/v3"
)

// Fetcher is the package's primary type. Construct with New, then call Fetch.
type Fetcher struct {
	// ctx is the application-lifetime context. It's stored on the struct
	// because the per-host rate limiter is constructed lazily inside
	// tier1Once.Do (driven by the first request) but its background
	// cleaner goroutine must outlive any single request. Cancel this
	// ctx to terminate the cleaner.
	ctx     context.Context
	cfg     Config
	reqC    *req.Client  // tier 1 client; nil until tier1Once fires
	rl      *hostLimiter // per-host rate limiter for tier 1
	browser *browserPool // tier 2 pool; only used in chromium builds
	jf      *justFindCtx // tier 3 context; only used in chromium builds

	// Per-subsystem once guards. Each tier initialises its own resources
	// lazily on first use, so a process that never calls Tier 2/3 never
	// pays the rod startup cost.
	tier1Once sync.Once
	// tier2Mu guards lazy creation of f.browser. Used in fetchTier2 and
	// closePools so a concurrent first-call doesn't race a teardown reading
	// f.browser.
	tier2Mu sync.Mutex
	// tier3Mu guards lazy creation of f.jf. Tier 3 init can fail (no
	// writable user-data-dir, launcher errors, etc.) and we want to allow
	// retries on subsequent calls — sync.Once would wedge us forever.
	tier3Mu sync.Mutex
}

// ErrUnsupportedMode is returned when a Mode requires a build tag that
// wasn't enabled (e.g. ModeBrowser without -tags chromium).
var ErrUnsupportedMode = errors.New("fetcher: mode requires chromium build tag")

// New constructs a Fetcher from cfg. ctx is the application-lifetime
// context that drives the per-host rate limiter's background cleaner —
// canceling it terminates the cleaner goroutine. Tier 1 is always
// available; Tier 2/3 require building with -tags chromium.
func New(ctx context.Context, cfg Config) *Fetcher {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Fetcher{ctx: ctx, cfg: cfg}
}

// Close releases resources held by the fetcher (browser pools, JustFind
// persistent context, per-host rate-limiter cleaner). Safe to call
// multiple times. In non-chromium builds the browser/JustFind teardown
// is a no-op, but the rate-limiter cleaner is still joined so it cannot
// keep touching the fetcher's internal maps after Close() returns.
func (f *Fetcher) Close() error {
	// rl is created lazily on the first Tier 1 fetch; nil-check guards
	// the never-fetched path.
	if f.rl != nil {
		f.rl.stop()
	}
	return f.closePools()
}

// Fetch retrieves rawURL according to opts and returns the Result.
//
// Mode dispatch:
//   - ModeAuto:     Tier 1 → if shouldEscalate, Tier 2
//   - ModeHTTPOnly: Tier 1 only
//   - ModeBrowser:  Tier 2 only (requires chromium build)
//   - ModeJustFind: Tier 3 (requires chromium build)
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, opts Options) (*Result, error) {
	f.initTier1()

	// Propagate the per-request private-hosts opt-in through the context
	// so every SSRF check in Tier 1/2/3 (validateURL + safeDialContext +
	// HijackRequests) honours it without each site needing its own
	// plumbing. Only trusted callers with a site-config URL should set
	// this flag on Options.
	if opts.AllowPrivateHosts {
		ctx = withAllowPrivate(ctx)
	}

	switch opts.Mode {
	case ModeBrowser:
		return f.fetchTier2(ctx, rawURL, opts)
	case ModeJustFind:
		return f.fetchTier3(ctx, rawURL, opts)
	case ModeHTTPOnly:
		return f.fetchTier1(ctx, rawURL, opts)
	case ModeAuto:
		res, err := f.fetchTier1(ctx, rawURL, opts)
		if err == nil && !shouldEscalate(res) {
			return res, nil
		}
		// Escalate on shouldEscalate(true) or on tier 1 error.
		if t2, t2err := f.fetchTier2(ctx, rawURL, opts); t2err == nil {
			return t2, nil
		} else if err == nil {
			// Tier 2 failed but tier 1 had a usable result.
			return res, nil
		} else {
			return nil, errors.Join(err, t2err)
		}
	default:
		return nil, fmt.Errorf("fetcher: unknown mode %d", opts.Mode)
	}
	// unreachable: all switch cases return.
}
