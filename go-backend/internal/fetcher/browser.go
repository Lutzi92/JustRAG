//go:build chromium

package fetcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
)

// browserPool owns one warm *rod.Browser and serialises access to it via
// a buffered channel of incognito sub-browsers used as "sessions". When the
// browser hits its recycle threshold it is closed and re-launched on the
// next acquire.
//
// Concurrency model: each incognito session is handed out as a sessionTicket
// carrying the generation number of the browser it came from. When the parent
// browser is recycled the generation is bumped and the channel is swapped for
// a fresh one. In-flight release() calls that return a stale ticket (gen !=
// current) close the stale incognito directly instead of inserting it into
// the new pool. This avoids both the send-on-closed-channel race and the
// "session from old browser lands in new pool" bug.
type sessionTicket struct {
	browser *rod.Browser
	gen     uint64
}

type browserPool struct {
	cfg        Config
	mu         sync.Mutex
	browser    *rod.Browser
	launchedAt time.Time
	pageCount  int
	gen        uint64 // current browser generation; bumped on every recycle
	sessions   chan sessionTicket
}

const defaultBrowserPoolSize = 6

func resolveBrowserPoolSize(cfg Config) int {
	if cfg.BrowserPoolSize > 0 {
		return cfg.BrowserPoolSize
	}
	return defaultBrowserPoolSize
}

func newBrowserPool(cfg Config) *browserPool {
	return &browserPool{
		cfg:      cfg,
		sessions: make(chan sessionTicket, resolveBrowserPoolSize(cfg)),
	}
}

// ensure makes sure the parent browser is launched and the session pool is
// filled. Recycles when thresholds are exceeded.
func (b *browserPool) ensure() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	needNew := b.browser == nil ||
		(b.cfg.BrowserRecycleAfter > 0 && b.pageCount >= b.cfg.BrowserRecycleAfter) ||
		(b.cfg.BrowserRecycleEvery > 0 && time.Since(b.launchedAt) > b.cfg.BrowserRecycleEvery)

	if !needNew {
		return nil
	}
	if b.browser != nil {
		// Drain everything currently parked in the channel and close those
		// sessions explicitly. Do NOT close the channel — in-flight release()
		// calls may still try to send on it; they'll see a mismatched gen
		// and close their own ticket instead. Swapping the field gives new
		// traffic a fresh channel while old traffic's ticket.gen check
		// quietly drops stale sessions on the floor.
	drainLoop:
		for {
			select {
			case t := <-b.sessions:
				logBrowserCleanup("drain stale session", t.browser.Close())
			default:
				break drainLoop
			}
		}
		b.sessions = make(chan sessionTicket, resolveBrowserPoolSize(b.cfg))
		logBrowserCleanup("recycle parent browser", b.browser.Close())
		b.browser = nil
		b.gen++
	}

	l := launcher.New().
		Headless(true).
		Leakless(true).
		Set(flags.Flag("disable-dev-shm-usage"))
	// Point rod at the system Chromium binary when one is configured.
	// Without this, rod downloads its own Chromium bundle (~300 MB) into
	// ~/.cache/rod on the first Tier 2 call — which in production means
	// the first /api/kb/{id}/crawl request stalls for minutes while the
	// container downloads from Google over the public internet.
	if b.cfg.BrowserBin != "" {
		l = l.Bin(b.cfg.BrowserBin)
	}
	if b.cfg.AllowNoSandbox {
		slog.Warn("fetcher: launching Chromium with --no-sandbox; only safe inside a container boundary")
		l = l.NoSandbox(true)
	}
	if b.cfg.ProxyURL != "" {
		l = l.Proxy(b.cfg.ProxyURL)
	}
	controlURL, err := l.Launch()
	if err != nil {
		return fmt.Errorf("browser launch: %w", err)
	}
	br := rod.New().ControlURL(controlURL)
	if err := br.Connect(); err != nil {
		// Launcher already spawned a Chromium process; kill it so we
		// don't leak browsers on every failed connect. Without this, a
		// node with flaky Chromium startup accumulates orphaned processes
		// and eventually exhausts memory / PIDs.
		l.Kill()
		return fmt.Errorf("browser connect: %w", err)
	}
	b.browser = br
	b.launchedAt = time.Now()
	b.pageCount = 0

	poolSize := resolveBrowserPoolSize(b.cfg)
	for i := 0; i < poolSize; i++ {
		incog, err := br.Incognito()
		if err != nil {
			// Drain any sessions we already pushed onto the channel —
			// they're about to be backed by a closed parent browser, and
			// leaving them in the channel would let the next acquire()
			// hand out a ticket pointing at a dead incognito. Use a
			// non-blocking drain loop so we don't wait on an empty channel.
		drainPartial:
			for {
				select {
				case t := <-b.sessions:
					logBrowserCleanup("drain after incognito failure", t.browser.Close())
				default:
					break drainPartial
				}
			}
			logBrowserCleanup("close after incognito failure", br.Close())
			b.browser = nil
			// Bump the generation so any ticket that happens to still be
			// in flight from a previous (now defunct) init attempt is
			// treated as stale and closed by release() rather than being
			// reinserted into the channel on its way back.
			b.gen++
			return fmt.Errorf("browser incognito: %w", err)
		}
		b.sessions <- sessionTicket{browser: incog, gen: b.gen}
	}
	return nil
}

