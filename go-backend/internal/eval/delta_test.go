package eval

import "testing"

func TestComputeRouteDeltas(t *testing.T) {
	baseline := Report{
		K: 10,
		RouteAggregates: map[string]AggregateMetrics{
			"lookup":      {K: 10, Count: 40, MeanRecall: 0.500, MeanPrecision: 0.100, MRR: 0.200, MeanNDCG: 0.300},
			"enumeration": {K: 10, Count: 14, MeanRecall: 0.250, MeanPrecision: 0.050, MRR: 0.100, MeanNDCG: 0.150},
		},
	}
	candidate := Report{
		K: 10,
		RouteAggregates: map[string]AggregateMetrics{
			"lookup":      {K: 10, Count: 40, MeanRecall: 0.600, MeanPrecision: 0.120, MRR: 0.250, MeanNDCG: 0.350},
			"enumeration": {K: 10, Count: 14, MeanRecall: 0.200, MeanPrecision: 0.040, MRR: 0.080, MeanNDCG: 0.140},
		},
	}
	got := ComputeRouteDeltas(baseline, candidate)
	if d := got["lookup"].RecallDelta; !almostEqual(d, 0.100) {
		t.Errorf("lookup recall delta = %.3f, want 0.100", d)
	}
	if d := got["enumeration"].RecallDelta; !almostEqual(d, -0.050) {
		t.Errorf("enumeration recall delta = %.3f, want -0.050", d)
	}
}

func TestComputeRouteDeltas_MissingRouteInCandidate(t *testing.T) {
	baseline := Report{
		RouteAggregates: map[string]AggregateMetrics{
			"lookup":      {MeanRecall: 0.5},
			"enumeration": {MeanRecall: 0.3},
		},
	}
	candidate := Report{
		RouteAggregates: map[string]AggregateMetrics{
			"lookup": {MeanRecall: 0.6},
		},
	}
	got := ComputeRouteDeltas(baseline, candidate)
	if _, ok := got["enumeration"]; ok {
		t.Error("expected no delta for enumeration (missing in candidate)")
	}
	if _, ok := got["lookup"]; !ok {
		t.Error("expected lookup delta present")
	}
}

func almostEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.001
}
