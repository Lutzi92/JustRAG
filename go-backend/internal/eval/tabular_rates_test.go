package eval

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// TestTabularRouterRates_ThreeSyntheticTraces is the case called out in the
// task brief: three QuestionReports mixing eligible/ineligible query types
// and fired/unfired/errored tabular traces, chosen so that computing the
// fire rate over ALL questions (instead of just the eligible subset)
// produces a different, wrong number.
//
//   - q0: query_type=lookup (eligible), tabular fired, outcome=fired_ok
//   - q1: query_type=enumeration (NOT eligible), tabular fired, outcome=sql_error
//   - q2: query_type=lookup (eligible), tabular did not fire (skipped)
//
// Eligible questions: q0, q2 (2). Of those, q0 fired → fire_rate = 1/2.
// A denominator of "all questions" (3) would instead yield 1/3 — the
// mutation this test guards against.
//
// Fired questions (any query type): q0, q1 (2). Of those, q1 ended in
// sql_error → sql_error_rate = 1/2.
func TestTabularRouterRates_ThreeSyntheticTraces(t *testing.T) {
	reports := []QuestionReport{
		{
			Question: Question{QueryType: "lookup"},
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: true, Outcome: "fired_ok"}},
		},
		{
			Question: Question{QueryType: "enumeration"},
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: true, Outcome: "sql_error"}},
		},
		{
			Question: Question{QueryType: "lookup"},
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: false, Outcome: "skipped_no_cue"}},
		},
	}

	fireRate, sqlErrorRate := TabularRouterRates(reports)

	if fireRate == nil {
		t.Fatal("fireRate = nil, want 0.5")
	}
	if math.Abs(*fireRate-0.5) > 1e-9 {
		t.Errorf("fireRate = %v, want 0.5", *fireRate)
	}

	if sqlErrorRate == nil {
		t.Fatal("sqlErrorRate = nil, want 0.5")
	}
	if math.Abs(*sqlErrorRate-0.5) > 1e-9 {
		t.Errorf("sqlErrorRate = %v, want 0.5", *sqlErrorRate)
	}
}

// TestTabularRouterRates_ZeroDenominatorsOmitted ensures both rates come
// back nil (not NaN or 0.0) when their respective denominator is 0, so the
// JSON report omits the fields instead of emitting a misleading zero.
func TestTabularRouterRates_ZeroDenominatorsOmitted(t *testing.T) {
	reports := []QuestionReport{
		{Question: Question{QueryType: "enumeration"}}, // not eligible, no Agent at all
	}
	fireRate, sqlErrorRate := TabularRouterRates(reports)
	if fireRate != nil {
		t.Errorf("fireRate = %v, want nil (no eligible questions)", *fireRate)
	}
	if sqlErrorRate != nil {
		t.Errorf("sqlErrorRate = %v, want nil (no fired questions)", *sqlErrorRate)
	}
}

// TestTabularRouterRates_NilAgentTreatedAsNotFired guards against a nil
// Agent (adapters that don't dispatch through an orchestrator, or an
// errored question) panicking or miscounting.
func TestTabularRouterRates_NilAgentTreatedAsNotFired(t *testing.T) {
	reports := []QuestionReport{
		{Question: Question{QueryType: "lookup"}, Agent: nil},
		{Question: Question{QueryType: "lookup"}, Agent: &AgentTrace{}}, // Tabular nil
	}
	fireRate, sqlErrorRate := TabularRouterRates(reports)
	if fireRate == nil || *fireRate != 0 {
		t.Errorf("fireRate = %v, want 0 (2 eligible, 0 fired)", fireRate)
	}
	if sqlErrorRate != nil {
		t.Errorf("sqlErrorRate = %v, want nil (0 fired)", *sqlErrorRate)
	}
}

// TestReport_TabularRates_JSONOmitsWhenNil confirms the Report fields stay
// out of the JSON payload when nil, keeping legacy on-disk reports
// byte-stable.
func TestReport_TabularRates_JSONOmitsWhenNil(t *testing.T) {
	rep := Report{}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if strings.Contains(s, "tabular_router_fire_rate") || strings.Contains(s, "tabular_sql_error_rate") {
		t.Errorf("expected tabular rate fields to be omitted when nil, got %s", s)
	}
}
