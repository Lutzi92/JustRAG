package chatpolicy

import (
	"strings"
	"testing"
)

func ptrBool(b bool) *bool { return &b }
func ptrInt(i int) *int    { return &i }

// TestParseOrchestratorPolicy_Empty pins the "unset key" contract: an empty or
// whitespace-only value is not an error, it is "no policy" — the ladder is
// unchanged. Without this, an operator clearing the field would 400 on save.
func TestParseOrchestratorPolicy_Empty(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "   ", "\n\t "} {
		p, err := ParseOrchestratorPolicy(raw)
		if err != nil {
			t.Fatalf("ParseOrchestratorPolicy(%q): unexpected error %v", raw, err)
		}
		if p != nil {
			t.Fatalf("ParseOrchestratorPolicy(%q) = %v, want nil", raw, p)
		}
	}
}

// TestParseOrchestratorPolicy_Valid checks the happy path decodes every When
// field, including the pointer-bool tri-state (absent vs false).
func TestParseOrchestratorPolicy_Valid(t *testing.T) {
	t.Parallel()
	raw := `[
	  {"when": {"query_type": ["lookup"], "global_synthesis": false, "enumeration": true,
	            "recency_listing": false, "has_file_selection": true, "history_turns_gte": 3,
	            "kb_ids": ["kb-1", "kb-2"]},
	   "orchestrator": "supervisor", "mode": "prefer"},
	  {"when": {}, "orchestrator": "standard", "mode": "force"}
	]`
	p, err := ParseOrchestratorPolicy(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p) != 2 {
		t.Fatalf("len = %d, want 2", len(p))
	}
	r0 := p[0]
	if r0.Orchestrator != "supervisor" || r0.Mode != ModePrefer {
		t.Fatalf("rule 0 = %+v", r0)
	}
	if len(r0.When.QueryType) != 1 || r0.When.QueryType[0] != "lookup" {
		t.Fatalf("query_type = %v", r0.When.QueryType)
	}
	if r0.When.GlobalSynthesis == nil || *r0.When.GlobalSynthesis {
		t.Fatalf("global_synthesis = %v, want pointer to false", r0.When.GlobalSynthesis)
	}
	if r0.When.Enumeration == nil || !*r0.When.Enumeration {
		t.Fatalf("enumeration = %v, want pointer to true", r0.When.Enumeration)
	}
	if r0.When.RecencyListing == nil || *r0.When.RecencyListing {
		t.Fatalf("recency_listing = %v", r0.When.RecencyListing)
	}
	if r0.When.HasFileSelection == nil || !*r0.When.HasFileSelection {
		t.Fatalf("has_file_selection = %v", r0.When.HasFileSelection)
	}
	if r0.When.HistoryTurnsGTE == nil || *r0.When.HistoryTurnsGTE != 3 {
		t.Fatalf("history_turns_gte = %v", r0.When.HistoryTurnsGTE)
	}
	if len(r0.When.KBIDs) != 2 {
		t.Fatalf("kb_ids = %v", r0.When.KBIDs)
	}
	// Absent pointer fields stay nil — "not tested", not "tested against false".
	if p[1].When.GlobalSynthesis != nil || p[1].When.HistoryTurnsGTE != nil {
		t.Fatalf("rule 1 When should be all-nil, got %+v", p[1].When)
	}
}

