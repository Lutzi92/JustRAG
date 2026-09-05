package admineval

import (
	"context"
	"testing"

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
}
