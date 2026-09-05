package admineval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/observability"
)

func rep(recall, mrr float64, lookupRecall, lookupMRR float64) eval.Report {
	return eval.Report{
		Aggregate: eval.AggregateMetrics{MeanRecall: recall, MRR: mrr},
		RouteAggregates: map[string]eval.AggregateMetrics{
			"lookup": {MeanRecall: lookupRecall, MRR: lookupMRR},
		},
	}
}

func TestEvaluateScheduledRun_NoBaseline(t *testing.T) {
	out := evaluateScheduledRun(nil, rep(0.9, 0.9, 0.9, 0.9), eval.DefaultRegressionThresholds)
	if out.HadBaseline || len(out.Regressions) != 0 {
		t.Fatalf("no baseline → no regressions, HadBaseline=false; got %+v", out)
	}
}

func TestEvaluateScheduledRun_FlagsDrop(t *testing.T) {
	prev := rep(0.9, 0.9, 0.95, 0.9)
	cur := rep(0.9, 0.9, 0.90, 0.9) // lookup recall -5pp
	out := evaluateScheduledRun(&prev, cur, eval.DefaultRegressionThresholds)
	if !out.HadBaseline || len(out.Regressions) != 1 || out.Regressions[0].Route != "lookup" {
		t.Fatalf("want one lookup regression, got %+v", out)
	}
}

func TestPublishScheduledGauges_SetsMetricAndRegressionFlags(t *testing.T) {
	prev := rep(0.9, 0.9, 0.95, 0.9)
	cur := rep(0.9, 0.9, 0.90, 0.9)
	out := evaluateScheduledRun(&prev, cur, eval.DefaultRegressionThresholds)
	publishScheduledGauges("kb1", "gs1", cur, out)

	if v := testutil.ToFloat64(observability.EvalScheduledMetric.WithLabelValues("kb1", "gs1", "lookup", "recall")); v != 0.90 {
		t.Fatalf("lookup recall gauge: want 0.90, got %v", v)
	}
	if v := testutil.ToFloat64(observability.EvalScheduledRegression.WithLabelValues("kb1", "gs1", "lookup")); v != 1 {
		t.Fatalf("lookup regression gauge: want 1, got %v", v)
	}
	if v := testutil.ToFloat64(observability.EvalScheduledRegression.WithLabelValues("kb1", "gs1", "overall")); v != 0 {
		t.Fatalf("overall regression gauge: want 0, got %v", v)
	}
}

func TestPublishScheduledGauges_NoBaselineStillSetsMetrics(t *testing.T) {
	cur := rep(0.5, 0.5, 0.5, 0.5)
	publishScheduledGauges("kb2", "gs2", cur, evaluateScheduledRun(nil, cur, eval.DefaultRegressionThresholds))
	if v := testutil.ToFloat64(observability.EvalScheduledMetric.WithLabelValues("kb2", "gs2", "overall", "recall")); v != 0.5 {
		t.Fatalf("metric gauge must be set without a baseline: got %v", v)
	}
	// The regression gauge must NOT have been touched for kb2/gs2: collect
	// the vec and assert no series carries kb="kb2". (WithLabelValues would
	// create the series, so inspect the collected family instead.)
	ch := make(chan prometheus.Metric, 64)
	observability.EvalScheduledRegression.Collect(ch)
	close(ch)
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatal(err)
		}
		for _, lp := range pb.GetLabel() {
			if lp.GetName() == "kb" && lp.GetValue() == "kb2" {
				t.Fatalf("regression gauge must stay unset without a baseline, found series %v", pb.GetLabel())
			}
		}
	}
}

func TestRegressionThresholdsFrom_DefaultsAndOverrides(t *testing.T) {
	if th := regressionThresholdsFrom(context.Background(), staticCfg(nil)); th != eval.DefaultRegressionThresholds {
		t.Fatalf("defaults: got %+v", th)
	}
	th := regressionThresholdsFrom(context.Background(), staticCfg(map[string]string{"eval_regression_recall_pp": "1.5", "eval_regression_mrr_pp": "4"}))
	if th.RecallPP != 1.5 || th.MRRPP != 4 {
		t.Fatalf("overrides: got %+v", th)
	}
	if th := regressionThresholdsFrom(context.Background(), staticCfg(map[string]string{"eval_regression_recall_pp": "-1"})); th.RecallPP != 2 {
		t.Fatalf("out-of-range must fall back to default: got %+v", th)
	}
	if th := regressionThresholdsFrom(context.Background(), staticCfg(map[string]string{"eval_regression_recall_pp": "150"})); th.RecallPP != eval.DefaultRegressionThresholds.RecallPP {
		t.Fatalf("above-100 must fall back to default: got %+v", th)
	}
	if th := regressionThresholdsFrom(context.Background(), staticCfg(map[string]string{"eval_regression_mrr_pp": "abc"})); th.MRRPP != eval.DefaultRegressionThresholds.MRRPP {
		t.Fatalf("unparsable must fall back to default: got %+v", th)
	}
}

