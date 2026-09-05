package eval

import (
	"bytes"
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

func boolPtrForTest(b bool) *bool { return &b }

// TestTabularRouterRates_NoFieldPresentUsesLegacyQueryTypeRule is Ruling
// R75 case (a): none of TestTabularRouterRates_ThreeSyntheticTraces /
// _ZeroDenominatorsOmitted / _NilAgentTreatedAsNotFired above populate
// Question.TabularExpected, so those three already exercise "old
// behaviour, byte-identical to the pre-R75 numbers" — this test exists
// only to name that fact explicitly and pin it against a regression that
// would make tabularEligibilityIsExplicit misfire on an all-nil slice.
//
// Mutation check: change tabularEligibilityIsExplicit to always return
// true (ignoring whether any question actually carries the field) and
// this test goes red — with all TabularExpected nil, the explicit branch
// evaluates every isEligible to false (nil-flag dereference guarded to
// false), collapsing eligible to 0 and fireRate to nil instead of 0.5.
// Confirmed red 2026-09-05: forcing tabularEligibilityIsExplicit to
// `return true` unconditionally produced fireRate=nil (mismatch: "want
// 0.5"), sqlErrorRate unaffected (its denominator doesn't depend on
// eligibility) — see Task 12 report for the full transcript.
func TestTabularRouterRates_NoFieldPresentUsesLegacyQueryTypeRule(t *testing.T) {
	reports := []QuestionReport{
		{
			Question: Question{QueryType: "lookup"},
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: true, Outcome: "fired_ok"}},
		},
		{
			Question: Question{QueryType: "lookup"},
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: false, Outcome: "skipped_no_cue"}},
		},
	}
	fireRate, _ := TabularRouterRates(reports)
	if fireRate == nil {
		t.Fatal("fireRate = nil, want 0.5")
	}
	if math.Abs(*fireRate-0.5) > 1e-9 {
		t.Errorf("fireRate = %v, want 0.5", *fireRate)
	}
}

// TestTabularRouterRates_R75ExplicitFlagOverridesQueryType is Ruling R75
// case (b): a mix of questions where TabularExpected is set (true or
// false) and one where it is left nil, in a report set where at least one
// question DOES carry the field — so the explicit-flag rule applies to
// ALL of them, not just the ones that set it.
//
//   - a: query_type=lookup, tabular_expected=true, fired → counts as
//     eligible AND fired.
//   - b: query_type=lookup, tabular_expected=false, fired → the router
//     fired anyway (e.g. on a form-field region it should have skipped),
//     but R75 says a false flag excludes the question from BOTH the
//     numerator and the denominator — this must not raise the rate.
//   - c: query_type=lookup, tabular_expected=nil (in a set where b/d
//     carry the field), fired → excluded from eligibility entirely, same
//     as b, even though its query_type alone would have made it eligible
//     under the legacy rule.
//   - d: query_type=lookup, tabular_expected=true, not fired → eligible,
//     not fired.
//
// Eligible = {a, d} = 2. Fired-and-eligible = {a} = 1. fireRate = 1/2.
// A denominator/numerator computed under the OLD query_type rule would
// instead count all four as eligible (all query_type=lookup) and three
// as fired (a, b, c — c is also fired=true) → 3/4 = 0.75, which differs
// from 0.5, so the ratio assertion alone does catch this mutation.
//
// Mutation check: replace the explicit/legacy branch with an
// unconditional `isEligible := eligibleTabularQueryTypes[r.Question.QueryType]`
// (ignore `explicit` entirely) — eligible becomes 4, firedEligible becomes
// 3 (a, b, c), fireRate becomes 0.75 instead of 0.5. Confirmed red
// 2026-09-05: got "fireRate = 0.75, want 0.5" — see Task 12 report for
// the full transcript.
func TestTabularRouterRates_R75ExplicitFlagOverridesQueryType(t *testing.T) {
	reports := []QuestionReport{
		{ // a
			Question: Question{QueryType: "lookup", TabularExpected: boolPtrForTest(true)},
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: true, Outcome: "fired_ok"}},
		},
		{ // b
			Question: Question{QueryType: "lookup", TabularExpected: boolPtrForTest(false)},
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: true, Outcome: "fired_ok"}},
		},
		{ // c
			Question: Question{QueryType: "lookup"}, // TabularExpected nil
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: true, Outcome: "fired_ok"}},
		},
		{ // d
			Question: Question{QueryType: "lookup", TabularExpected: boolPtrForTest(true)},
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: false, Outcome: "skipped_no_cue"}},
		},
	}

	fireRate, _ := TabularRouterRates(reports)
	if fireRate == nil {
		t.Fatal("fireRate = nil, want 0.5")
	}
	if math.Abs(*fireRate-0.5) > 1e-9 {
		t.Errorf("fireRate = %v, want 0.5 (eligible={a,d}=2, fired-and-eligible={a}=1)", *fireRate)
	}
}

