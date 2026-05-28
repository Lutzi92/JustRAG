package eval

// AggregateByOrchestrator buckets non-errored QuestionReports by which
// orchestrator handled them (qr.Agent.Orchestrator) and computes the
// standard AggregateMetrics per bucket. Mirrors AggregateByRoute.
//
// Reports with Agent == nil are skipped so legacy adapters (that don't
// dispatch through an orchestrator) don't produce a misleading
// empty-keyed bucket. Returns nil when no buckets exist so the field
// stays omitempty in JSON output.
func AggregateByOrchestrator(reports []QuestionReport, k int) map[string]AggregateMetrics {
	buckets := make(map[string][]QuestionReport)
	for _, r := range reports {
		if r.Agent == nil {
			continue
		}
		if r.Error != "" {
			continue
		}
		buckets[r.Agent.Orchestrator] = append(buckets[r.Agent.Orchestrator], r)
	}
	if len(buckets) == 0 {
		return nil
	}
	out := make(map[string]AggregateMetrics, len(buckets))
	for orchestrator, bucket := range buckets {
		out[orchestrator] = Aggregate(bucket, k)
	}
	return out
}