// TestParseOrchestratorPolicy_Errors gives every validation rule its own case,
// each asserting a substring an operator would see in the 400 body.
func TestParseOrchestratorPolicy_Errors(t *testing.T) {
	t.Parallel()
	manyRules := "[" + strings.Repeat(`{"when":{},"orchestrator":"standard","mode":"force"},`, MaxRules) +
		`{"when":{},"orchestrator":"standard","mode":"force"}]`

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"not an array (object)", `{"orchestrator":"standard"}`, "must be a JSON array"},
		{"not an array (string)", `"standard"`, "must be a JSON array"},
		{"malformed JSON", `[{"orchestrator":]`, "invalid"},
		{"trailing data", `[] []`, "trailing"},
		{"too many rules", manyRules, "at most 32 rules"},
		{"unknown orchestrator", `[{"when":{},"orchestrator":"nope","mode":"force"}]`, `unknown orchestrator "nope"`},
		{"empty orchestrator", `[{"when":{},"mode":"force"}]`, "unknown orchestrator"},
		{"unknown mode", `[{"when":{},"orchestrator":"standard","mode":"maybe"}]`, `unknown mode "maybe"`},
		{"empty mode", `[{"when":{},"orchestrator":"standard"}]`, "unknown mode"},
		{"unknown query_type", `[{"when":{"query_type":["chitchat"]},"orchestrator":"standard","mode":"force"}]`, `unknown query_type "chitchat"`},
		{"empty kb_id", `[{"when":{"kb_ids":["a",""]},"orchestrator":"standard","mode":"force"}]`, "kb_ids"},
		{"unknown top-level key", `[{"when":{},"orchestrator":"standard","mode":"force","extra":1}]`, "unknown field"},
		{"unknown when key", `[{"when":{"nope":true},"orchestrator":"standard","mode":"force"}]`, "unknown field"},
		{"wrong type for when", `[{"when":true,"orchestrator":"standard","mode":"force"}]`, "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseOrchestratorPolicy(tc.raw)
			if err == nil {
				t.Fatalf("ParseOrchestratorPolicy(%q): want error, got nil", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// TestParseOrchestratorPolicy_RuleIndexInError pins that the message names the
// offending rule — a 32-rule document is unusable feedback otherwise.
func TestParseOrchestratorPolicy_RuleIndexInError(t *testing.T) {
	t.Parallel()
	raw := `[{"when":{},"orchestrator":"standard","mode":"force"},
	         {"when":{},"orchestrator":"bogus","mode":"force"}]`
	_, err := ParseOrchestratorPolicy(raw)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "rule 1") {
		t.Fatalf("error %q should name rule 1", err.Error())
	}
}

// TestParseOrchestratorPolicy_AllOrchestratorsAccepted keeps the parser and
// the documented name list from drifting apart.
func TestParseOrchestratorPolicy_AllOrchestratorsAccepted(t *testing.T) {
	t.Parallel()
	for _, o := range Orchestrators {
		raw := `[{"when":{},"orchestrator":"` + o + `","mode":"force"}]`
		if _, err := ParseOrchestratorPolicy(raw); err != nil {
			t.Fatalf("orchestrator %q rejected: %v", o, err)
		}
	}
	for _, q := range QueryTypes {
		raw := `[{"when":{"query_type":["` + q + `"]},"orchestrator":"standard","mode":"force"}]`
		if _, err := ParseOrchestratorPolicy(raw); err != nil {
			t.Fatalf("query_type %q rejected: %v", q, err)
		}
	}
}

// TestValidateOrchestratorPolicyJSON is the save-time entry point; it must
// agree with the parser on both verdicts.
func TestValidateOrchestratorPolicyJSON(t *testing.T) {
	t.Parallel()
	if err := ValidateOrchestratorPolicyJSON(`[{"when":{},"orchestrator":"agentic","mode":"prefer"}]`); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
	if err := ValidateOrchestratorPolicyJSON(""); err != nil {
		t.Fatalf("empty document rejected: %v", err)
	}
	if err := ValidateOrchestratorPolicyJSON(`[{"when":{},"orchestrator":"nope","mode":"force"}]`); err == nil {
		t.Fatal("invalid document accepted")
	}
}

// baseSignals is a fully-populated signal set the per-field match tests vary
// one field at a time from.
func baseSignals() Signals {
	return Signals{
		QueryType:        "lookup",
		GlobalSynthesis:  false,
		Enumeration:      false,
		RecencyListing:   false,
		HasFileSelection: false,
		HistoryTurns:     0,
		KBID:             "kb-1",
	}
}

// TestMatch_PerField exercises every When field in both directions — a field
// that is silently ignored would pass the "match" half and fail the "no match"
// half.
func TestMatch_PerField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		when When
		sig  Signals
		want bool
	}{
		{"empty when matches everything", When{}, baseSignals(), true},
		{"query_type in list", When{QueryType: []string{"lookup", "enumeration"}}, baseSignals(), true},
		{"query_type not in list", When{QueryType: []string{"enumeration"}}, baseSignals(), false},
		{"global_synthesis true vs false", When{GlobalSynthesis: ptrBool(true)}, baseSignals(), false},
		{"global_synthesis false vs false", When{GlobalSynthesis: ptrBool(false)}, baseSignals(), true},
		{"global_synthesis true vs true", When{GlobalSynthesis: ptrBool(true)}, func() Signals {
			s := baseSignals()
			s.GlobalSynthesis = true
			return s
		}(), true},
		{"enumeration true vs false", When{Enumeration: ptrBool(true)}, baseSignals(), false},
		{"enumeration true vs true", When{Enumeration: ptrBool(true)}, func() Signals {
			s := baseSignals()
			s.Enumeration = true
			return s
		}(), true},
		{"recency_listing true vs false", When{RecencyListing: ptrBool(true)}, baseSignals(), false},
		{"recency_listing true vs true", When{RecencyListing: ptrBool(true)}, func() Signals {
			s := baseSignals()
			s.RecencyListing = true
			return s
		}(), true},
		{"has_file_selection true vs false", When{HasFileSelection: ptrBool(true)}, baseSignals(), false},
		{"has_file_selection false vs false", When{HasFileSelection: ptrBool(false)}, baseSignals(), true},
		{"history_turns_gte above", When{HistoryTurnsGTE: ptrInt(2)}, func() Signals {
			s := baseSignals()
			s.HistoryTurns = 5
			return s
		}(), true},
		{"history_turns_gte exact", When{HistoryTurnsGTE: ptrInt(2)}, func() Signals {
			s := baseSignals()
			s.HistoryTurns = 2
			return s
		}(), true},
		{"history_turns_gte below", When{HistoryTurnsGTE: ptrInt(2)}, func() Signals {
			s := baseSignals()
			s.HistoryTurns = 1
			return s
		}(), false},
		{"kb_ids contains", When{KBIDs: []string{"kb-0", "kb-1"}}, baseSignals(), true},
		{"kb_ids missing", When{KBIDs: []string{"kb-9"}}, baseSignals(), false},
		{"all fields agree", When{
			QueryType: []string{"lookup"}, GlobalSynthesis: ptrBool(false), Enumeration: ptrBool(false),
			RecencyListing: ptrBool(false), HasFileSelection: ptrBool(false), HistoryTurnsGTE: ptrInt(0),
			KBIDs: []string{"kb-1"},
		}, baseSignals(), true},
		{"one field of many disagrees", When{
			QueryType: []string{"lookup"}, GlobalSynthesis: ptrBool(true),
		}, baseSignals(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := OrchestratorPolicy{{When: tc.when, Orchestrator: "standard", Mode: ModeForce}}
			idx, rule, ok := p.Match(tc.sig)
			if ok != tc.want {
				t.Fatalf("Match ok = %v, want %v", ok, tc.want)
			}
			if ok {
				if idx != 0 || rule == nil || rule.Orchestrator != "standard" {
					t.Fatalf("Match = (%d, %+v)", idx, rule)
				}
			} else if rule != nil || idx != -1 {
				t.Fatalf("no-match should be (-1, nil), got (%d, %+v)", idx, rule)
			}
		})
	}
}

