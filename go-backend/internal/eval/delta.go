package eval

// RouteDelta holds per-metric differences between a candidate and a baseline
// for a single route. Positive values mean the candidate scored higher.
type RouteDelta struct {
	Route              string
	Count              int
	BaselineCount      int
	RecallDelta        float64
	PrecisionDelta     float64
	MRRDelta           float64
	NDCGDelta          float64
	BaselineRecall     float64
	CandidateRecall    float64
	BaselinePrecision  float64
	CandidatePrecision float64
	BaselineMRR        float64
	CandidateMRR       float64
	BaselineNDCG       float64
	CandidateNDCG      float64
}

// ComputeRouteDeltas returns one RouteDelta per route that exists in BOTH
// reports. Routes present in only one side are omitted. Shared by the in-app
// eval runner's markdown export (internal/eval/markdown_export.go) and the
// standalone cmd/eval_delta tool (Plan 2).
func ComputeRouteDeltas(baseline, candidate Report) map[string]RouteDelta {
	out := make(map[string]RouteDelta, len(baseline.RouteAggregates))
	for route, b := range baseline.RouteAggregates {
		c, ok := candidate.RouteAggregates[route]
		if !ok {
			continue
		}
		out[route] = RouteDelta{
			Route:              route,
			Count:              c.Count,
			BaselineCount:      b.Count,
			RecallDelta:        c.MeanRecall - b.MeanRecall,
			PrecisionDelta:     c.MeanPrecision - b.MeanPrecision,
			MRRDelta:           c.MRR - b.MRR,
			NDCGDelta:          c.MeanNDCG - b.MeanNDCG,
			BaselineRecall:     b.MeanRecall,
			CandidateRecall:    c.MeanRecall,
			BaselinePrecision:  b.MeanPrecision,
			CandidatePrecision: c.MeanPrecision,
			BaselineMRR:        b.MRR,
			CandidateMRR:       c.MRR,
			BaselineNDCG:       b.MeanNDCG,
			CandidateNDCG:      c.MeanNDCG,
		}
	}
	return out
}
