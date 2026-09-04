package eval

// eligibleTabularQueryTypes are the golden query_type labels the
// deterministic tabular SQL router is expected to engage on. The golden
// schema (Question.QueryType) has no "aggregation" label — its allowed
// values are lookup / enumeration / global_synthesis / complex_reasoning —
// so complex_reasoning stands in for it: aggregation-shaped questions
// ("how many rows have X in total") are labeled complex_reasoning in
// existing golden sets, not enumeration or global_synthesis.
var eligibleTabularQueryTypes = map[string]bool{
	"lookup":            true,
	"complex_reasoning": true,
}

// tabularOf reads the Tabular sub-trace off a QuestionReport, tolerating a
// nil Agent (adapters that don't dispatch through an orchestrator, or an
// errored question).
func tabularOf(r QuestionReport) *TabularEvalTrace {
	if r.Agent == nil {
		return nil
	}
	return r.Agent.Tabular
}

// TabularRouterRates computes the deterministic tabular router's fire rate
// and SQL error rate from a completed eval run's per-question reports.
//
// fireRate is fired / (questions whose golden query_type is in
// eligibleTabularQueryTypes) — how often the router engaged on a question
// shape it's built for. The denominator is the ELIGIBLE subset, not every
// question in the run: a golden set dominated by enumeration/global_synthesis
// questions the router is not meant to fire on would otherwise make the
// rate look artificially low.
//
// sqlErrorRate is (sql_error + validator_rejected) / fired — of the turns
// the router actually attempted (an SQL generation call was made,
// regardless of the question's query_type), how often it ended in an
// unusable statement.
//
// Both return nil when their respective denominator is 0, so the Report
// JSON omits the field instead of emitting a misleading 0.0.
func TabularRouterRates(reports []QuestionReport) (fireRate, sqlErrorRate *float64) {
	var eligible, firedEligible int
	var firedTotal, sqlErr int

	for _, r := range reports {
		tab := tabularOf(r)
		if eligibleTabularQueryTypes[r.Question.QueryType] {
			eligible++
			if tab != nil && tab.Fired {
				firedEligible++
			}
		}
		if tab != nil && tab.Fired {
			firedTotal++
			if tab.Outcome == "sql_error" || tab.Outcome == "validator_rejected" {
				sqlErr++
			}
		}
	}

	if eligible > 0 {
		v := float64(firedEligible) / float64(eligible)
		fireRate = &v
	}
	if firedTotal > 0 {
		v := float64(sqlErr) / float64(firedTotal)
		sqlErrorRate = &v
	}
	return fireRate, sqlErrorRate
}
