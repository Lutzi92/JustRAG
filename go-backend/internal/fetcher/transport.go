package fetcher

import (
	"fmt"
	"net/http"
	"time"

	"github.com/imroc/req/v3"
)

// newReqClient builds the *req.Client used by Tier 1. It uses
// SetTLSFingerprintChrome() to impersonate Chrome's TLS fingerprint,
// which is the highest-impact anti-bot evasion measure.
//
// The SSRF-safe dialer is wired in via SetDialContext(); redirect
// validation via SetRedirectPolicy(). Retry replaces retryablehttp.
//
// Decompression contract: DisableAutoReadResponse() is set so the
// caller receives the raw *http.Response body. applyDefaultHeaders
// explicitly sets Accept-Encoding: "gzip, br", so Tier 1 must
// decompress the body itself (see decodeBody in tier1.go).
func newReqClient(cfg Config) *req.Client {
	// Defensive clamp: a Config{} literal (no DefaultConfig) leaves
	// PerHostConcurrency at 0, which would silently allow unlimited
	// connections per host. Force a safe minimum here.
	perHost := cfg.PerHostConcurrency
	if perHost <= 0 {
		perHost = 2
	}
	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 5 * time.Second
	}

	client := req.C().
		SetTLSFingerprintChrome().
		SetDial(safeDialContext(dialTimeout)).
		DisableAutoReadResponse().
		// DisableAutoDecode prevents req from applying charset conversion
		// to the raw response body. Without this, req sees Content-Type:
		// charset=iso-8859-15 (or similar) and re-encodes the body to
		// UTF-8 — which corrupts gzip/br compressed data before our
		// decodeBody() can decompress it. We handle charset conversion
		// ourselves in decodeBody() AFTER decompression.
		DisableAutoDecode().
		SetCommonRetryCount(2).
		SetCommonRetryBackoffInterval(500*time.Millisecond, 4*time.Second)

	// Wire in redirect validation (hop count, scheme-downgrade, SSRF).
	checkRedirect := newCheckRedirect(cfg)
	client.SetRedirectPolicy(req.RedirectPolicy(checkRedirect))

	// Connection pool tuning on the underlying transport.
	t := client.GetTransport()
	t.MaxIdleConns = 100
	t.MaxIdleConnsPerHost = perHost
	t.MaxConnsPerHost = perHost * 2
	t.IdleConnTimeout = 90 * time.Second
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ExpectContinueTimeout = 1 * time.Second

	return client
}

// newCheckRedirect returns a CheckRedirect function bound to cfg.MaxRedirects.
// The closure is what gets installed as Client.CheckRedirect. It enforces a
// max hop count, blocks https→http downgrades, and re-runs SSRF on every hop.
//
// Order of checks matters: hop-count and scheme-downgrade come BEFORE the
// SSRF check so that test fixtures (with no real DNS) can verify the cheap
// fast paths without triggering a network lookup.
func newCheckRedirect(cfg Config) func(req *http.Request, via []*http.Request) error {
	maxRedirects := cfg.MaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = 10
	}
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("fetcher: too many redirects (%d)", len(via))
		}
		if len(via) > 0 {
			prev := via[len(via)-1]
			if prev.URL.Scheme == "https" && req.URL.Scheme == "http" {
				return fmt.Errorf("fetcher: refusing https→http downgrade to %s", req.URL)
			}
		}
		// CheckRedirect runs synchronously inside Client.Do; req.Context()
		// already carries the per-request deadline so DNS lookups inside
		// validateURL respect the overall fetch budget.
		if err := validateURL(req.Context(), req.URL.String()); err != nil {
			return fmt.Errorf("fetcher: redirect blocked: %w", err)
		}
		return nil
	}
}

// applyDefaultHeaders sets the headers we want on every Tier-1 request:
// realistic UA, broad Accept, language hint, decompression preferences,
// Chrome Client Hints, and Sec-Fetch navigation headers.
func applyDefaultHeaders(req *http.Request, userAgent, acceptLanguage string) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", acceptLanguage)
	req.Header.Set("Accept-Encoding", "gzip, br")
	req.Header.Set("DNT", "1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	// Chrome Client Hints — many anti-bot systems validate these match
	// the User-Agent. The version here should stay in sync with the UA
	// pool versions in options.go.
	req.Header.Set("Sec-Ch-Ua", `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	// Sec-Fetch headers indicate a top-level navigation (user-initiated).
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
}