// TestTabularRouterRates_R75ExplicitFlagExcludesFalseAndNilEvenWhenFired
// is a second, independent fixture for the same mutation as the test
// above, using query_type=complex_reasoning (the other eligible legacy
// label) instead of lookup, so the two tests together cover both
// branches of eligibleTabularQueryTypes.
//
//   - a: tabular_expected=true, fired → eligible, fired.
//   - b: tabular_expected=false, fired → legacy rule would count it
//     eligible+fired (raising the rate); R75 must not.
//   - c: tabular_expected=false, NOT fired → excluded either way, added
//     only so the false-labeled bucket isn't a single question.
//
// R75: eligible={a}=1, fired-and-eligible={a}=1 → fireRate=1.0.
// Legacy (query_type only, ignoring the flags): all three are
// query_type=complex_reasoning → eligible=3, fired-and-eligible={a,b}=2
// → fireRate=0.667. Confirmed red 2026-09-05 under the same mutation as
// the previous test — see Task 12 report for the transcript.
func TestTabularRouterRates_R75ExplicitFlagExcludesFalseAndNilEvenWhenFired(t *testing.T) {
	reports := []QuestionReport{
		{
			Question: Question{QueryType: "complex_reasoning", TabularExpected: boolPtrForTest(true)},
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: true, Outcome: "fired_ok"}},
		},
		{
			Question: Question{QueryType: "complex_reasoning", TabularExpected: boolPtrForTest(false)},
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: true, Outcome: "fired_ok"}},
		},
		{
			Question: Question{QueryType: "complex_reasoning", TabularExpected: boolPtrForTest(false)},
			Agent:    &AgentTrace{Tabular: &TabularEvalTrace{Fired: false, Outcome: "skipped_no_cue"}},
		},
	}

	fireRate, _ := TabularRouterRates(reports)
	if fireRate == nil {
		t.Fatal("fireRate = nil, want 1.0")
	}
	if math.Abs(*fireRate-1.0) > 1e-9 {
		t.Errorf("fireRate = %v, want 1.0 (only the tabular_expected=true question is eligible)", *fireRate)
	}
}

