package eval

import (
	"bytes"
	"strings"
	"testing"
)

// mkReport builds a synthetic Report for regression-check tests. It carries
// one placeholder QuestionReport — not because these tests inspect
// Questions, but because ReadJSONReport (F4) rejects a report with zero
// questions, and TestReadJSONReport_RoundTrip below round-trips a mkReport
// through JSON.
func mkReport(overallRecall, overallMRR float64, routes map[string][2]float64) Report {
	rep := Report{
		Questions:       []QuestionReport{{Question: Question{ID: "q1"}}},
		Aggregate:       AggregateMetrics{K: 10, Count: 10, MeanRecall: overallRecall, MRR: overallMRR},
		RouteAggregates: map[string]AggregateMetrics{},
	}
	for r, v := range routes {
		rep.RouteAggregates[r] = AggregateMetrics{K: 10, Count: 5, MeanRecall: v[0], MRR: v[1]}
	}
	return rep
}

func TestCheckRegression_NoneWithinThreshold(t *testing.T) {
	base := mkReport(0.90, 0.90, map[string][2]float64{"lookup": {0.95, 0.90}})
	cand := mkReport(0.89, 0.88, map[string][2]float64{"lookup": {0.94, 0.88}}) // -1pp recall, -2pp MRR
	regs := CheckRegression(base, cand, RegressionThresholds{RecallPP: 2, MRRPP: 3})
	if len(regs) != 0 {
		t.Fatalf("want no regressions, got %+v", regs)
	}
}

func TestCheckRegression_FlagsOverallAndRoute(t *testing.T) {
	base := mkReport(0.90, 0.90, map[string][2]float64{"lookup": {0.95, 0.90}, "enumeration": {0.90, 1.0}})
	cand := mkReport(0.87, 0.90, map[string][2]float64{"lookup": {0.95, 0.85}, "enumeration": {0.90, 1.0}}) // overall recall -3pp; lookup MRR -5pp
	regs := CheckRegression(base, cand, RegressionThresholds{RecallPP: 2, MRRPP: 3})
	if len(regs) != 2 {
		t.Fatalf("want 2 regressions, got %d: %+v", len(regs), regs)
	}
	want := map[string]bool{"overall/recall": false, "lookup/mrr": false}
	for _, r := range regs {
		want[r.Route+"/"+r.Metric] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("missing regression %s", k)
		}
	}
}

func TestCheckRegression_ExactlyAtThresholdIsNotARegression(t *testing.T) {
	base := mkReport(0.90, 0.90, nil)
	cand := mkReport(0.88, 0.90, nil) // exactly -2pp
	regs := CheckRegression(base, cand, RegressionThresholds{RecallPP: 2, MRRPP: 3})
	if len(regs) != 0 {
		t.Fatalf("exactly at threshold must not flag, got %+v", regs)
	}
}

func TestCheckRegression_MRRExactlyAtThresholdIsNotARegression(t *testing.T) {
	base := mkReport(0.90, 0.90, nil)
	cand := mkReport(0.90, 0.87, nil) // recall unchanged; MRR exactly -3pp
	regs := CheckRegression(base, cand, RegressionThresholds{RecallPP: 2, MRRPP: 3})
	if len(regs) != 0 {
		t.Fatalf("MRR exactly at threshold must not flag, got %+v", regs)
	}
}

func TestCheckRegression_RouteOnlyInCandidateIsIgnored(t *testing.T) {
	base := mkReport(0.90, 0.90, nil)
	cand := mkReport(0.90, 0.90, map[string][2]float64{"lookup": {0.10, 0.10}})
	if regs := CheckRegression(base, cand, RegressionThresholds{RecallPP: 2, MRRPP: 3}); len(regs) != 0 {
		t.Fatalf("route absent from baseline must be skipped, got %+v", regs)
	}
}

func TestWriteDeltaTable_ContainsRoutesAndMarksRegressions(t *testing.T) {
	base := mkReport(0.90, 0.90, map[string][2]float64{"lookup": {0.95, 0.90}})
	cand := mkReport(0.86, 0.90, map[string][2]float64{"lookup": {0.95, 0.90}})
	regs := CheckRegression(base, cand, RegressionThresholds{RecallPP: 2, MRRPP: 3})
	var buf bytes.Buffer
	if err := WriteDeltaTable(&buf, base, cand, regs); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, needle := range []string{"overall", "lookup", "REGRESSION", "-4.0"} {
		if !strings.Contains(out, needle) {
			t.Errorf("delta table missing %q:\n%s", needle, out)
		}
	}
}

func TestRoundPP_AbsorbsFloat64Noise(t *testing.T) {
	// b, c as typed float64 variables (not untyped constants, which the
	// compiler would fold in arbitrary precision) reproduce the real
	// runtime noise: (c-b)*100 lands on -2.0000000000000018, not the
	// exact -2.0 a naive reading of the arithmetic suggests. roundPP must
	// collapse that noise away.
	var b, c float64 = 0.90, 0.88
	if got := roundPP((c - b) * 100); got != -2 {
		t.Fatalf("roundPP((c-b)*100) = %v, want -2", got)
	}
}

func TestReadJSONReport_RoundTrip(t *testing.T) {
	rep := mkReport(0.5, 0.6, map[string][2]float64{"lookup": {0.7, 0.8}})
	var buf bytes.Buffer
	if err := WriteJSONReport(&buf, rep); err != nil {
		t.Fatal(err)
	}
	got, err := ReadJSONReport(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Aggregate.MeanRecall != 0.5 || got.RouteAggregates["lookup"].MRR != 0.8 {
		t.Fatalf("round trip lost data: %+v", got)
	}
}
