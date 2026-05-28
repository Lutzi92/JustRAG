package fetcher

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestFetcher(t *testing.T) *Fetcher {
	t.Helper()
	// Flag stays set for the package test run. Flipping it back in a
	// t.Cleanup would race with parallel tests that observe it via
	// fetchTier1 — atomic.Bool prevents data-race UB but a sibling
	// test could still observe false mid-flight and fail the SSRF
	// loopback check. Production never sets it.
	allowLoopbackForTests.Store(true)
	cfg := DefaultConfig()
	cfg.PerHostRPS = 1000 // don't rate-limit tests
	cfg.PerHostConcurrency = 8
	cfg.GlobalConcurrency = 16
	cfg.DefaultTimeout = 5 * time.Second
	f := New(context.Background(), cfg)
	f.initTier1()
	return f
}

func TestTier1FetchPlain(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><head><title>hi</title></head><body><p>hello</p></body></html>"))
	}))
	defer srv.Close()

	f := newTestFetcher(t)
	res, err := f.fetchTier1(context.Background(), srv.URL, Options{SkipExtraction: true})
	if err != nil {
		t.Fatalf("fetchTier1: %v", err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d", res.StatusCode)
	}
	if !strings.Contains(res.HTML, "hello") {
		t.Errorf("HTML missing 'hello': %q", res.HTML)
	}
}

func TestTier1HandlesGzip(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = gz.Write([]byte("<html><body>compressed-body</body></html>"))
		_ = gz.Close()
	}))
	defer srv.Close()

	f := newTestFetcher(t)
	res, err := f.fetchTier1(context.Background(), srv.URL, Options{})
	if err != nil {
		t.Fatalf("fetchTier1: %v", err)
	}
	if !strings.Contains(res.HTML, "compressed-body") {
		t.Errorf("gzip not decompressed: %q", res.HTML)
	}
}

func TestTier1MaxBytes(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("a", 50_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, big)
	}))
	defer srv.Close()

	f := newTestFetcher(t)
	res, err := f.fetchTier1(context.Background(), srv.URL, Options{MaxBytes: 1024})
	if err != nil {
		t.Fatalf("fetchTier1: %v", err)
	}
	if len(res.HTML) > 1024 {
		t.Errorf("body not capped: len=%d", len(res.HTML))
	}
}
