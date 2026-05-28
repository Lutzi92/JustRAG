package fetcher

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"golang.org/x/net/html/charset"
	"golang.org/x/net/publicsuffix"
)

// allowLoopbackForTests lets the package's own tests use httptest.NewServer
// fixtures (which listen on 127.0.0.1) by skipping the SSRF loopback check
// in fetchTier1. Production code MUST NOT toggle this — it is package-private
// and only flipped from *_test.go files within this package.
// Once a test sets it to true, leave it set for the remainder of the
// package's test run. Flipping it back in a cleanup would race with
// parallel tests that observe it via fetchTier1.
//
// atomic.Bool keeps reads and writes safe under `go test -race`.
var allowLoopbackForTests atomic.Bool

// initTier1 wires up the Tier-1 *req.Client and rate limiter. Idempotent.
func (f *Fetcher) initTier1() {
	f.tier1Once.Do(func() {
		jar, jarErr := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
		if jarErr != nil {
			// cookiejar.New with a non-nil PublicSuffixList never errors
			// in current stdlib, but be defensive: if it ever does, log
			// and fall through with a nil jar so the client still works
			// without cookie tracking rather than panicking later.
			slog.Warn("fetcher: cookiejar init failed; continuing without cookie jar", "err", jarErr)
			jar = nil
		}
		client := newReqClient(f.cfg)
		if jar != nil {
			client.SetCookieJar(jar)
		}
		// When running package tests (allowLoopbackForTests=true), replace
		// the SSRF-safe dialer with a plain net.Dialer so httptest.NewServer
		// fixtures on 127.0.0.1 are reachable. This branch is never taken in
		// production because allowLoopbackForTests is never set to true there.
		if allowLoopbackForTests.Load() {
			plain := &net.Dialer{Timeout: f.cfg.DialTimeout}
			client.SetDial(plain.DialContext)
		}
		f.reqC = client
		f.rl = newHostLimiter(f.ctx, f.cfg.PerHostRPS, f.cfg.PerHostConcurrency, f.cfg.GlobalConcurrency)
	})
}

// fetchTier1 performs an HTTP GET, handles compression+charset, and returns
// a Result with HTML populated. Extraction is layered in via the !opts.SkipExtraction branch below.
func (f *Fetcher) fetchTier1(ctx context.Context, rawURL string, opts Options) (*Result, error) {
	if !allowLoopbackForTests.Load() {
		if err := validateURL(ctx, rawURL); err != nil {
			return nil, err
		}
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = f.cfg.DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	release, err := f.rl.acquire(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("tier1: rate limit: %w", err)
	}
	defer release()

	ua := opts.UserAgent
	if ua == "" {
		ua = f.cfg.UserAgent
	}
	ua = resolveUserAgent(ua)

	resp, err := f.reqC.R().
		SetContext(ctx).
		SetHeaders(map[string]string{
			"User-Agent":                ua,
			"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
			"Accept-Language":           f.cfg.AcceptLanguage,
			"Accept-Encoding":           "gzip, br",
			"DNT":                       "1",
			"Upgrade-Insecure-Requests": "1",
			"Sec-Ch-Ua":                 `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`,
			"Sec-Ch-Ua-Mobile":          "?0",
			"Sec-Ch-Ua-Platform":        `"Windows"`,
			"Sec-Fetch-Dest":            "document",
			"Sec-Fetch-Mode":            "navigate",
			"Sec-Fetch-Site":            "none",
			"Sec-Fetch-User":            "?1",
		}).
		Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("tier1: do: %w", err)
	}
	// Access the underlying *http.Response for decodeBody and header access.
	httpResp := resp.Response
	defer drainAndClose(httpResp.Body, 1<<20)

	body, err := decodeBody(httpResp, opts.MaxBytes, f.cfg.MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("tier1: decode body: %w", err)
	}

	finalURL := httpResp.Request.URL.String()
	res := &Result{
		URL:        finalURL,
		StatusCode: resp.StatusCode,
		HTML:       body,
		Tier:       1,
		Headers: map[string]string{
			"content-type":     httpResp.Header.Get("Content-Type"),
			"content-language": httpResp.Header.Get("Content-Language"),
		},
		FetchedAt: time.Now().UTC(),
		Links:     extractLinks(body, finalURL),
	}
	if !opts.SkipExtraction {
		title, md, err := Extract(body, finalURL)
		if err != nil {
			slog.Debug("fetcher: extract failed", "url", finalURL, "err", err)
		} else {
			res.Title = title
			res.Markdown = md
		}
	}
	return res, nil
}

// decodeBody handles gzip/br decompression, charset detection, and the
// body-size cap. The returned string is always valid UTF-8.
func decodeBody(resp *http.Response, optMax, cfgMax int64) (string, error) {
	var src io.Reader = resp.Body
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		gz, err := gzip.NewReader(src)
		if err != nil {
			return "", err
		}
		defer gz.Close()
		src = gz
	case "br":
		src = brotli.NewReader(src)
	case "deflate":
		flr := flate.NewReader(src)
		defer flr.Close()
		src = flr
	}

	limit := optMax
	if limit <= 0 {
		limit = cfgMax
	}
	if limit <= 0 {
		limit = 10 << 20
	}
	src = io.LimitReader(src, limit)

	// charset.NewReader handles BOM, meta tags, and Content-Type sniffing.
	reader, err := charset.NewReader(src, resp.Header.Get("Content-Type"))
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// drainAndClose drains up to maxDrain bytes of body before closing so the
// connection returns to the keep-alive pool, then closes.
func drainAndClose(body io.ReadCloser, maxDrain int64) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxDrain))
	_ = body.Close()
}