// TestWriteHumanSummary_TabularFireRateWordingReflectsRule is Ruling R75
// case (c): the human-readable summary line for fire_rate must name
// whichever eligibility rule actually produced the number — the legacy
// query_type wording when no question in the report carries
// tabular_expected, and the tabular_expected wording when at least one
// does — so an operator reading the summary isn't misled about what the
// denominator was.
//
// Mutation check: hardcode report.go's eligibilityDesc to always the
// legacy string (drop the tabularEligibilityIsExplicit(rep.Questions)
// check) and the second case below goes red. Confirmed red 2026-09-05,
// see Task 12 report.
func TestWriteHumanSummary_TabularFireRateWordingReflectsRule(t *testing.T) {
	rate := 0.5

	t.Run("legacy query_type wording when no question carries the field", func(t *testing.T) {
		rep := Report{
			Questions:             []QuestionReport{{Question: Question{QueryType: "lookup"}}},
			TabularRouterFireRate: &rate,
		}
		var buf bytes.Buffer
		if err := WriteHumanSummary(&buf, rep); err != nil {
			t.Fatalf("WriteHumanSummary: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "(of lookup/complex_reasoning questions)") {
			t.Errorf("summary missing legacy wording, got:\n%s", out)
		}
		if strings.Contains(out, "tabular_expected questions") {
			t.Errorf("summary should not use explicit-flag wording, got:\n%s", out)
		}
	})

	t.Run("tabular_expected wording when at least one question carries the field", func(t *testing.T) {
		rep := Report{
			Questions: []QuestionReport{
				{Question: Question{QueryType: "lookup", TabularExpected: boolPtrForTest(true)}},
			},
			TabularRouterFireRate: &rate,
		}
		var buf bytes.Buffer
		if err := WriteHumanSummary(&buf, rep); err != nil {
			t.Fatalf("WriteHumanSummary: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "(of tabular_expected questions)") {
			t.Errorf("summary missing explicit-flag wording, got:\n%s", out)
		}
		if strings.Contains(out, "lookup/complex_reasoning questions") {
			t.Errorf("summary should not use legacy wording, got:\n%s", out)
		}
	})
}

// TestWriteHumanSummary_TabularFireRateNilExplicitPrintsNA covers the case
// the fire_rate wording test above does not: a golden set that DOES carry
// Question.TabularExpected (so the explicit R75 rule is in effect) but has
// zero questions with TabularExpected == true, so TabularRouterRates
// returns a nil fireRate (0/0 is undefined, not 0.0). That nil is a
// genuine, reportable fact about the golden set — "this run had nothing
// the router was expected to fire on" — and must render as an explicit
// "n/a (0 tabular_expected questions)" line, not silently vanish the way a
// nil fire_rate under the legacy query_type rule still does (no tabular
// signal at all in that case means there's nothing informative to say).
//
// Mutation: removing the `case explicitTabularEligibility:` branch in
// report.go (reverting to only the `if rep.TabularRouterFireRate != nil`
// check, with no outer `|| explicitTabularEligibility`) makes this go RED —
// confirmed below.
func TestWriteHumanSummary_TabularFireRateNilExplicitPrintsNA(t *testing.T) {
	t.Run("explicit rule, 0 tabular_expected questions prints n/a", func(t *testing.T) {
		rep := Report{
			Questions: []QuestionReport{
				{Question: Question{QueryType: "lookup", TabularExpected: boolPtrForTest(false)}},
			},
			// TabularRouterFireRate left nil: TabularRouterRates(reports)
			// would return nil here too (eligible == 0 under R75, since the
			// one question's flag is false) — set directly to isolate the
			// report.go rendering logic from the rate computation.
		}
		var buf bytes.Buffer
		if err := WriteHumanSummary(&buf, rep); err != nil {
			t.Fatalf("WriteHumanSummary: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "Tabular router:") {
			t.Fatalf("expected a Tabular router section to be printed, got:\n%s", out)
		}
		if !strings.Contains(out, "fire_rate      = n/a (0 tabular_expected questions)") {
			t.Errorf("expected the n/a line, got:\n%s", out)
		}
	})

	t.Run("legacy rule, nil fire_rate stays silent", func(t *testing.T) {
		rep := Report{
			Questions: []QuestionReport{
				{Question: Question{QueryType: "enumeration"}}, // no TabularExpected anywhere -> legacy rule
			},
			// TabularRouterFireRate and TabularSQLErrorRate both nil.
		}
		var buf bytes.Buffer
		if err := WriteHumanSummary(&buf, rep); err != nil {
			t.Fatalf("WriteHumanSummary: %v", err)
		}
		out := buf.String()
		if strings.Contains(out, "Tabular router:") {
			t.Errorf("legacy-rule nil fire_rate must stay silent (no tabular signal at all), got:\n%s", out)
		}
	})
}