// acquire pulls a session from the pool, blocking up to ctx deadline.
func (b *browserPool) acquire(ctx context.Context) (sessionTicket, error) {
	if err := b.ensure(); err != nil {
		return sessionTicket{}, err
	}
	// Snapshot the channel under the lock so a concurrent recycle that
	// swaps b.sessions can't make us read from a different channel than
	// the one ensure() just populated.
	b.mu.Lock()
	ch := b.sessions
	b.mu.Unlock()
	select {
	case t := <-ch:
		return t, nil
	case <-ctx.Done():
		return sessionTicket{}, ctx.Err()
	}
}

// release returns a session to the pool and increments the page counter.
// Stale sessions (whose generation doesn't match the current pool) are
// closed directly — this handles the case where a recycle happened while
// this session was in flight.
func (b *browserPool) release(t sessionTicket) {
	b.mu.Lock()
	if t.gen != b.gen {
		b.mu.Unlock()
		logBrowserCleanup("release stale session", t.browser.Close())
		return
	}
	b.pageCount++
	ch := b.sessions
	b.mu.Unlock()
	// Non-blocking send: if the channel is full (shouldn't happen in a
	// healthy pool) or a recycle raced us and swapped the channel after
	// our snapshot, close the session rather than blocking or panicking.
	select {
	case ch <- t:
	default:
		logBrowserCleanup("release into full channel", t.browser.Close())
	}
}

// logBrowserCleanup records best-effort browser-close failures at debug
// level. The parent process may already have exited and most close errors
// are noise, so we don't propagate them — but logging at debug means
// operators chasing leaks can flip the log level and see what's going on.
func logBrowserCleanup(stage string, err error) {
	if err == nil {
		return
	}
	slog.Debug("fetcher: browser cleanup", "stage", stage, "error", err)
}

// closePools tears down the Tier 2 browser pool and the Tier 3 JustFind
// browser if they were ever started. Safe to call multiple times.
func (f *Fetcher) closePools() error {
	// Snapshot the pool pointers under the same locks that guard their lazy
	// initialisation in fetchTier2 / fetchTier3. Without this, a concurrent
	// first-call to a tier could be racing the assignment to f.browser /
	// f.jf while we read it here.
	f.tier2Mu.Lock()
	browser := f.browser
	f.tier2Mu.Unlock()

	f.tier3Mu.Lock()
	jf := f.jf
	f.tier3Mu.Unlock()

	var errs []error
	if browser != nil {
		browser.mu.Lock()
	drainLoop:
		for {
			select {
			case t := <-browser.sessions:
				if err := t.browser.Close(); err != nil {
					errs = append(errs, err)
				}
			default:
				break drainLoop
			}
		}
		if browser.browser != nil {
			if err := browser.browser.Close(); err != nil {
				errs = append(errs, err)
			}
			browser.browser = nil
		}
		browser.mu.Unlock()
	}
	if jf != nil {
		jf.mu.Lock()
		if jf.browser != nil {
			if err := jf.browser.Close(); err != nil {
				errs = append(errs, err)
			}
			jf.browser = nil
		}
		jf.mu.Unlock()
	}
	return errors.Join(errs...)
}

