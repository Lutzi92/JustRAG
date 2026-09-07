package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/chatpolicy"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Call-site wiring coverage for the W6-R6 orchestrator-policy hook.
//
// SelectOrchestratorWithPolicy is unit-tested in orchestrator_select_test.go;
// what these tests pin is the GLUE in http_send.go that nothing else would
// catch: that the policy is actually read from site_config per turn, that the
// signals reach it, that the orchestrator_policy trajectory event is emitted
// with the rule index, and that the applied rule reaches the agent_decisions
// recorder (and a merely-matched one does not).
//
// Both cases force / prefer a route that resolves to the standard 2-step
// RunDeepChat, so the only model round-trips are the ones
// wiringConfigResolver already serves for the comparison-team wiring tests in
// this package — no new fakes.
// ---------------------------------------------------------------------------

func policyWiringHandler(t *testing.T, policy string, extra map[string]*string) (*Handler, *fakeDecisionRecorder) {
	t.Helper()
	values := map[string]*string{
		"chat_orchestrator_policy": strPtr(policy),
		"factcheck_in_chat":        strPtr("false"),
		"chat_compare_enabled":     strPtr("false"),
	}
	for k, v := range extra {
		values[k] = v
	}
	rec := &fakeDecisionRecorder{}
	h := &Handler{
		store:            &wiringStore{},
		aiResolver:       wiringConfigResolver(t),
		siteConfigReader: &fakeSiteConfigReader{values: values},
		decisionRecorder: rec,
		searchService: wiringSearcher{byQuery: map[string][]vector.SearchChunk{
			"Welche Themen dominieren?": {{ID: "c1", FileID: "f1", FileName: "f1.md", Content: "content"}},
		}},
	}
	return h, rec
}

func runPolicyWiringTurn(t *testing.T, h *Handler, queryType string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", nil)
	w := httptest.NewRecorder()
	body := sendMessageRequest{Message: "Welche Themen dominieren?"}
	tp := h.resolveTurnPolicy(wiringCtx("u1"), "kb1", queryType, "Welche Themen dominieren?", "de", body, nil)
	handled := h.tryDeepChat(wiringCtx("u1"), w, r, "chat1", "kb1", "de", "",
		"Welche Themen dominieren?", "", queryType, "", "",
		body, turnAnchor{}, GraphTraversalDecision{}, nil, nil, nil, nil, "", tp)
	if !handled {
		t.Fatalf("tryDeepChat did not handle the turn; body: %s", w.Body.String())
	}
	return w.Body.String()
}

// A "force standard" rule must win over an ENABLED supervisor: without the
// hook the ladder picks supervisor for this complex_reasoning turn, so the
// mode recorded on the agent_decisions row is what proves the policy steered
// the dispatch — not just that an event was written.
func TestTryDeepChat_PolicyForceOverridesLadderAndRecordsRule(t *testing.T) {
	h, rec := policyWiringHandler(t,
		`[{"when":{"query_type":["complex_reasoning"]},"orchestrator":"standard","mode":"force"}]`,
		map[string]*string{"chat_supervisor_enabled": strPtr("true")})

	out := runPolicyWiringTurn(t, h, vector.QueryTypeComplexReasoning)

	for _, want := range []string{`"stage":"orchestrator_policy"`, `"decision":"standard"`, `"mode":"force"`, `"policy_rule":0`} {
		if !strings.Contains(out, want) {
			t.Errorf("SSE stream is missing %q:\n%s", want, out)
		}
	}

	snap := waitForRecord(t, rec)
	if snap.mode != "standard" {
		t.Errorf("agent_decisions mode = %q, want %q (the forced route, not the ladder's supervisor)", snap.mode, "standard")
	}
	if snap.policyRule == nil || *snap.policyRule != 0 {
		t.Errorf("agent_decisions policy_rule = %v, want 0", snap.policyRule)
	}
}

