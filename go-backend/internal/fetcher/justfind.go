//go:build chromium

package fetcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"

	"github.com/justrag/go-backend/internal/safego"
)

// justFindCtx owns a single dedicated rod browser with a persistent
// user-data-dir so the Anubis challenge cookie survives across requests
// and process restarts. All JustFind requests are serialised through queue.
type justFindCtx struct {
	cfg     Config
	mu      sync.Mutex
	browser *rod.Browser
	queue   chan struct{}
}

func newJustFind(cfg Config) *justFindCtx {
	return &justFindCtx{
		cfg:   cfg,
		queue: make(chan struct{}, 1),
	}
}

// ensure lazily launches the dedicated browser with the persistent profile.
func (j *justFindCtx) ensure() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.browser != nil {
		return nil
	}
	if j.cfg.JustFindUserDataDir == "" {
		return fmt.Errorf("justfind: JustFindUserDataDir not configured")
	}
	l := launcher.New().
		Headless(true).
		Leakless(true).
		UserDataDir(j.cfg.JustFindUserDataDir).
		Set(flags.Flag("disable-dev-shm-usage"))
	// Same system-Chromium pinning as Tier 2 — avoid rod's auto-download.
	if j.cfg.BrowserBin != "" {
		l = l.Bin(j.cfg.BrowserBin)
	}
	if j.cfg.AllowNoSandbox {
		slog.Warn("fetcher: launching JustFind Chromium with --no-sandbox; only safe inside a container boundary")
		l = l.NoSandbox(true)
	}
	if j.cfg.ProxyURL != "" {
		l = l.Proxy(j.cfg.ProxyURL)
	}
	controlURL, err := l.Launch()
	if err != nil {
		return fmt.Errorf("justfind launch: %w", err)
	}
	br := rod.New().ControlURL(controlURL)
	if err := br.Connect(); err != nil {
		// Launcher already started a Chromium process; kill it so we don't
		// leak browsers on every failed connect.
		l.Kill()
		return fmt.Errorf("justfind connect: %w", err)
	}
	j.browser = br
	return nil
}

// fetchTier3 fetches a JustFind page through the persistent context.
// Requests are serialised so the Anubis cookie isn't being raced.
func (f *Fetcher) fetchTier3(ctx context.Context, rawURL string, opts Options) (*Result, error) {
	if err := validateURL(ctx, rawURL); err != nil {
		return nil, err
	}
	if f.cfg.JustFindUserDataDir == "" {
		return nil, fmt.Errorf("justfind: JustFindUserDataDir not configured")
	}
	// Lazy init: keep f.jf around between calls so the persistent profile
	// is reused, but DO NOT use sync.Once — if ensure() fails (e.g. the
	// user-data-dir wasn't writable yet), the next call should be allowed
	// to retry instead of being permanently wedged.
	f.tier3Mu.Lock()
	if f.jf == nil {
		f.jf = newJustFind(f.cfg)
	}
	jf := f.jf
	f.tier3Mu.Unlock()
	if err := jf.ensure(); err != nil {
		return nil, err
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Serialise: only one navigation at a time on this profile.
	select {
	case jf.queue <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-jf.queue }()

	page, err := jf.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("tier3: page: %w", err)
	}
	defer page.Close() //nolint:errcheck

	// SSRF inside the browser: intercept every request, validate, abort
	// anything pointing at a private IP.
	router := page.HijackRequests()
	if err := router.Add("*", "", func(h *rod.Hijack) {
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
	}); err != nil {
		return nil, fmt.Errorf("tier3: hijack add: %w", err)
	}
	// safego: rod panics internally on protocol hiccups, and this goroutine
	// runs inside the long-lived server/worker process — an unrecovered
	// panic here would crash it mid-fetch.
	safego.Go(router.Run)
	defer router.Stop() //nolint:errcheck

	// Capture the HTTP status of the main-frame response. Rod's Navigate
	// does not return it directly, so subscribe to NetworkResponseReceived
	// and remember the status of the response whose URL matches the page's
	// current main-frame navigation.
	var (
		statusMu   sync.Mutex
		mainStatus int
		mainReqID  proto.NetworkRequestID
		haveReqID  bool
	)
	waitEvents := page.Context(ctx).EachEvent(func(e *proto.NetworkRequestWillBeSent) {
		if e.Type != proto.NetworkResourceTypeDocument {
			return
		}
		statusMu.Lock()
		if !haveReqID {
			mainReqID = e.RequestID
			haveReqID = true
		}
		statusMu.Unlock()
	}, func(e *proto.NetworkResponseReceived) {
		statusMu.Lock()
		defer statusMu.Unlock()
		if haveReqID && e.RequestID == mainReqID {
			mainStatus = e.Response.Status
		}
	})
	// safego for the same reason as router.Run above — rod's event pump
	// can panic on protocol-level surprises.
	safego.Go(waitEvents)

	if err := page.Context(ctx).Navigate(rawURL); err != nil {
		return nil, fmt.Errorf("tier3: navigate: %w", err)
	}
	// WaitLoad waits for window.onload; ignore errors (page may already be loaded).
	_ = page.Context(ctx).WaitLoad()
	// Wait for JustFind content selectors. If they never appear (Anubis
	// challenge timing out, or site structure changed), we proceed with
	// whatever HTML is rendered at that point.
	_, _ = page.Context(ctx).Element(`.record, .result-list-item, .mainbody`)

	html, err := page.HTML()
	if err != nil {
		return nil, fmt.Errorf("tier3: html: %w", err)
	}

	// Capture the final URL after redirects.
	finalURL := rawURL
	if info, infoErr := page.Info(); infoErr == nil && info.URL != "" {
		finalURL = info.URL
	}

	// Read the captured main-frame status. If no NetworkResponseReceived
	// event arrived (e.g. navigation served entirely from cache, or only
	// data: URLs), fall back to 0 as a sentinel meaning "unknown — rod did
	// not surface an HTTP status for this navigation". Downstream consumers
	// must treat StatusCode == 0 as unavailable rather than as success.
	statusMu.Lock()
	status := mainStatus
	statusMu.Unlock()

	res := &Result{
		URL:        finalURL,
		StatusCode: status,
		HTML:       html,
		Tier:       3,
		FetchedAt:  time.Now().UTC(),
	}
	if !opts.SkipExtraction {
		title, md, extractErr := Extract(html, finalURL)
		if extractErr == nil {
			res.Title = title
			res.Markdown = md
		} else {
			slog.Warn("fetcher: tier3 extract failed", "url", finalURL, "err", extractErr)
		}
	}
	return res, nil
}
