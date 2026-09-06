package observability

import (
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSetSourceSyncAge_EmitsPerKindPerKB(t *testing.T) {
	ResetSourceSyncAge()

	SetSourceSyncAge("rss", "kb-1", 3600)
	SetSourceSyncAge("confluence", "kb-1", 60)
	SetSourceSyncAge("git", "kb-2", 7)

	if got := testutil.ToFloat64(SourceSyncAgeForTest().WithLabelValues("rss", "kb-1")); got != 3600 {
		t.Errorf("rss/kb-1 = %v, want 3600", got)
	}
	if got := testutil.ToFloat64(SourceSyncAgeForTest().WithLabelValues("confluence", "kb-1")); got != 60 {
		t.Errorf("confluence/kb-1 = %v, want 60", got)
	}
	if got := testutil.ToFloat64(SourceSyncAgeForTest().WithLabelValues("git", "kb-2")); got != 7 {
		t.Errorf("git/kb-2 = %v, want 7", got)
	}
	if n := testutil.CollectAndCount(SourceSyncAgeForTest()); n != 3 {
		t.Errorf("series count = %d, want 3", n)
	}
}

// Mutation: make ResetSourceSyncAge a no-op → the first snapshot's series
// survive the second one and this fails. A Set-only refresh is exactly how a
// deleted source's age alert becomes unresolvable.
func TestResetSourceSyncAge_DropsStaleSeries(t *testing.T) {
	ResetSourceSyncAge()

	SetSourceSyncAge("rss", "kb-gone", 3600)
	SetSourceSyncAge("rss", "kb-stays", 60)
	if n := testutil.CollectAndCount(SourceSyncAgeForTest()); n != 2 {
		t.Fatalf("series after the first snapshot = %d, want 2", n)
	}

	// Second snapshot: kb-gone no longer has a source.
	ResetSourceSyncAge()
	SetSourceSyncAge("rss", "kb-stays", 120)

	if n := testutil.CollectAndCount(SourceSyncAgeForTest()); n != 1 {
		t.Errorf("series after the second snapshot = %d, want 1 (the removed source's series must be gone)", n)
	}
	if got := testutil.ToFloat64(SourceSyncAgeForTest().WithLabelValues("rss", "kb-stays")); got != 120 {
		t.Errorf("surviving series = %v, want the refreshed 120", got)
	}
}

// Mutation: drop the kind allow-list → an unknown kind emits its own series
// and this fails. `kind` is a closed enum by design; the whole point of
// folding it is that a typo at a call site cannot grow cardinality.
func TestSetSourceSyncAge_UnknownKindFoldsToOther(t *testing.T) {
	ResetSourceSyncAge()

	SetSourceSyncAge("sharepoint", "kb-1", 42)

	if got := testutil.ToFloat64(SourceSyncAgeForTest().WithLabelValues("other", "kb-1")); got != 42 {
		t.Errorf("unknown kind should land on 'other', got %v", got)
	}
	if n := testutil.CollectAndCount(SourceSyncAgeForTest()); n != 1 {
		t.Errorf("series count = %d, want 1", n)
	}
}

// Mutation: remove the sourceSyncAgeMaxKBs cap → every KB gets its own
// series, no "overflow" series exists, and this fails. Unbounded kb labels
// on a gauge refreshed every maintenance tick is the cardinality bomb the
// cap exists to defuse.
func TestSetSourceSyncAge_CapsKBCardinality(t *testing.T) {
	ResetSourceSyncAge()

	for i := 0; i < sourceSyncAgeMaxKBs+50; i++ {
		SetSourceSyncAge("rss", fmt.Sprintf("kb-%d", i), float64(i))
	}

	series := testutil.CollectAndCount(SourceSyncAgeForTest())
	// At most cap distinct KBs plus the shared overflow series. The
	// Load-then-LoadOrStore pair may overshoot by a few under concurrency;
	// this test is single-goroutine, so the bound is exact enough to assert.
	if series > sourceSyncAgeMaxKBs+1 {
		t.Errorf("series = %d, want at most %d (cap + overflow)", series, sourceSyncAgeMaxKBs+1)
	}
	if got := testutil.ToFloat64(SourceSyncAgeForTest().WithLabelValues("rss", "overflow")); got == 0 {
		t.Error("expected KBs past the cap to fold into the 'overflow' label")
	}
}