// A "prefer" rule whose orchestrator is disabled must leave the ladder in
// charge: the event says so ("fallthrough"), and the agent_decisions row
// carries NO rule — attributing the row to a rule that did not decide would
// make the policy's measured effect a fiction.
func TestTryDeepChat_PolicyPreferFallthroughEmitsEventButRecordsNoRule(t *testing.T) {
	h, rec := policyWiringHandler(t,
		`[{"when":{},"orchestrator":"drift","mode":"prefer"}]`, nil)

	out := runPolicyWiringTurn(t, h, vector.QueryTypeComplexReasoning)

	for _, want := range []string{`"stage":"orchestrator_policy"`, `"decision":"fallthrough"`, `"mode":"prefer"`, `"policy_rule":0`} {
		if !strings.Contains(out, want) {
			t.Errorf("SSE stream is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "drift is disabled") {
		t.Errorf("SSE stream does not say why the prefer rule fell through:\n%s", out)
	}

	snap := waitForRecord(t, rec)
	if snap.policyRule != nil {
		t.Errorf("agent_decisions policy_rule = %v, want nil for a matched-but-unapplied rule", snap.policyRule)
	}
}

// No policy: no event, no rule on the row. This is the default deployment, and
// it is what makes the two assertions above meaningful — without it they could
// both pass on a build that emits the event unconditionally.
func TestTryDeepChat_NoPolicyEmitsNoEvent(t *testing.T) {
	h, rec := policyWiringHandler(t, "", nil)

	out := runPolicyWiringTurn(t, h, vector.QueryTypeComplexReasoning)

	if strings.Contains(out, "orchestrator_policy") {
		t.Errorf("orchestrator_policy event emitted without a policy:\n%s", out)
	}
	snap := waitForRecord(t, rec)
	if snap.policyRule != nil {
		t.Errorf("agent_decisions policy_rule = %v, want nil", snap.policyRule)
	}
}

// ---------------------------------------------------------------------------
// S1 (Wave-6 fix round 1): the entry gate
//
// Before the fix the policy was read INSIDE tryDeepChat, which SendMessage
// only enters for complex_reasoning (or comparison / team) turns — so a
// `when.query_type: ["lookup"]` rule, the very shape W6-R6's schema exists to
// express, was a no-op in production. These tests pin both halves: the gate
// widens for a forced non-standard route, and it widens for NOTHING else.
// ---------------------------------------------------------------------------

// forcedGate builds the turnPolicy a resolved rule produces, without going
// through site_config — the resolver itself is covered by
// TestResolveTurnPolicy below.
func forcedGate(orch, mode string, applied bool) turnPolicy {
	return turnPolicy{
		policy: chatpolicy.OrchestratorPolicy{{Orchestrator: orch, Mode: mode}},
		gate: chatpolicy.Decision{
			RuleIndex: 0, Orchestrator: orch, Mode: mode, Matched: true, Applied: applied,
		},
	}
}

func TestShouldTryDeepChat(t *testing.T) {
	noPolicy := turnPolicy{gate: chatpolicy.Decision{RuleIndex: -1}}

	tests := []struct {
		name                                    string
		isComplex, runCompare, team, streamMode bool
		tp                                      turnPolicy
		want                                    bool
	}{
		// The pre-W6-R6 gate, unchanged. These four rows are what "byte-identical
		// with no policy" means at this level.
		{name: "no policy, lookup, streaming", streamMode: true, tp: noPolicy, want: false},
		{name: "no policy, complex, streaming", isComplex: true, streamMode: true, tp: noPolicy, want: true},
		{name: "no policy, comparison, streaming", runCompare: true, streamMode: true, tp: noPolicy, want: true},
		{name: "no policy, team, streaming", team: true, streamMode: true, tp: noPolicy, want: true},
		{name: "no policy, complex, NOT streaming", isComplex: true, tp: noPolicy, want: false},

		// S1: a forced non-standard route opens the dispatch for a turn the
		// classifier would never have sent there.
		{name: "force supervisor on a lookup turn", streamMode: true, tp: forcedGate("supervisor", chatpolicy.ModeForce, true), want: true},
		{name: "prefer agentic (flag on) on a lookup turn", streamMode: true, tp: forcedGate("agentic", chatpolicy.ModePrefer, true), want: true},

		// ... and nothing else does.
		{name: "prefer supervisor with the flag OFF stays standard", streamMode: true, tp: forcedGate("supervisor", chatpolicy.ModePrefer, false), want: false},
		{name: "force standard does not widen the gate", streamMode: true, tp: forcedGate(chatpolicy.OrchestratorStandard, chatpolicy.ModeForce, true), want: false},
		{name: "force supervisor on a NON-streaming turn", tp: forcedGate("supervisor", chatpolicy.ModeForce, true), want: false},
		{name: "force standard on a complex turn still enters (isComplex does)", isComplex: true, streamMode: true, tp: forcedGate(chatpolicy.OrchestratorStandard, chatpolicy.ModeForce, true), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldTryDeepChat(tt.isComplex, tt.runCompare, tt.team, tt.streamMode, tt.tp); got != tt.want {
				t.Fatalf("shouldTryDeepChat = %v, want %v", got, tt.want)
			}
		})
	}
}

// standardPathPolicyRule records a forced-"standard" rule on the standard path
// — the only place that rule index can land — and stays nil on the
// orchestrator-error fall-through, where the forced route did not answer.
func TestStandardPathPolicyRule(t *testing.T) {
	forcedStandard := forcedGate(chatpolicy.OrchestratorStandard, chatpolicy.ModeForce, true)
	got := standardPathPolicyRule(forcedStandard, false)
	if got == nil || *got != 0 {
		t.Fatalf("forced standard, deep chat not attempted: got %v, want 0", got)
	}
	if got := standardPathPolicyRule(forcedStandard, true); got != nil {
		t.Fatalf("forced standard after a deep-chat attempt: got %v, want nil (the route that answered is not this row's rule)", got)
	}
	if got := standardPathPolicyRule(forcedGate("supervisor", chatpolicy.ModeForce, true), false); got != nil {
		t.Fatalf("forced supervisor on the standard path: got %v, want nil", got)
	}
	if got := standardPathPolicyRule(turnPolicy{gate: chatpolicy.Decision{RuleIndex: -1}}, false); got != nil {
		t.Fatalf("no policy: got %v, want nil", got)
	}
	// A matched-but-unapplied prefer rule left the ladder in charge.
	if got := standardPathPolicyRule(forcedGate(chatpolicy.OrchestratorStandard, chatpolicy.ModePrefer, false), false); got != nil {
		t.Fatalf("unapplied rule: got %v, want nil", got)
	}
}

// resolveTurnPolicy reads the document once and builds the signals only when
// there is a policy to test them against.
func TestResolveTurnPolicy(t *testing.T) {
	newHandler := func(policy string, extra map[string]*string) *Handler {
		values := map[string]*string{"chat_orchestrator_policy": strPtr(policy)}
		for k, v := range extra {
			values[k] = v
		}
		return &Handler{siteConfigReader: &fakeSiteConfigReader{values: values}}
	}
	ctx := context.Background()
	body := sendMessageRequest{Message: "q", SelectedFileIDs: []string{"f1"}}

	// No policy: no signals, no decision, and the gate stays shut.
	tp := newHandler("", nil).resolveTurnPolicy(ctx, "kb1", vector.QueryTypeLookup, "Welche neuen Meldungen gibt es?", "de", body, nil)
	if len(tp.policy) != 0 || tp.gate.Matched || tp.gate.RuleIndex != -1 || tp.opensDeepChatDispatch() {
		t.Fatalf("empty policy: %+v", tp)
	}
	if tp.signals != (chatpolicy.Signals{}) {
		t.Fatalf("empty policy built a signal bag: %+v (the two regex classifiers must not run)", tp.signals)
	}

	// A policy: every signal resolved, and the force decision made.
	h := newHandler(`[{"when":{"query_type":["lookup"],"recency_listing":true,"has_file_selection":true},"orchestrator":"supervisor","mode":"force"}]`, nil)
	tp = h.resolveTurnPolicy(ctx, "kb1", vector.QueryTypeLookup, "Welche neuen Meldungen gibt es?", "de", body, nil)
	if !tp.gate.Applied || tp.gate.Orchestrator != "supervisor" || tp.gate.RuleIndex != 0 {
		t.Fatalf("decision = %+v, want an applied supervisor rule 0", tp.gate)
	}
	if !tp.signals.RecencyListing || !tp.signals.HasFileSelection || tp.signals.KBID != "kb1" {
		t.Fatalf("signals = %+v", tp.signals)
	}
	if !tp.opensDeepChatDispatch() {
		t.Fatal("opensDeepChatDispatch = false for a forced supervisor rule")
	}

	// prefer + flag off: matched, not applied, gate stays shut.
	h = newHandler(`[{"when":{},"orchestrator":"drift","mode":"prefer"}]`, nil)
	tp = h.resolveTurnPolicy(ctx, "kb1", vector.QueryTypeLookup, "q", "de", body, nil)
	if !tp.gate.Matched || tp.gate.Applied || tp.opensDeepChatDispatch() {
		t.Fatalf("prefer with the flag off: %+v", tp.gate)
	}
	// ... and applied once the flag is on, which is what proves
	// policyEnabledFromConfig is actually consulted.
	h = newHandler(`[{"when":{},"orchestrator":"drift","mode":"prefer"}]`, map[string]*string{"chat_drift_enabled": strPtr("true")})
	tp = h.resolveTurnPolicy(ctx, "kb1", vector.QueryTypeLookup, "q", "de", body, nil)
	if !tp.gate.Applied || !tp.opensDeepChatDispatch() {
		t.Fatalf("prefer with the flag on: %+v", tp.gate)
	}
}

// policyEnabledFromConfig (reader-sourced, used by the entry gate) and
// OrchestratorInputs.policyEnabled (inputs-sourced, used by the selector) must
// produce the same map, or the gate could open for a turn the selector then
// refuses — or, worse, stay shut for one it would have taken.
func TestPolicyEnabledMapsAgree(t *testing.T) {
	for _, on := range []bool{false, true} {
		v := "false"
		if on {
			v = "true"
		}
		reader := &fakeSiteConfigReader{values: map[string]*string{
			"chat_drift_enabled":        strPtr(v),
			"chat_longcontext_enabled":  strPtr(v),
			"chat_supervisor_enabled":   strPtr(v),
			"chat_plan_execute_enabled": strPtr(v),
			"chat_agentic_enabled":      strPtr(v),
		}}
		fromConfig := policyEnabledFromConfig(context.Background(), reader)
		fromInputs := OrchestratorInputs{
			DriftEnabled: on, LongContextEnabled: on, SupervisorEnabled: on,
			PlanExecuteEnabled: on, AgenticEnabled: on,
		}.policyEnabled()
		if len(fromConfig) != len(fromInputs) {
			t.Fatalf("map sizes differ: %v vs %v", fromConfig, fromInputs)
		}
		for k, want := range fromInputs {
			if fromConfig[k] != want {
				t.Errorf("flags=%v: policyEnabledFromConfig[%q] = %v, want %v", on, k, fromConfig[k], want)
			}
		}
		// Every name the policy may carry except "standard" must be present.
		for _, name := range chatpolicy.Orchestrators {
			if name == chatpolicy.OrchestratorStandard {
				continue
			}
			if _, ok := fromConfig[name]; !ok {
				t.Errorf("policyEnabledFromConfig is missing %q", name)
			}
		}
	}
}

// End to end through tryDeepChat: a LOOKUP turn with a force rule really runs
// the supervisor and records mode="supervisor" with the rule index. This is
// the case S1 was about — before the fix this turn never reached tryDeepChat
// at all, and the ladder alone would answer it on the standard path.
func TestTryDeepChat_PolicyForceSupervisorOnLookupTurn(t *testing.T) {
	h, rec := policyWiringHandler(t,
		`[{"when":{"query_type":["lookup"]},"orchestrator":"supervisor","mode":"force"}]`, nil)

	out := runPolicyWiringTurn(t, h, vector.QueryTypeLookup)

	if !strings.Contains(out, `"decision":"supervisor"`) || !strings.Contains(out, `"policy_rule":0`) {
		t.Errorf("SSE stream does not report the forced supervisor route:\n%s", out)
	}
	snap := waitForRecord(t, rec)
	if snap.mode != "supervisor" {
		t.Errorf("agent_decisions mode = %q, want %q — a lookup turn must be routable by a rule", snap.mode, "supervisor")
	}
	if snap.policyRule == nil || *snap.policyRule != 0 {
		t.Errorf("agent_decisions policy_rule = %v, want 0", snap.policyRule)
	}
}

// The mirror image: a prefer rule whose orchestrator is disabled leaves a
// lookup turn on the standard path, and the gate never opens for it.
func TestPolicyPreferWithFlagOffLeavesLookupOnStandardPath(t *testing.T) {
	h := &Handler{siteConfigReader: &fakeSiteConfigReader{values: map[string]*string{
		"chat_orchestrator_policy": strPtr(`[{"when":{"query_type":["lookup"]},"orchestrator":"supervisor","mode":"prefer"}]`),
	}}}
	tp := h.resolveTurnPolicy(context.Background(), "kb1", vector.QueryTypeLookup, "q", "de", sendMessageRequest{}, nil)

	if shouldTryDeepChat(false, false, false, true, tp) {
		t.Fatal("a prefer rule with its flag off opened the deep-chat gate for a lookup turn")
	}
	if standardPathPolicyRule(tp, false) != nil {
		t.Fatal("an unapplied rule was recorded on the standard path")
	}
}
