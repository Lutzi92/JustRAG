package fetcher

import (
	"context"
	"net/http"
	"time"
)

// ValidateURL exposes the package's SSRF check for callers outside the
// fetcher tier-1/2/3 path (e.g. files.FetchURL, which validates a
// user-supplied URL before handing it to the fetcher). Returns nil iff
// rawURL is http/https, has a non-empty host, and every IP the host
// resolves to is public. Use SafeHTTPClient for the actual fetch so the
// dial-time re-resolution closes the DNS-rebinding window.
func ValidateURL(ctx context.Context, rawURL string) error {
	return validateURL(ctx, rawURL)
}

// SafeHTTPClient returns an *http.Client whose DialContext refuses any
// connection that resolves to a private / loopback / link-local address.
// This is the SSRF-safe equivalent of httpclient.New — use it whenever
// a user-controlled URL is being fetched.
//
// timeout is the full request lifecycle deadline (Client.Timeout). The
// internal dialTimeout is fixed at 5s to match the fetcher tier-1 default.
func SafeHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = safeDialContext(5 * time.Second)
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}
