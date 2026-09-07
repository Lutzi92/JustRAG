package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/ragassamples"
)

// fakeDailyStore records the arguments of the nightly pass and replays canned
// results, so the loop body can be tested without a database.
type fakeDailyStore struct {
	stats    map[string]ragassamples.DailyStats
	statsErr error

	sinceArgs     []time.Time
	prunedBefore  []time.Time
	pruneErr      error
	pruneReturned int64
}

func (f *fakeDailyStore) Insert(context.Context, ragassamples.Sample) error { return nil }

func (f *fakeDailyStore) DailyStats(_ context.Context, since time.Time) (map[string]ragassamples.DailyStats, error) {
	f.sinceArgs = append(f.sinceArgs, since)
	return f.stats, f.statsErr
}

func (f *fakeDailyStore) Prune(_ context.Context, olderThan time.Time) (int64, error) {
	f.prunedBefore = append(f.prunedBefore, olderThan)
	return f.pruneReturned, f.pruneErr
}

func ptrF(v float64) *float64 { return &v }

func TestRefreshRagasDaily_PublishesGaugesAndPrunes(t *testing.T) {
	observability.ResetRagasDaily()
	store := &fakeDailyStore{stats: map[string]ragassamples.DailyStats{
		"kb-a": {N: 10, Faithfulness: ptrF(0.9), AnswerRelevance: ptrF(0.8), ContextPrecision: ptrF(0.7)},
		// kb-b's judge failed every faithfulness prompt in the window: a nil
		// mean must NOT be published as 0, which would read as "this KB
		// answers nothing faithfully" on a dashboard.
		"kb-b": {N: 3, AnswerRelevance: ptrF(0.5)},
	}}

	before := time.Now()
	refreshRagasDaily(context.Background(), store, 90*24*time.Hour)

	if len(store.sinceArgs) != 1 {
		t.Fatalf("DailyStats calls = %d, want 1", len(store.sinceArgs))
	}
	if d := before.Sub(store.sinceArgs[0]); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("DailyStats since = %v before now, want ~24h", d)
	}

	if got := testutil.ToFloat64(observability.RagasDailyNForTest().WithLabelValues("kb-a")); got != 10 {
		t.Errorf("n{kb-a} = %v, want 10", got)
	}
	if got := testutil.ToFloat64(observability.RagasDailyMeanForTest().WithLabelValues("kb-a", "faithfulness")); got != 0.9 {
		t.Errorf("mean{kb-a,faithfulness} = %v, want 0.9", got)
	}
	// kb-b: 1 mean series (answer_relevance) — plus kb-a's 3 = 4 total.
	if n := testutil.CollectAndCount(observability.RagasDailyMeanForTest()); n != 4 {
		t.Errorf("mean series = %d, want 4 (nil means must not be published)", n)
	}

	if len(store.prunedBefore) != 1 {
		t.Fatalf("Prune calls = %d, want 1", len(store.prunedBefore))
	}
	if d := before.Sub(store.prunedBefore[0]); d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Errorf("Prune cutoff = %v before now, want ~90d", d)
	}
}

// Mutation: drop the ResetRagasDaily call in refreshRagasDaily → the first
// pass's kb-gone series survives the second and this fails.
func TestRefreshRagasDaily_ResetsStaleSeriesEachPass(t *testing.T) {
	observability.ResetRagasDaily()
	store := &fakeDailyStore{stats: map[string]ragassamples.DailyStats{
		"kb-gone":  {N: 2, Faithfulness: ptrF(0.4)},
		"kb-stays": {N: 2, Faithfulness: ptrF(0.9)},
	}}
	refreshRagasDaily(context.Background(), store, time.Hour)
	if n := testutil.CollectAndCount(observability.RagasDailyNForTest()); n != 2 {
		t.Fatalf("n series after the first pass = %d, want 2", n)
	}

	// Next night kb-gone had no samples at all — it is absent from the map.
	store.stats = map[string]ragassamples.DailyStats{
		"kb-stays": {N: 5, Faithfulness: ptrF(0.95)},
	}
	refreshRagasDaily(context.Background(), store, time.Hour)

	if n := testutil.CollectAndCount(observability.RagasDailyNForTest()); n != 1 {
		t.Errorf("n series after the second pass = %d, want 1 (kb-gone must disappear)", n)
	}
}

// A failed aggregate query must leave the previous snapshot intact — a
// slightly stale gauge beats a blank one — and must NOT stop the retention
// delete, which is independent of it.
func TestRefreshRagasDaily_StatsError_KeepsGaugesAndStillPrunes(t *testing.T) {
	observability.ResetRagasDaily()
	store := &fakeDailyStore{stats: map[string]ragassamples.DailyStats{
		"kb-a": {N: 4, Faithfulness: ptrF(0.6)},
	}}
	refreshRagasDaily(context.Background(), store, time.Hour)

	store.statsErr = errors.New("db down")
	refreshRagasDaily(context.Background(), store, time.Hour)

	if got := testutil.ToFloat64(observability.RagasDailyNForTest().WithLabelValues("kb-a")); got != 4 {
		t.Errorf("n{kb-a} = %v after a failed pass, want the previous 4", got)
	}
	if len(store.prunedBefore) != 2 {
		t.Errorf("Prune calls = %d, want 2 (retention is independent of the aggregate)", len(store.prunedBefore))
	}
}

// A nil store means persistence was never wired; the pass must be a no-op
// rather than a nil-dereference panic that the launch() supervisor restarts
// every 30 seconds forever.
func TestRefreshRagasDaily_NilStore_NoOp(t *testing.T) {
	refreshRagasDaily(context.Background(), nil, time.Hour)
}
