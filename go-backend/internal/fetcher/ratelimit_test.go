package fetcher

import (
	"context"
	"testing"
	"time"
)

func TestHostKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		url  string
		want string
	}{
		{"https://www.example.com/foo", "example.com"},
		{"https://blog.example.co.uk/", "example.co.uk"},
		{"https://1.2.3.4:8080/", "1.2.3.4"},
		{"", ""},
		{"://bad", ""},
		{"example.com", ""},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			t.Parallel()
			got := hostKey(tc.url)
			if got != tc.want {
				t.Errorf("hostKey(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestHostLimiterEnforcesRPS(t *testing.T) {
	t.Parallel()
	hl := newHostLimiter(t.Context(), 2 /*rps*/, 4 /*per-host conc*/, 10 /*global*/)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	for i := 0; i < 5; i++ {
		release, err := hl.acquire(ctx, "https://example.com/")
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		release()
	}
	elapsed := time.Since(start)
	// 5 reqs at 2 rps should take ~2s minus burst-of-2 → ≥1.4s
	if elapsed < 1400*time.Millisecond {
		t.Errorf("expected ≥1.4s elapsed for 5 reqs at 2 rps, got %v", elapsed)
	}
}

func TestHostLimiterStopJoinsCleaner(t *testing.T) {
	t.Parallel()
	// Use the test context that is NOT cancelled when this test returns
	// so we know stop() — not test-ctx cancellation — is what brings the
	// cleaner down. If stop() didn't join, this test could still pass on
	// a fast machine; the race detector + explicit lock/unlock around the
	// limiter map below catches the missing happens-before.
	parent := context.Background()
	hl := newHostLimiter(parent, 10, 4, 10)
	if _, err := hl.acquire(parent, "https://example.com/"); err != nil {
		t.Fatalf("warmup acquire: %v", err)
	}

	done := make(chan struct{})
	go func() {
		hl.stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hostLimiter.stop did not return within 2s — cleaner goroutine was not joined")
	}

	// Touch the maps after stop() to confirm the cleaner isn't racing us.
	// Under -race a still-running cleanLoop would be flagged here.
	hl.mu.Lock()
	_ = len(hl.limiters)
	hl.mu.Unlock()
}