// TestMatch_FirstWins pins the ordering contract: rule order is the operator's
// priority statement, so a later, more specific rule must NOT win.
func TestMatch_FirstWins(t *testing.T) {
	t.Parallel()
	p := OrchestratorPolicy{
		{When: When{}, Orchestrator: "agentic", Mode: ModeForce},
		{When: When{QueryType: []string{"lookup"}}, Orchestrator: "supervisor", Mode: ModeForce},
	}
	idx, rule, ok := p.Match(baseSignals())
	if !ok || idx != 0 || rule.Orchestrator != "agentic" {
		t.Fatalf("Match = (%d, %+v, %v), want rule 0 agentic", idx, rule, ok)
	}
	// Reversed order: the specific rule now sits first and must win.
	p[0], p[1] = p[1], p[0]
	idx, rule, ok = p.Match(baseSignals())
	if !ok || idx != 0 || rule.Orchestrator != "supervisor" {
		t.Fatalf("Match = (%d, %+v, %v), want rule 0 supervisor", idx, rule, ok)
	}
}

// TestMatch_NilPolicy — the default (unset key) must never match.
func TestMatch_NilPolicy(t *testing.T) {
	t.Parallel()
	var p OrchestratorPolicy
	if idx, rule, ok := p.Match(baseSignals()); ok || rule != nil || idx != -1 {
		t.Fatalf("nil policy matched: (%d, %+v, %v)", idx, rule, ok)
	}
}