// ---------------------------------------------------------------------------
// checkScheduledRegression glue — exercised directly against a Worker built
// with a fake previousRunFinder, no *eval.Store or testableWorker needed.
// ---------------------------------------------------------------------------

// fakeFinder is a scriptable previousRunFinder: it returns whatever run/err
// is configured and counts how many times it was invoked, so tests can
// assert the finder was (or wasn't) called.
type fakeFinder struct {
	calls int
	run   *eval.Run
	err   error
}

func (f *fakeFinder) LatestCompletedScheduled(_ context.Context, _, _ uuid.UUID) (*eval.Run, error) {
	f.calls++
	return f.run, f.err
}

// testLogger is a *slog.Logger that discards output, for tests that only
// care about gauge/call-count side effects.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mustReportJSON marshals a Report the same way a real eval run would
// persist it, for feeding to checkScheduledRegression / fakeFinder.
func mustReportJSON(t *testing.T, r eval.Report) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return b
}

// assertNoRegressionSeriesForKB fails the test if any collected series of
// EvalScheduledRegression carries kb=kb — i.e. the regression gauge was
// never touched for that KB (WithLabelValues would itself create a zero
// series, so this must inspect the collected family instead).
func assertNoRegressionSeriesForKB(t *testing.T, kb string) {
	t.Helper()
	ch := make(chan prometheus.Metric, 256)
	observability.EvalScheduledRegression.Collect(ch)
	close(ch)
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatal(err)
		}
		for _, lp := range pb.GetLabel() {
			if lp.GetName() == "kb" && lp.GetValue() == kb {
				t.Fatalf("regression gauge must stay unset for kb=%s, found series %v", kb, pb.GetLabel())
			}
		}
	}
}

// assertNoMetricSeriesForKB is the EvalScheduledMetric analogue of
// assertNoRegressionSeriesForKB.
func assertNoMetricSeriesForKB(t *testing.T, kb string) {
	t.Helper()
	ch := make(chan prometheus.Metric, 256)
	observability.EvalScheduledMetric.Collect(ch)
	close(ch)
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatal(err)
		}
		for _, lp := range pb.GetLabel() {
			if lp.GetName() == "kb" && lp.GetValue() == kb {
				t.Fatalf("metric gauge must stay unset for kb=%s, found series %v", kb, pb.GetLabel())
			}
		}
	}
}

func TestCheckScheduledRegression_HappyPathFlagsRegression(t *testing.T) {
	kbID, gsID := uuid.New(), uuid.New()
	prevRep := rep(0.9, 0.9, 0.95, 0.9)
	curRep := rep(0.9, 0.9, 0.90, 0.9) // lookup recall -5pp

	finder := &fakeFinder{run: &eval.Run{ID: uuid.New(), Report: mustReportJSON(t, prevRep)}}
	w := &Worker{prev: finder, cfg: staticCfg(nil)}
	run := eval.Run{ID: uuid.New(), KBID: kbID, GoldenSetID: &gsID, Scheduled: true}

	w.checkScheduledRegression(context.Background(), testLogger(), run, mustReportJSON(t, curRep))

	if finder.calls != 1 {
		t.Fatalf("finder calls: want 1, got %d", finder.calls)
	}
	kb, gs := kbID.String(), gsID.String()
	if v := testutil.ToFloat64(observability.EvalScheduledMetric.WithLabelValues(kb, gs, "lookup", "recall")); v != 0.90 {
		t.Fatalf("lookup recall gauge: want 0.90, got %v", v)
	}
	if v := testutil.ToFloat64(observability.EvalScheduledRegression.WithLabelValues(kb, gs, "lookup")); v != 1 {
		t.Fatalf("lookup regression gauge: want 1, got %v", v)
	}
	if v := testutil.ToFloat64(observability.EvalScheduledRegression.WithLabelValues(kb, gs, "overall")); v != 0 {
		t.Fatalf("overall regression gauge: want 0, got %v", v)
	}
}

