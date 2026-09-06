package confluence

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// countingHandler counts how many records a slog.Logger emits, independent
// of message/attrs, so a test can assert "logged exactly once" without
// coupling to exact wording.
// ---------------------------------------------------------------------------

type countingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

func (h *countingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.records)
}

func installCountingHandler(t *testing.T) *countingHandler {
	t.Helper()
	h := &countingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// ---------------------------------------------------------------------------
// Re-import decision pinning (W5-R5 / Task 8).
//
// isPageUpdated now parses Version.When through page.VersionWhen() only —
// the second literal-layout fallback ("2006-01-02T15:04:05.000Z") is
// unreachable dead code: time.Parse(time.RFC3339, ...) already accepts a
// fractional-second component even though the RFC3339 layout constant
// doesn't spell one out, so any string that fails the first parse also
// fails the narrower second one. Deleting it must not change the re-import
// decision for any well-formed timestamp shape Confluence actually sends.
// ---------------------------------------------------------------------------

// Mutation: reintroduce the old two-layout parse in place of VersionWhen() —
// this test keeps passing (all three shapes already parse under plain
// time.RFC3339), which is the point: the fallback layout was already dead.
func TestIsPageUpdatedReImportDecisionForWellFormedTimestamps(t *testing.T) {
	baseline := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	existing := []ConfluenceFileRow{
		{Type: "text/markdown", CreatedAt: baseline},
	}

	cases := []struct {
		name string
		when string
		want bool
	}{
		{
			name: "no fractional seconds, Z",
			when: "2026-08-01T10:11:05Z",
			want: true, // 10:11:05Z is after the 08:00:00Z baseline
		},
		{
			name: "fractional seconds, Z",
			when: "2026-08-01T10:11:05.123Z",
			want: true,
		},
		{
			name: "fractional seconds, numeric offset",
			when: "2026-08-01T10:11:05.123+02:00", // == 08:11:05.123Z
			want: true,                            // still after 08:00:00Z baseline
		},
		{
			name: "before the existing file's created_at",
			when: "2026-08-01T07:00:00Z",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := ConfluencePage{ID: "p1"}
			page.Version.When = tc.when

			var once sync.Once
			got := isPageUpdated(page, existing, &once)
			if got != tc.want {
				t.Fatalf("isPageUpdated(%q) = %v, want %v", tc.when, got, tc.want)
			}
		})
	}
}

// A page with existing file rows but none of type text/markdown (e.g. only
// attachments were ever imported) is treated as if the page's main content
// were never captured, so it re-imports regardless of the version date.
//
// Mutation: drop the "no markdown row -> true" fallback at the end of
// isPageUpdated -> this fails (would return false instead).
func TestIsPageUpdatedNoMarkdownRowMeansTrue(t *testing.T) {
	page := ConfluencePage{ID: "p2"}
	page.Version.When = "2026-08-01T10:11:05Z"

	var once sync.Once
	if got := isPageUpdated(page, nil, &once); got != true {
		t.Fatalf("isPageUpdated with no markdown row = %v, want true", got)
	}
}

// An unparseable version.when must not be treated as "changed" (which would
// re-import every affected page on every sync tick); it is treated as
// unchanged, same as the pre-refactor two-layout parse failure path.
//
// Mutation: return true instead of false on an unparseable timestamp -> this
// fails, and every page with a bad timestamp starts re-importing forever.
func TestIsPageUpdatedUnparseableTreatedAsUnchanged(t *testing.T) {
	h := installCountingHandler(t)

	existing := []ConfluenceFileRow{
		{Type: "text/markdown", CreatedAt: time.Now()},
	}
	page := ConfluencePage{ID: "p3"}
	page.Version.When = "not-a-timestamp"

	var once sync.Once
	if got := isPageUpdated(page, existing, &once); got != false {
		t.Fatalf("isPageUpdated(unparseable) = %v, want false", got)
	}
	if h.count() != 1 {
		t.Fatalf("expected exactly 1 log record for the unparseable timestamp, got %d", h.count())
	}
}

// Task 8 / W5-R5: an unparseable version.when is logged once per sync, not
// once per call — isPageUpdated is called twice per page within one sync
// (once to size the progress estimate, once to classify the import job), and
// a sync with many affected pages must not spam one log line per page per
// call. The shared *sync.Once passed by the caller is what scopes "once" to
// the sync run instead of to the function call.
//
// Mutation: log unconditionally instead of through warnOnce.Do(...) -> this
// fails (count would be 3, not 1).
func TestIsPageUpdatedUnparseableLogsOncePerSyncNotPerCall(t *testing.T) {
	h := installCountingHandler(t)

	existing := []ConfluenceFileRow{
		{Type: "text/markdown", CreatedAt: time.Now()},
	}
	var once sync.Once

	// Simulate: two pages with bad timestamps, each checked twice within the
	// same sync (progress-sizing loop + job-classification loop).
	for _, pageID := range []string{"bad-1", "bad-2"} {
		page := ConfluencePage{ID: pageID}
		page.Version.When = "also-not-a-timestamp"
		isPageUpdated(page, existing, &once)
		isPageUpdated(page, existing, &once)
	}

	if h.count() != 1 {
		t.Fatalf("expected exactly 1 log record across the whole sync, got %d", h.count())
	}
}

// A nil warnOnce must not panic (defensive: any future call site that
// doesn't care about dedup can pass nil).
func TestIsPageUpdatedNilWarnOnceDoesNotPanic(t *testing.T) {
	page := ConfluencePage{ID: "p4"}
	page.Version.When = "still-not-a-timestamp"
	if got := isPageUpdated(page, nil, nil); got != false {
		t.Fatalf("isPageUpdated(unparseable, nil warnOnce) = %v, want false", got)
	}
}
