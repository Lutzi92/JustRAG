// Package httpclient provides shared HTTP clients with connection pooling.
// All clients share a single transport to enable TCP/TLS connection reuse
// across the application.
package httpclient

import (
	"net/http"
	"time"
)

// sharedTransport clones http.DefaultTransport to preserve standard defaults
// (Proxy, TLS config, etc.) while tuning pool sizes for higher concurrency.
var sharedTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 100
	t.MaxIdleConnsPerHost = 20
	t.IdleConnTimeout = 90 * time.Second
	return t
}()

// New returns an *http.Client that reuses the shared transport for connection
// pooling. The provided timeout applies to the full request lifecycle. Pass 0
// for no top-level timeout (use per-request context timeouts instead).
func New(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: sharedTransport,
	}
}