func TestCheckScheduledRegression_NilGoldenSetIDSkipsFinder(t *testing.T) {
	kbID := uuid.New()
	finder := &fakeFinder{run: &eval.Run{ID: uuid.New(), Report: mustReportJSON(t, rep(0.9, 0.9, 0.9, 0.9))}}
	w := &Worker{prev: finder, cfg: staticCfg(nil)}
	run := eval.Run{ID: uuid.New(), KBID: kbID, GoldenSetID: nil, Scheduled: true}
	cur := rep(0.7, 0.7, 0.7, 0.7)

	w.checkScheduledRegression(context.Background(), testLogger(), run, mustReportJSON(t, cur))

	if finder.calls != 0 {
		t.Fatalf("finder must not be called when GoldenSetID is nil, got %d calls", finder.calls)
	}
	kb := kbID.String()
	if v := testutil.ToFloat64(observability.EvalScheduledMetric.WithLabelValues(kb, "", "overall", "recall")); v != 0.7 {
		t.Fatalf("overall recall gauge: want 0.7, got %v", v)
	}
	assertNoRegressionSeriesForKB(t, kb)
}

func TestCheckScheduledRegression_EmptyPredecessorReportIsNoBaseline(t *testing.T) {
	kbID, gsID := uuid.New(), uuid.New()
	// prevRun exists but its Report is unset — e.g. a completed scheduled
	// row whose report column somehow came back empty. Must be treated as
	// "no baseline", not logged as a parse failure (that's a different,
	// genuinely-unexpected error condition).
	finder := &fakeFinder{run: &eval.Run{ID: uuid.New(), Report: nil}}
	w := &Worker{prev: finder, cfg: staticCfg(nil)}
	run := eval.Run{ID: uuid.New(), KBID: kbID, GoldenSetID: &gsID, Scheduled: true}
	cur := rep(0.6, 0.6, 0.6, 0.6)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	w.checkScheduledRegression(context.Background(), logger, run, mustReportJSON(t, cur))

	if finder.calls != 1 {
		t.Fatalf("finder calls: want 1, got %d", finder.calls)
	}
	if strings.Contains(logBuf.String(), "previous_parse_failed") {
		t.Fatalf("an empty predecessor report must not be logged as a parse failure: %s", logBuf.String())
	}
	kb, gs := kbID.String(), gsID.String()
	if v := testutil.ToFloat64(observability.EvalScheduledMetric.WithLabelValues(kb, gs, "overall", "recall")); v != 0.6 {
		t.Fatalf("overall recall gauge: want 0.6, got %v", v)
	}
	assertNoRegressionSeriesForKB(t, kb)
}

func TestCheckScheduledRegression_UnparsableCurrentReportSetsNoGauges(t *testing.T) {
	kbID, gsID := uuid.New(), uuid.New()
	finder := &fakeFinder{} // must not be called: we bail before looking up a predecessor
	w := &Worker{prev: finder, cfg: staticCfg(nil)}
	run := eval.Run{ID: uuid.New(), KBID: kbID, GoldenSetID: &gsID, Scheduled: true}

	w.checkScheduledRegression(context.Background(), testLogger(), run, json.RawMessage("not json"))

	if finder.calls != 0 {
		t.Fatalf("finder must not be called when the current report fails to parse, got %d calls", finder.calls)
	}
	kb := kbID.String()
	assertNoMetricSeriesForKB(t, kb)
	assertNoRegressionSeriesForKB(t, kb)
}

func TestCheckScheduledRegression_FinderErrorFallsBackToNoBaseline(t *testing.T) {
	kbID, gsID := uuid.New(), uuid.New()
	finder := &fakeFinder{err: errors.New("boom")}
	w := &Worker{prev: finder, cfg: staticCfg(nil)}
	run := eval.Run{ID: uuid.New(), KBID: kbID, GoldenSetID: &gsID, Scheduled: true}
	cur := rep(0.4, 0.4, 0.4, 0.4)

	w.checkScheduledRegression(context.Background(), testLogger(), run, mustReportJSON(t, cur))

	if finder.calls != 1 {
		t.Fatalf("finder calls: want 1, got %d", finder.calls)
	}
	kb, gs := kbID.String(), gsID.String()
	if v := testutil.ToFloat64(observability.EvalScheduledMetric.WithLabelValues(kb, gs, "overall", "recall")); v != 0.4 {
		t.Fatalf("overall recall gauge: want 0.4, got %v", v)
	}
	assertNoRegressionSeriesForKB(t, kb)
}
