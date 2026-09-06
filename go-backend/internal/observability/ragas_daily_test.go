package observability

import (
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSetRagasDaily_EmitsMeanAndN(t *testing.T) {
	ResetRagasDaily()

	SetRagasDailyN("kb-1", 12)
	SetRagasDailyMean("kb-1", "faithfulness", 0.91)
	SetRagasDailyMean("kb-1", "answer_relevance", 0.8)
	SetRagasDailyMean("kb-1", "context_precision", 0.55)

	if got := testutil.ToFloat64(RagasDailyNForTest().WithLabelValues("kb-1")); got != 12 {
		t.Errorf("n/kb-1 = %v, want 12", got)
	}
	if got := testutil.ToFloat64(RagasDailyMeanForTest().WithLabelValues("kb-1", "faithfulness")); got != 0.91 {
		t.Errorf("mean faithfulness = %v, want 0.91", got)
	}
	if n := testutil.CollectAndCount(RagasDailyMeanForTest()); n != 3 {
		t.Errorf("mean series count = %d, want 3", n)
	}
}

// Mutation: make ResetRagasDaily a no-op → the first snapshot's series survive
// the second and this fails. The nightly pass is a full snapshot of "KBs that
// were sampled in the last 24 h": without the reset, a KB that stopped being
// sampled (or was deleted) keeps a frozen mean forever, and a quality alert
// raised on it can never resolve.
func TestResetRagasDaily_DropsStaleSeries(t *testing.T) {
	ResetRagasDaily()

	SetRagasDailyN("kb-gone", 5)
	SetRagasDailyMean("kb-gone", "faithfulness", 0.4)
	SetRagasDailyN("kb-stays", 5)
	SetRagasDailyMean("kb-stays", "faithfulness", 0.9)
	if n := testutil.CollectAndCount(RagasDailyMeanForTest()); n != 2 {
		t.Fatalf("mean series after the first pass = %d, want 2", n)
	}

	// Second nightly pass: kb-gone had no samples in the window.
	ResetRagasDaily()
	SetRagasDailyN("kb-stays", 7)
	SetRagasDailyMean("kb-stays", "faithfulness", 0.95)

	if n := testutil.CollectAndCount(RagasDailyMeanForTest()); n != 1 {
		t.Errorf("mean series after the second pass = %d, want 1 (kb-gone must disappear)", n)
	}
	if n := testutil.CollectAndCount(RagasDailyNForTest()); n != 1 {
		t.Errorf("n series after the second pass = %d, want 1", n)
	}
	if got := testutil.ToFloat64(RagasDailyMeanForTest().WithLabelValues("kb-stays", "faithfulness")); got != 0.95 {
		t.Errorf("surviving series = %v, want the refreshed 0.95", got)
	}
}

// Mutation: drop the metric allow-list → a typo'd metric name mints its own
// series. `metric` is a closed enum by design.
func TestSetRagasDailyMean_UnknownMetricFoldsToOther(t *testing.T) {
	ResetRagasDaily()

	SetRagasDailyMean("kb-1", "faithfullness", 0.42) // typo

	if got := testutil.ToFloat64(RagasDailyMeanForTest().WithLabelValues("kb-1", "other")); got != 0.42 {
		t.Errorf("unknown metric should land on 'other', got %v", got)
	}
	if n := testutil.CollectAndCount(RagasDailyMeanForTest()); n != 1 {
		t.Errorf("mean series count = %d, want 1", n)
	}
}

// Mutation: remove the ragasDailyMaxKBs cap → every KB mints its own series,
// no "overflow" series exists, and this fails.
func TestSetRagasDaily_CapsKBCardinality(t *testing.T) {
	ResetRagasDaily()

	for i := 0; i < ragasDailyMaxKBs+50; i++ {
		kb := fmt.Sprintf("kb-%d", i)
		SetRagasDailyN(kb, float64(i))
		SetRagasDailyMean(kb, "faithfulness", 0.5)
	}

	if n := testutil.CollectAndCount(RagasDailyNForTest()); n != ragasDailyMaxKBs+1 {
		t.Errorf("n series count = %d, want %d (cap + overflow)", n, ragasDailyMaxKBs+1)
	}
	// The overflow bucket must exist rather than the excess KBs being dropped
	// silently — an operator needs to see that the cap bit.
	if got := testutil.ToFloat64(RagasDailyNForTest().WithLabelValues("overflow")); got == 0 {
		t.Error("overflow series missing: the excess KBs were dropped instead of folded")
	}
}

// The cap is shared between the two gauges so a KB never lands under its own
// name on one and under "overflow" on the other — an operator reading a mean
// with no matching n (or the reverse) cannot tell whether the sample count is
// zero or hidden.
func TestSetRagasDaily_CapIsSharedAcrossBothGauges(t *testing.T) {
	ResetRagasDaily()

	for i := 0; i < ragasDailyMaxKBs; i++ {
		SetRagasDailyN(fmt.Sprintf("kb-%d", i), 1)
	}
	// The cap is now full; a fresh KB must fold on BOTH gauges.
	SetRagasDailyN("kb-late", 3)
	SetRagasDailyMean("kb-late", "faithfulness", 0.7)

	if got := testutil.ToFloat64(RagasDailyMeanForTest().WithLabelValues("overflow", "faithfulness")); got != 0.7 {
		t.Errorf("late KB's mean should fold to overflow, got %v", got)
	}
	if n := testutil.CollectAndCount(RagasDailyMeanForTest()); n != 1 {
		t.Errorf("mean series count = %d, want 1 (only the overflow series)", n)
	}
}
