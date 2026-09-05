package admineval

import (
	"context"
	"strconv"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/observability"
)

// Site_config keys for the scheduled-run regression gate (percentage points).
const (
	keyRegressionRecallPP = "eval_regression_recall_pp"
	keyRegressionMRRPP    = "eval_regression_mrr_pp"
)

// previousRunFinder is the slice of *eval.Store the worker needs for the
// delta. Kept as an interface so the worker test can stub it.
type previousRunFinder interface {
	LatestCompletedScheduled(ctx context.Context, goldenSetID, exclude uuid.UUID) (*eval.Run, error)
}

// ScheduledOutcome is the result of comparing a scheduled run to its
// predecessor. HadBaseline=false means this was the first scheduled run for
// the golden set: metric gauges are set, the regression gauge is left alone.
type ScheduledOutcome struct {
	Regressions []eval.Regression
	HadBaseline bool
}

// evaluateScheduledRun is the pure comparison. prev == nil → no baseline.
func evaluateScheduledRun(prev *eval.Report, cur eval.Report, th eval.RegressionThresholds) ScheduledOutcome {
	if prev == nil {
		return ScheduledOutcome{}
	}
	return ScheduledOutcome{HadBaseline: true, Regressions: eval.CheckRegression(*prev, cur, th)}
}

// publishScheduledGauges writes the run's aggregates to
// rag_eval_scheduled_metric and, when a baseline existed, sets
// rag_eval_scheduled_regression to 1/0 per route.
func publishScheduledGauges(kb, goldenSet string, cur eval.Report, out ScheduledOutcome) {
	set := func(route string, a eval.AggregateMetrics) {
		observability.EvalScheduledMetric.WithLabelValues(kb, goldenSet, route, "recall").Set(a.MeanRecall)
		observability.EvalScheduledMetric.WithLabelValues(kb, goldenSet, route, "precision").Set(a.MeanPrecision)
		observability.EvalScheduledMetric.WithLabelValues(kb, goldenSet, route, "mrr").Set(a.MRR)
		observability.EvalScheduledMetric.WithLabelValues(kb, goldenSet, route, "ndcg").Set(a.MeanNDCG)
	}
	set("overall", cur.Aggregate)
	for route, a := range cur.RouteAggregates {
		set(route, a)
	}
	if !out.HadBaseline {
		return
	}
	regressed := map[string]bool{}
	for _, r := range out.Regressions {
		regressed[r.Route] = true
	}
	flag := func(route string) {
		v := 0.0
		if regressed[route] {
			v = 1
		}
		observability.EvalScheduledRegression.WithLabelValues(kb, goldenSet, route).Set(v)
	}
	flag("overall")
	for route := range cur.RouteAggregates {
		flag(route)
	}
}

// regressionThresholdsFrom reads the two threshold keys; unset, unparsable
// or non-positive values fall back to eval.DefaultRegressionThresholds.
func regressionThresholdsFrom(ctx context.Context, cfg siteConfigReader) eval.RegressionThresholds {
	th := eval.DefaultRegressionThresholds
	read := func(key string, into *float64) {
		if cfg == nil {
			return
		}
		v, err := cfg.GetSiteConfigValue(ctx, key)
		if err != nil || v == nil {
			return
		}
		f, err := strconv.ParseFloat(*v, 64)
		if err != nil || f <= 0 || f > 100 {
			return
		}
		*into = f
	}
	read(keyRegressionRecallPP, &th.RecallPP)
	read(keyRegressionMRRPP, &th.MRRPP)
	return th
}
