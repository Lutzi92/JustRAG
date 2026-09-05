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

// tabularEligibilityIsExplicit reports whether ANY question in reports
// carries a non-nil Question.TabularExpected. Per Ruling R75: the presence
// of the field on even one question in the set switches the
// router-eligibility rule (both here and in the report summary line) from
// the query_type fallback to the explicit tabular_expected flag. A golden
// set that mixes annotated and un-annotated questions (there shouldn't be
// one, but nothing enforces it) is treated as "explicit" so a stray nil
// question doesn't silently fall back to being counted as eligible.
func tabularEligibilityIsExplicit(reports []QuestionReport) bool {
	for _, r := range reports {
		if r.Question.TabularExpected != nil {
			return true
		}
	}
	return false
}

// TabularRouterRates computes the deterministic tabular router's fire rate
// and SQL error rate from a completed eval run's per-question reports.
//
// fireRate is fired / eligible, where "eligible" is determined per Ruling
// R75: if any question in reports carries a non-nil TabularExpected, the
// denominator is exactly the questions with TabularExpected == true (a
// false or absent flag excludes a question from both numerator and
// denominator, even if the router fired on it anyway — e.g. a form-field
// region the router should skip). Otherwise it falls back to the legacy
// rule: questions whose golden query_type is in eligibleTabularQueryTypes.
// The denominator is the ELIGIBLE subset, not every question in the run: a
// golden set dominated by questions the router is not meant to fire on
// would otherwise make the rate look artificially low.
//
// sqlErrorRate is (sql_error + validator_rejected) / fired — of the turns
// the router actually attempted (an SQL generation call was made,
// regardless of eligibility), how often it ended in an unusable statement.
// This denominator is unchanged by R75.
//
// Both return nil when their respective denominator is 0, so the Report
// JSON omits the field instead of emitting a misleading 0.0.
func TabularRouterRates(reports []QuestionReport) (fireRate, sqlErrorRate *float64) {
	explicit := tabularEligibilityIsExplicit(reports)

	var eligible, firedEligible int
	var firedTotal, sqlErr int

	for _, r := range reports {
		tab := tabularOf(r)

		isEligible := false
		if explicit {
			isEligible = r.Question.TabularExpected != nil && *r.Question.TabularExpected
		} else {
			isEligible = eligibleTabularQueryTypes[r.Question.QueryType]
		}
		if isEligible {
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
