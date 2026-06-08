package eval

import "github.com/justrag/go-backend/internal/vector"

// RoutingAccuracyReport measures how often the production query-type
// classifier — which deterministically drives orchestrator dispatch — agreed
// with the golden set's labeled QueryType. Only questions carrying BOTH a
// golden QueryType and a classified type (Agent.ClassifiedQueryType, set by
// OrchestratorDispatchAdapter) are scored; everything else is skipped, so the
// report is nil for retrieval-only or fully-unlabeled runs and the JSON field
// stays omitempty.
type RoutingAccuracyReport struct {
	Scored   int     `json:"scored"`
	Correct  int     `json:"correct"`
	Accuracy float64 `json:"accuracy"`
	// PerExpected breaks the score down by (normalized) golden QueryType so a
	// low overall number points at the offending route.
	PerExpected map[string]RoutingAccuracyBucket `json:"per_expected,omitempty"`
}

// RoutingAccuracyBucket is one expected-route slice of the routing score.
type RoutingAccuracyBucket struct {
	Scored  int `json:"scored"`
	Correct int `json:"correct"`
}

// normalizeRoutingType folds golden-only labels onto the classifier's output
// space. "global_synthesis" is not a classifier verdict; it shares the
// complex_reasoning dispatch path (non-lookup, non-enumeration), so it is
// compared as complex_reasoning.
func normalizeRoutingType(t string) string {
	if t == "global_synthesis" {
		return vector.QueryTypeComplexReasoning
	}
	return t
}

// RoutingAccuracy scores query-type classification against the golden labels.
// Returns nil when no question is scorable (keeps Report.RoutingAccuracy
// omitempty and on-disk reports byte-stable for legacy runs).
func RoutingAccuracy(reports []QuestionReport) *RoutingAccuracyReport {
	rep := &RoutingAccuracyReport{PerExpected: map[string]RoutingAccuracyBucket{}}
	for _, r := range reports {
		if r.Error != "" || r.Agent == nil {
			continue
		}
		expected, classified := r.Question.QueryType, r.Agent.ClassifiedQueryType
		if expected == "" || classified == "" {
			continue
		}
		exp := normalizeRoutingType(expected)
		rep.Scored++
		b := rep.PerExpected[exp]
		b.Scored++
		if exp == normalizeRoutingType(classified) {
			rep.Correct++
			b.Correct++
		}
		rep.PerExpected[exp] = b
	}
	if rep.Scored == 0 {
		return nil
	}
	rep.Accuracy = float64(rep.Correct) / float64(rep.Scored)
	return rep
}
