// Package httpclient provides shared HTTP clients with connection pooling.
// All clients share a single transport to enable TCP/TLS connection reuse
// across the application.
package httpclient

import (
	"net/http"
	"os"
	"strconv"
	"time"
)

// defaultMaxIdleConnsPerHost is the per-host idle-connection cap. The hot path
// here is the embedding/rerank provider: a single chat turn can fan out into
// several concurrent searches (multi-query, step-back, RAG-fusion), each
// embedding a batch, and many turns run in parallel under load. Go's stock
// default of 2 — and even the prior tuned 20 — throttles connection reuse
// against that one host, forcing fresh TCP (and TLS) handshakes on the hot
// path. 50 keeps a warm pool sized to realistic concurrent batch fan-out.
// Override via HTTPCLIENT_MAX_IDLE_CONNS_PER_HOST for deployments that drive
// even higher provider concurrency.
const defaultMaxIdleConnsPerHost = 50

// sharedTransport clones http.DefaultTransport to preserve standard defaults
// (Proxy, TLS config, ForceAttemptHTTP2, etc.) while tuning pool sizes for
// higher concurrency.
var sharedTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	perHost := envInt("HTTPCLIENT_MAX_IDLE_CONNS_PER_HOST", defaultMaxIdleConnsPerHost)
	t.MaxIdleConnsPerHost = perHost
	// Keep the global cap a comfortable multiple of the per-host cap so a
	// handful of distinct provider hosts (embed / rerank / chat / grader,
	// often on separate endpoints) can each fill their per-host pool without
	// bottlenecking on the total. 4× covers that without unbounded growth.
	t.MaxIdleConns = perHost * 4
	t.IdleConnTimeout = 90 * time.Second
	return t
}()

// envInt reads a positive integer from the named environment variable,
// falling back to def when unset, unparseable, or non-positive.
func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// New returns an *http.Client that reuses the shared transport for connection
// pooling. The provided timeout applies to the full request lifecycle. Pass 0
// for no top-level timeout (use per-request context timeouts instead).
func New(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: sharedTransport,
	}
}