// TestDecide covers the whole force/prefer truth table, including the
// prefer-with-flag-off case that must fall through to the ladder.
func TestDecide(t *testing.T) {
	t.Parallel()
	enabled := map[string]bool{"supervisor": true, "agentic": false}

	cases := []struct {
		name         string
		policy       OrchestratorPolicy
		wantMatched  bool
		wantApplied  bool
		wantIndex    int
		wantOrch     string
		wantModeSeen string
	}{
		{
			name:        "no policy",
			policy:      nil,
			wantMatched: false, wantApplied: false, wantIndex: -1,
		},
		{
			name:        "no rule matches",
			policy:      OrchestratorPolicy{{When: When{QueryType: []string{"enumeration"}}, Orchestrator: "supervisor", Mode: ModeForce}},
			wantMatched: false, wantApplied: false, wantIndex: -1,
		},
		{
			name:        "force applies regardless of flag",
			policy:      OrchestratorPolicy{{When: When{}, Orchestrator: "agentic", Mode: ModeForce}},
			wantMatched: true, wantApplied: true, wantIndex: 0, wantOrch: "agentic", wantModeSeen: ModeForce,
		},
		{
			name:        "prefer with flag on applies",
			policy:      OrchestratorPolicy{{When: When{}, Orchestrator: "supervisor", Mode: ModePrefer}},
			wantMatched: true, wantApplied: true, wantIndex: 0, wantOrch: "supervisor", wantModeSeen: ModePrefer,
		},
		{
			name:        "prefer with flag off does not apply",
			policy:      OrchestratorPolicy{{When: When{}, Orchestrator: "agentic", Mode: ModePrefer}},
			wantMatched: true, wantApplied: false, wantIndex: 0, wantOrch: "agentic", wantModeSeen: ModePrefer,
		},
		{
			name:        "prefer with orchestrator absent from the map does not apply",
			policy:      OrchestratorPolicy{{When: When{}, Orchestrator: "drift", Mode: ModePrefer}},
			wantMatched: true, wantApplied: false, wantIndex: 0, wantOrch: "drift", wantModeSeen: ModePrefer,
		},
		{
			name:        "prefer standard always applies",
			policy:      OrchestratorPolicy{{When: When{}, Orchestrator: "standard", Mode: ModePrefer}},
			wantMatched: true, wantApplied: true, wantIndex: 0, wantOrch: "standard", wantModeSeen: ModePrefer,
		},
		{
			name: "second rule wins when the first does not match",
			policy: OrchestratorPolicy{
				{When: When{QueryType: []string{"enumeration"}}, Orchestrator: "agentic", Mode: ModeForce},
				{When: When{QueryType: []string{"lookup"}}, Orchestrator: "supervisor", Mode: ModePrefer},
			},
			wantMatched: true, wantApplied: true, wantIndex: 1, wantOrch: "supervisor", wantModeSeen: ModePrefer,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := Decide(tc.policy, baseSignals(), enabled)
			if d.Matched != tc.wantMatched || d.Applied != tc.wantApplied || d.RuleIndex != tc.wantIndex {
				t.Fatalf("Decide = %+v, want matched=%v applied=%v index=%d", d, tc.wantMatched, tc.wantApplied, tc.wantIndex)
			}
			if d.Orchestrator != tc.wantOrch || d.Mode != tc.wantModeSeen {
				t.Fatalf("Decide = %+v, want orchestrator=%q mode=%q", d, tc.wantOrch, tc.wantModeSeen)
			}
		})
	}
}

// TestDecide_NilEnabledMap — a caller that has not built the flag map yet must
// not panic; prefer simply never applies (except standard).
func TestDecide_NilEnabledMap(t *testing.T) {
	t.Parallel()
	p := OrchestratorPolicy{{When: When{}, Orchestrator: "supervisor", Mode: ModePrefer}}
	if d := Decide(p, baseSignals(), nil); !d.Matched || d.Applied {
		t.Fatalf("Decide with nil map = %+v, want matched, not applied", d)
	}
	p = OrchestratorPolicy{{When: When{}, Orchestrator: "supervisor", Mode: ModeForce}}
	if d := Decide(p, baseSignals(), nil); !d.Matched || !d.Applied {
		t.Fatalf("force with nil map = %+v, want applied", d)
	}
}

// TestEmptyPolicyRoundTrip is the W6-R10 guard at the package level: the
// document an operator gets by clearing the field parses to nil and decides
// nothing, so the ladder is untouched.
func TestEmptyPolicyRoundTrip(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "[]"} {
		p, err := ParseOrchestratorPolicy(raw)
		if err != nil {
			t.Fatalf("ParseOrchestratorPolicy(%q): %v", raw, err)
		}
		d := Decide(p, baseSignals(), map[string]bool{"supervisor": true})
		if d.Matched || d.Applied {
			t.Fatalf("empty policy %q decided %+v", raw, d)
		}
	}
}
