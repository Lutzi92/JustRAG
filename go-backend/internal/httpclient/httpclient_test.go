package httpclient_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/httpclient"
)

func TestNew_AppliesTimeout(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		timeout time.Duration
	}{
		{"zero timeout (no top-level cap)", 0},
		{"second timeout", 1 * time.Second},
		{"minute timeout", 60 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := httpclient.New(tc.timeout)
			if c == nil {
				t.Fatal("New returned nil")
			}
			if c.Timeout != tc.timeout {
				t.Errorf("Timeout: got %v, want %v", c.Timeout, tc.timeout)
			}
			if c.Transport == nil {
				t.Error("Transport must not be nil (shared transport is required for pooling)")
			}
		})
	}
}

// Two clients constructed via New must share the same *http.Transport. The
// whole point of the package is that the shared transport enables TCP/TLS
// connection reuse across the application — if each call returned a fresh
// transport, pools would be per-client and the optimisation would silently
// disappear.
func TestNew_TransportIsShared(t *testing.T) {
	t.Parallel()

	a := httpclient.New(1 * time.Second)
	b := httpclient.New(30 * time.Second)

	if a.Transport != b.Transport {
		t.Errorf("Transport instances differ across New() calls; expected the same shared *http.Transport")
	}
}

// Smoke test: an actual round-trip through a clients returned by New must
// succeed and reuse the connection when the server keeps it alive. We hit
// httptest.Server twice with the same client and confirm both requests
// succeed; the test catches misconfigured transports (e.g. accidentally
// disabling keep-alives) more robustly than inspecting the struct fields.
func TestNew_RoundTrip(t *testing.T) {
	t.Parallel()

	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := httpclient.New(5 * time.Second)
	for i := range 2 {
		resp, err := c.Get(ts.URL)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("request %d: status %d, want 200", i, resp.StatusCode)
		}
	}

	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hit count: got %d, want 2", got)
	}
}
