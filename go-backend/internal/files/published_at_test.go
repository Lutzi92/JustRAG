package files

import (
	"testing"
	"time"
)

// TestClampPublishedAt pins the shared ingest-time clamp now that three
// sources (RSS, Confluence, git) write published_at through it. The worker's
// own rsspoll_published_test still exercises the poller's call site; this one
// pins the helper in the package that owns the column.
//
// Mutation: drop the `t.After(now)` branch in ClampPublishedAt → the future
// case below fails.
func TestClampPublishedAt(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	if got := ClampPublishedAt(nil, now); got != nil {
		t.Errorf("nil in, nil out; got %v", got)
	}

	past := time.Date(2025, 12, 24, 9, 30, 0, 0, time.UTC)
	if got := ClampPublishedAt(&past, now); got == nil || !got.Equal(past) {
		t.Errorf("a past date must pass through unchanged, got %v", got)
	}

	// Exactly `now` is not in the future — it must pass through, not be
	// replaced by a re-derived UTC copy of itself.
	if got := ClampPublishedAt(&now, now); got == nil || !got.Equal(now) {
		t.Errorf("now must pass through unchanged, got %v", got)
	}

	future := now.Add(72 * time.Hour)
	got := ClampPublishedAt(&future, now)
	if got == nil {
		t.Fatal("a future date must be clamped, not dropped")
	}
	if !got.Equal(now) {
		t.Errorf("clamped date = %v, want exactly now (%v)", got, now)
	}
}
