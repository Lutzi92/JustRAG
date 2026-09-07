package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func runPolicyWiringTurn(t *testing.T, h *Handler) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb1/chat", nil)
	w := httptest.NewRecorder()
	body := sendMessageRequest{Message: "Welche Themen dominieren?"}
	handled := h.tryDeepChat(wiringCtx("u1"), w, r, "chat1", "kb1", "de", "",
		"Welche Themen dominieren?", "", vector.QueryTypeComplexReasoning, "", "",
		body, turnAnchor{}, GraphTraversalDecision{}, nil, nil, nil, nil, "")
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

	out := runPolicyWiringTurn(t, h)

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

	out := runPolicyWiringTurn(t, h)

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

	out := runPolicyWiringTurn(t, h)

	if strings.Contains(out, "orchestrator_policy") {
		t.Errorf("orchestrator_policy event emitted without a policy:\n%s", out)
	}
	snap := waitForRecord(t, rec)
	if snap.policyRule != nil {
		t.Errorf("agent_decisions policy_rule = %v, want nil", snap.policyRule)
	}
}
