package eval

import (
	"fmt"
	"io"
	"math"
	"sort"
)

// RegressionThresholds are the maximum tolerated drops, in percentage
// points, between a baseline and a candidate report. A drop strictly greater
// than the threshold is a regression; a drop equal to it is not.
type RegressionThresholds struct {
	RecallPP float64
	MRRPP    float64
}

// DefaultRegressionThresholds are the review's starting values (-2 pp recall,
// -3 pp MRR). Operators tune them via eval_regression_recall_pp /
// eval_regression_mrr_pp (scheduled runs) or the cmd/eval flags.
var DefaultRegressionThresholds = RegressionThresholds{RecallPP: 2, MRRPP: 3}

// Regression names one metric on one route that dropped beyond its threshold.
// Route "overall" is the whole-set aggregate.
type Regression struct {
	Route     string
	Metric    string // "recall" | "mrr"
	Baseline  float64
	Candidate float64
	DeltaPP   float64 // candidate - baseline, in percentage points (negative = drop)
}

// roundPP rounds a percentage-point delta to six decimal places. Metrics
// arrive as float64 fractions (e.g. 0.90, 0.88); the *100 conversion to
// percentage points is subject to ordinary float64 representation error
// (e.g. (0.88-0.90)*100 lands on -2.0000000000000018, not exactly -2.0 —
// verified empirically, this is not a hypothetical), which would make an
// exact-threshold comparison flag noise as a regression. Six decimal places
// is far below any meaningful pp difference but well above that noise floor.
func roundPP(x float64) float64 {
	const scale = 1e6
	return math.Round(x*scale) / scale
}

// CheckRegression compares candidate against baseline on mean recall and MRR,
// overall and per route. Routes present only in the candidate are ignored
// (nothing to compare against); routes present only in the baseline are
// ignored too, matching ComputeRouteDeltas. Results are sorted by route then
// metric so output is stable.
func CheckRegression(baseline, candidate Report, th RegressionThresholds) []Regression {
	var out []Regression
	check := func(route string, b, c AggregateMetrics) {
		if d := roundPP((c.MeanRecall - b.MeanRecall) * 100); d < -th.RecallPP {
			out = append(out, Regression{Route: route, Metric: "recall", Baseline: b.MeanRecall, Candidate: c.MeanRecall, DeltaPP: d})
		}
		if d := roundPP((c.MRR - b.MRR) * 100); d < -th.MRRPP {
			out = append(out, Regression{Route: route, Metric: "mrr", Baseline: b.MRR, Candidate: c.MRR, DeltaPP: d})
		}
	}
	check("overall", baseline.Aggregate, candidate.Aggregate)
	for route, rd := range ComputeRouteDeltas(baseline, candidate) {
		check(route,
			AggregateMetrics{MeanRecall: rd.BaselineRecall, MRR: rd.BaselineMRR},
			AggregateMetrics{MeanRecall: rd.CandidateRecall, MRR: rd.CandidateMRR})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Route != out[j].Route {
			return out[i].Route < out[j].Route
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}

// WriteDeltaTable prints one row per route (overall first) with baseline →
// candidate recall / MRR / nDCG and the pp deltas, tagging rows that carry a
// regression with "REGRESSION". Stable column layout for log scraping.
func WriteDeltaTable(w io.Writer, baseline, candidate Report, regs []Regression) error {
	flagged := map[string]bool{}
	for _, r := range regs {
		flagged[r.Route] = true
	}
	row := func(route string, b, c AggregateMetrics) error {
		tag := ""
		if flagged[route] {
			tag = "  REGRESSION"
		}
		_, err := fmt.Fprintf(w, "  %-18s recall %.3f→%.3f (%+.1f pp)  mrr %.3f→%.3f (%+.1f pp)  ndcg %.3f→%.3f (%+.1f pp)%s\n",
			route,
			b.MeanRecall, c.MeanRecall, (c.MeanRecall-b.MeanRecall)*100,
			b.MRR, c.MRR, (c.MRR-b.MRR)*100,
			b.MeanNDCG, c.MeanNDCG, (c.MeanNDCG-b.MeanNDCG)*100,
			tag)
		return err
	}
	if _, err := fmt.Fprintln(w, "Delta vs baseline:"); err != nil {
		return err
	}
	if err := row("overall", baseline.Aggregate, candidate.Aggregate); err != nil {
		return err
	}
	deltas := ComputeRouteDeltas(baseline, candidate)
	routes := make([]string, 0, len(deltas))
	for r := range deltas {
		routes = append(routes, r)
	}
	sort.Strings(routes)
	for _, r := range routes {
		d := deltas[r]
		if err := row(r,
			AggregateMetrics{MeanRecall: d.BaselineRecall, MRR: d.BaselineMRR, MeanNDCG: d.BaselineNDCG},
			AggregateMetrics{MeanRecall: d.CandidateRecall, MRR: d.CandidateMRR, MeanNDCG: d.CandidateNDCG}); err != nil {
			return err
		}
	}
	return nil
}