// fetchTier2 navigates to rawURL in a fresh page within an incognito browser,
// enforces SSRF on every subresource via HijackRequests, and returns the
// rendered HTML.
func (f *Fetcher) fetchTier2(ctx context.Context, rawURL string, opts Options) (*Result, error) {
	if err := validateURL(ctx, rawURL); err != nil {
		return nil, err
	}
	f.tier2Mu.Lock()
	if f.browser == nil {
		f.browser = newBrowserPool(f.cfg)
	}
	f.tier2Mu.Unlock()

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = f.cfg.DefaultTimeout
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticket, err := f.browser.acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("tier2: acquire: %w", err)
	}
	defer f.browser.release(ticket)

	page, err := ticket.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("tier2: page: %w", err)
	}
	defer page.Close() //nolint:errcheck

	// Inject stealth overrides before navigation so they're active when
	// the page's own scripts run. This masks navigator.webdriver, patches
	// window.chrome.runtime, and spoofs navigator.plugins.
	_, _ = page.EvalOnNewDocument(stealthScript)

	// Rotate User-Agent to match Tier 1 behaviour.
	ua := resolveUserAgent(opts.UserAgent)
	if ua == "" {
		ua = resolveUserAgent(f.cfg.UserAgent)
	}
	page.MustSetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent: ua,
	})

	// SSRF inside the browser: intercept every request, validate, abort
	// anything pointing at a private IP.
	router := page.HijackRequests()
	router.MustAdd("*", func(h *rod.Hijack) {
		u := h.Request.URL().String()
		// Inherit the outer ctx so request_id/trace_id correlate; cap the
		// SSRF DNS lookup with its own short timeout. router.Stop() (deferred
		// below) drains in-flight callbacks before the outer ctx is cancelled.
		hctx, hcancel := context.WithTimeout(ctx, 5*time.Second)
		validateErr := validateURL(hctx, u)
		hcancel()
		if validateErr != nil {
			h.Response.Fail(proto.NetworkErrorReasonAccessDenied)
			return
		}
		h.ContinueRequest(&proto.FetchContinueRequest{})
	})
	go router.Run()
	defer router.Stop() //nolint:errcheck

	if err := page.Context(ctx).Navigate(rawURL); err != nil {
		return nil, fmt.Errorf("tier2: navigate: %w", err)
	}
	// WaitLoad waits for window.onload; ignore errors (page may already be loaded).
	_ = page.Context(ctx).WaitLoad()
	// Best-effort network idle so SPAs have time to render.
	_ = page.Context(ctx).WaitIdle(3 * time.Second)

	html, err := page.HTML()
	if err != nil {
		return nil, fmt.Errorf("tier2: html: %w", err)
	}

	// Capture the final URL after redirects.
	finalURL := rawURL
	if info, infoErr := page.Info(); infoErr == nil && info.URL != "" {
		finalURL = info.URL
	}

	res := &Result{
		URL:        finalURL,
		StatusCode: 200, // navigation succeeded; HTTP status not directly exposed by rod
		HTML:       html,
		Tier:       2,
		FetchedAt:  time.Now().UTC(),
		Links:      extractLinks(html, finalURL),
	}
	if !opts.SkipExtraction {
		title, md, extractErr := Extract(html, finalURL)
		if extractErr != nil {
			slog.Debug("fetcher tier2: extraction failed", "err", extractErr, "url", finalURL)
		} else {
			res.Title = title
			res.Markdown = md
		}
	}
	return res, nil
}
