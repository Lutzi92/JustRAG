package vector

import (
	"math"
	"testing"
	"time"
)

func TestApplyRecencyBoost_FreshDocGetsNearFullWeight(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	docs := []RankedDoc{{ID: "c1", FileID: "f1", Score: 1.0}}
	times := map[string]time.Time{"f1": now}
	ApplyRecencyBoost(docs, times, now, 14, 0.1)
	if got := docs[0].Score; math.Abs(got-1.1) > 1e-9 {
		t.Errorf("fresh doc score = %v, want 1.1", got)
	}
}

func TestApplyRecencyBoost_HalfLifeGivesHalfWeight(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	docs := []RankedDoc{{ID: "c1", FileID: "f1", Score: 1.0}}
	times := map[string]time.Time{"f1": now.AddDate(0, 0, -14)}
	ApplyRecencyBoost(docs, times, now, 14, 0.1)
	if got := docs[0].Score; math.Abs(got-1.05) > 1e-9 {
		t.Errorf("one-half-life doc score = %v, want 1.05", got)
	}
}

func TestApplyRecencyBoost_AncientDocUnchanged(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	docs := []RankedDoc{{ID: "c1", FileID: "f1", Score: 1.0}}
	times := map[string]time.Time{"f1": now.AddDate(-10, 0, 0)}
	ApplyRecencyBoost(docs, times, now, 14, 0.1)
	if got := docs[0].Score; got > 1.0001 {
		t.Errorf("10-year-old doc score = %v, want ~1.0", got)
	}
}

func TestApplyRecencyBoost_MissingFileTimeIsNoOp(t *testing.T) {
	t.Parallel()
	now := time.Now()
	docs := []RankedDoc{{ID: "c1", FileID: "unknown", Score: 1.0}}
	ApplyRecencyBoost(docs, map[string]time.Time{"f1": now}, now, 14, 0.1)
	if docs[0].Score != 1.0 {
		t.Errorf("doc without file time must keep its score, got %v", docs[0].Score)
	}
}

func TestApplyRecencyBoost_ZeroWeightOrHalfLifeIsNoOp(t *testing.T) {
	t.Parallel()
	now := time.Now()
	times := map[string]time.Time{"f1": now}
	docs := []RankedDoc{{ID: "c1", FileID: "f1", Score: 1.0}}
	ApplyRecencyBoost(docs, times, now, 14, 0)
	ApplyRecencyBoost(docs, times, now, 0, 0.1)
	if docs[0].Score != 1.0 {
		t.Errorf("zero weight/half-life must be a no-op, got %v", docs[0].Score)
	}
}

func TestApplyRecencyBoost_FutureTimestampClampsToFresh(t *testing.T) {
	t.Parallel()
	// Clock skew between DB and app must not produce a boost above weight.
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	docs := []RankedDoc{{ID: "c1", FileID: "f1", Score: 1.0}}
	times := map[string]time.Time{"f1": now.Add(48 * time.Hour)}
	ApplyRecencyBoost(docs, times, now, 14, 0.1)
	if got := docs[0].Score; math.Abs(got-1.1) > 1e-9 {
		t.Errorf("future-dated doc score = %v, want clamp to 1.1", got)
	}
}
