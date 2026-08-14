package pipeline

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
)

// fakeReader is an in-memory siteconfig.BatchReader.
type fakeReader struct{ vals map[string]string }

func (f fakeReader) GetSiteConfigValue(ctx context.Context, key string) (*string, error) {
	if v, ok := f.vals[key]; ok {
		return &v, nil
	}
	return nil, nil
}

func (f fakeReader) GetSiteConfigValues(ctx context.Context, keys []string) (map[string]*string, error) {
	out := make(map[string]*string, len(keys))
	for _, k := range keys {
		if v, ok := f.vals[k]; ok {
			vv := v
			out[k] = &vv
		} else {
			out[k] = nil
		}
	}
	return out, nil
}

func nodeByIDIn(g *ProjectedGraph, id NodeID) *ProjectedNode {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

func TestProjectMarksDisabledNodeInactive(t *testing.T) {
	// factcheck_in_chat, not chat_factuality_verifier_enabled: it is the real
	// master toggle for the default-path factchecker and NodeFactuality's
	// only key. The claim-level verifier is a narrower, separately-gated
	// escalation and lives in its own node (NodeFactVerifier).
	r := fakeReader{vals: map[string]string{"factcheck_in_chat": "false"}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeFactuality)
	if n == nil {
		t.Fatal("factuality node missing from projection")
	}
	if n.Activation != ActivationInactive {
		t.Fatalf("Activation = %q, want %q", n.Activation, ActivationInactive)
	}
	if n.Reason != "flag_off" {
		t.Fatalf("Reason = %q, want %q", n.Reason, "flag_off")
	}
}

func TestProjectMarksEnabledNodeActive(t *testing.T) {
	r := fakeReader{vals: map[string]string{"factcheck_in_chat": "true"}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	if n := nodeByIDIn(g, NodeFactuality); n.Activation != ActivationActive {
		t.Fatalf("Activation = %q, want %q", n.Activation, ActivationActive)
	}
}

// Nodes with no keys are unconditional stages and must always be active.
func TestProjectKeylessNodesAreAlwaysActive(t *testing.T) {
	g, err := Project(context.Background(), fakeReader{vals: map[string]string{}}, fakeReader{vals: map[string]string{}}, LaneLookup)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	for _, id := range []NodeID{NodeRetrieve, NodeRerank, NodeAnswer} {
		if n := nodeByIDIn(g, id); n.Activation != ActivationActive {
			t.Errorf("node %q Activation = %q, want %q", id, n.Activation, ActivationActive)
		}
	}
}

// On a lookup lane none of the complex-reasoning orchestrators can win, so the
// only candidate is standard.
func TestProjectLookupLaneYieldsStandardOnly(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_supervisor_enabled": "true",
		"chat_drift_enabled":      "true",
	}}

	g, err := Project(context.Background(), r, r, LaneLookup)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	if len(g.Orchestrators) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(g.Orchestrators), g.Orchestrators)
	}
	if g.Orchestrators[0].Orchestrator != chat.OrchStandard {
		t.Fatalf("candidate = %q, want %q", g.Orchestrators[0].Orchestrator, chat.OrchStandard)
	}
}

// With drift on, a complex lane has TWO candidates: drift conditionally (for
// global-synthesis queries) and supervisor otherwise.
func TestProjectComplexLaneListsConditionalDrift(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_drift_enabled":      "true",
		"chat_supervisor_enabled": "true",
	}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	var drift, supervisor *OrchestratorCandidate
	for i := range g.Orchestrators {
		switch g.Orchestrators[i].Orchestrator {
		case chat.OrchDrift:
			drift = &g.Orchestrators[i]
		case chat.OrchSupervisor:
			supervisor = &g.Orchestrators[i]
		}
	}

	if drift == nil {
		t.Fatal("drift missing from candidates")
	}
	if drift.Activation != ActivationConditional {
		t.Errorf("drift Activation = %q, want %q", drift.Activation, ActivationConditional)
	}
	if drift.Condition == "" {
		t.Error("drift candidate has no human-readable Condition")
	}
	if supervisor == nil {
		t.Fatal("supervisor missing from candidates")
	}
	if supervisor.Activation != ActivationActive {
		t.Errorf("supervisor Activation = %q, want %q", supervisor.Activation, ActivationActive)
	}
}

// The cost estimate must count only nodes that actually run.
func TestProjectCostEstimateExcludesInactiveNodes(t *testing.T) {
	off := fakeReader{vals: map[string]string{"factcheck_in_chat": "false"}}
	on := fakeReader{vals: map[string]string{"factcheck_in_chat": "true"}}

	gOff, err := Project(context.Background(), off, off, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	gOn, err := Project(context.Background(), on, on, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	if gOn.EstLLMCalls <= gOff.EstLLMCalls {
		t.Fatalf("enabling the factchecker did not raise EstLLMCalls (%d vs %d)",
			gOn.EstLLMCalls, gOff.EstLLMCalls)
	}
}

// Registry membership decides whether the UI may offer an editor.
func TestProjectMarksEditability(t *testing.T) {
	g, err := Project(context.Background(), fakeReader{vals: map[string]string{}}, fakeReader{vals: map[string]string{}}, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	// crag_enabled IS in the per-KB registry today.
	if n := nodeByIDIn(g, NodeCRAGGrade); !n.Editable {
		t.Error("crag_grade should be editable (crag_enabled is in the per-KB registry)")
	}
}

// Adaptive routing skips CRAG on lookup and enumeration lanes even when
// crag_enabled is true — the single most surprising behaviour in the
// pipeline, per the code comment beside the branch in project.go.
func TestProjectCRAGLaneSkippedOnLookupAndEnumeration(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"crag_enabled":             "true",
		"adaptive_routing_enabled": "true",
	}}

	for _, lane := range []Lane{LaneLookup, LaneEnumeration} {
		g, err := Project(context.Background(), r, r, lane)
		if err != nil {
			t.Fatalf("Project(%s): %v", lane, err)
		}
		for _, id := range []NodeID{NodeCRAGGrade, NodeCRAGRewrite} {
			n := nodeByIDIn(g, id)
			if n == nil {
				t.Fatalf("lane %s: node %q missing from projection", lane, id)
			}
			if n.Activation != ActivationInactive {
				t.Errorf("lane %s: node %q Activation = %q, want %q", lane, id, n.Activation, ActivationInactive)
			}
			if n.Reason != "lane_skipped" {
				t.Errorf("lane %s: node %q Reason = %q, want %q", lane, id, n.Reason, "lane_skipped")
			}
		}
	}
}

// On the complex-reasoning lane the adaptive-routing skip must NOT fire — that
// branch belongs to lookup/enumeration only. The complex lane has its own,
// disjoint reason for CRAG not running plainly (the orchestrator bypass), so
// the assertion here is on the Reason: seeing "lane_skipped" on complex would
// mean the adaptive-routing lane check had been inverted.
func TestProjectCRAGNotLaneSkippedOnComplexLaneUnderAdaptiveRouting(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"crag_enabled":             "true",
		"adaptive_routing_enabled": "true",
	}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	for _, id := range []NodeID{NodeCRAGGrade, NodeCRAGRewrite} {
		n := nodeByIDIn(g, id)
		if n.Reason == "lane_skipped" {
			t.Errorf("node %q: adaptive routing skipped CRAG on the complex lane, "+
				"where it does not apply", id)
		}
		if n.Activation != ActivationConditional || n.Reason != "orchestrator_bypass" {
			t.Errorf("node %q: Activation/Reason = %q/%q, want %q/%q",
				id, n.Activation, n.Reason, ActivationConditional, "orchestrator_bypass")
		}
	}
}

// Without adaptive routing turned on, CRAG runs on every lane, including
// lookup — catches the lane-skip branch firing unconditionally.
func TestProjectCRAGActiveOnLookupWhenAdaptiveRoutingOff(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"crag_enabled":             "true",
		"adaptive_routing_enabled": "false",
	}}

	g, err := Project(context.Background(), r, r, LaneLookup)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	for _, id := range []NodeID{NodeCRAGGrade, NodeCRAGRewrite} {
		if n := nodeByIDIn(g, id); n.Activation != ActivationActive {
			t.Errorf("node %q Activation = %q, want %q", id, n.Activation, ActivationActive)
		}
	}
}

// Self-RAG supersedes the CLAIM-LEVEL VERIFIER (ai.VerifyFactuality), which is
// the only thing internal/chat/post_response.go:301 swaps out. It does not
// supersede the factchecker behind factcheck_in_chat: that runs in its own
// goroutine (post_response.go:134-140) whatever Self-RAG is doing.
//
// The previous version of this test asserted the opposite for NodeFactuality
// and so enshrined a diagram that told operators "Faktencheck — wird von
// Self-RAG ersetzt" while ai.CheckFacts fired an LLM call on every turn.
func TestProjectVerifierSupersededBySelfRAG(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"factcheck_in_chat":                "true",
		"chat_factuality_verifier_enabled": "true",
		"chat_self_rag_enabled":            "true",
	}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	v := nodeByIDIn(g, NodeFactVerifier)
	if v == nil {
		t.Fatal("factuality_verifier node missing from projection")
	}
	if v.Activation != ActivationInactive {
		t.Fatalf("verifier Activation = %q, want %q", v.Activation, ActivationInactive)
	}
	if v.Reason != "superseded_by:self_rag" {
		t.Fatalf("verifier Reason = %q, want %q", v.Reason, "superseded_by:self_rag")
	}
}

// The regression guard for the bug above: the factchecker keeps running.
func TestProjectFactcheckSurvivesSelfRAG(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"factcheck_in_chat":     "true",
		"chat_self_rag_enabled": "true",
	}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeFactuality)
	if n == nil {
		t.Fatal("factuality node missing from projection")
	}
	if n.Activation != ActivationActive {
		t.Fatalf("Activation = %q, want %q — ai.CheckFacts runs regardless of Self-RAG",
			n.Activation, ActivationActive)
	}
	if n.Reason != "" {
		t.Fatalf("Reason = %q, want empty", n.Reason)
	}
}

func TestProjectRejectsUnknownLane(t *testing.T) {
	if _, err := Project(context.Background(), fakeReader{vals: map[string]string{}}, fakeReader{vals: map[string]string{}}, Lane("nonsense")); err == nil {
		t.Fatal("Project accepted an unknown lane")
	}
}

func TestProjectReportsValueOrigins(t *testing.T) {
	global := fakeReader{vals: map[string]string{"crag_enabled": "false"}}
	overlaid := fakeReader{vals: map[string]string{"crag_enabled": "true"}}

	g, err := Project(context.Background(), overlaid, global, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeCRAGGrade)
	if got := n.Origins["crag_enabled"]; got != "kb" {
		t.Fatalf("Origins[crag_enabled] = %q, want %q", got, "kb")
	}
}

func TestProjectReportsGlobalOrigin(t *testing.T) {
	global := fakeReader{vals: map[string]string{"crag_enabled": "true"}}

	g, err := Project(context.Background(), global, global, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeCRAGGrade)
	if got := n.Origins["crag_enabled"]; got != "global" {
		t.Fatalf("Origins[crag_enabled] = %q, want %q", got, "global")
	}
}

func TestProjectReportsDefaultOrigin(t *testing.T) {
	empty := fakeReader{vals: map[string]string{}}

	g, err := Project(context.Background(), empty, empty, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeCRAGGrade)
	if got := n.Origins["crag_enabled"]; got != "default" {
		t.Fatalf("Origins[crag_enabled] = %q, want %q", got, "default")
	}
}

// ---------------------------------------------------------------------------
// Orchestrator bypass on the complex lane (the endpoint's DEFAULT lane)
// ---------------------------------------------------------------------------

// Every stage that lives only inside chat.PrepareChatContext must NOT project
// as plainly active on the complex lane: SendMessage routes every streaming
// complex_reasoning turn into tryDeepChat, and no branch of that switch calls
// PrepareChatContext.
func TestProjectPrepareChatContextStagesAreConditionalOnComplexLane(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"crag_enabled":                     "true",
		"adaptive_routing_enabled":         "false",
		"step_back_enabled":                "true",
		"query_decompose_enabled":          "true",
		"chat_context_compression_enabled": "true",
		"chat_sufficient_context_enabled":  "true",
	}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	for id := range prepareChatContextOwned {
		n := nodeByIDIn(g, id)
		if n == nil {
			t.Fatalf("node %q missing from projection", id)
		}
		if n.Activation == ActivationActive {
			t.Errorf("node %q projects active on the complex lane, but the "+
				"orchestrator bypasses PrepareChatContext there", id)
		}
		if n.Reason != "orchestrator_bypass" {
			t.Errorf("node %q Reason = %q, want %q", id, n.Reason, "orchestrator_bypass")
		}
		if n.Condition == "" {
			t.Errorf("node %q has no German Condition explaining the bypass", id)
		}
	}
}

// Negative control for the rule above: on the lookup lane PrepareChatContext
// really does run, so these stages must still project ACTIVE. Step-back and
// decompose are excluded — they are complex-reasoning-only inside
// PrepareChatContext itself and get their own assertion below.
func TestProjectPrepareChatContextStagesActiveOnLookupLane(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"crag_enabled":                     "true",
		"adaptive_routing_enabled":         "false",
		"chat_context_compression_enabled": "true",
		"chat_sufficient_context_enabled":  "true",
	}}

	g, err := Project(context.Background(), r, r, LaneLookup)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	for _, id := range []NodeID{NodeCRAGGrade, NodeCRAGRewrite, NodeCompression, NodeSufficientCtx} {
		n := nodeByIDIn(g, id)
		if n == nil {
			t.Fatalf("node %q missing from projection", id)
		}
		if n.Activation != ActivationActive {
			t.Errorf("node %q Activation = %q, want %q (lookup reaches PrepareChatContext)",
				id, n.Activation, ActivationActive)
		}
		if n.Reason != "" {
			t.Errorf("node %q Reason = %q, want empty", id, n.Reason)
		}
	}
}

// The supervisor carries the sufficient-context gate itself, so on the complex
// lane it is the one PrepareChatContext-owned stage that really does run.
func TestProjectSufficientContextActiveUnderSupervisor(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_supervisor_enabled":         "true",
		"chat_sufficient_context_enabled": "true",
	}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeSufficientCtx)
	if n.Activation != ActivationActive {
		t.Fatalf("Activation = %q, want %q — RunSupervisorChat calls "+
			"ai.JudgeContextSufficiency itself", n.Activation, ActivationActive)
	}

	// …and with any other orchestrator winning the lane, it does not.
	r2 := fakeReader{vals: map[string]string{
		"chat_agentic_enabled":            "true",
		"chat_sufficient_context_enabled": "true",
	}}
	g2, err := Project(context.Background(), r2, r2, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if n2 := nodeByIDIn(g2, NodeSufficientCtx); n2.Activation != ActivationConditional {
		t.Fatalf("agentic lane: Activation = %q, want %q", n2.Activation, ActivationConditional)
	}
}

// A stage whose own flag is off stays "flag_off" on every lane — the bypass
// rule must only ever downgrade an otherwise-active node.
func TestProjectBypassDoesNotMaskFlagOff(t *testing.T) {
	empty := fakeReader{vals: map[string]string{}}

	g, err := Project(context.Background(), empty, empty, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	for id := range prepareChatContextOwned {
		n := nodeByIDIn(g, id)
		if n.Activation != ActivationInactive || n.Reason != "flag_off" {
			t.Errorf("node %q Activation/Reason = %q/%q, want %q/%q",
				id, n.Activation, n.Reason, ActivationInactive, "flag_off")
		}
	}
}

// Step-back and decompose refuse anything but a complex_reasoning query inside
// PrepareChatContext, so on lookup/enumeration they are lane-skipped even
// though the standard path runs there.
func TestProjectComplexOnlyStagesLaneSkippedOnLookup(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"step_back_enabled":       "true",
		"query_decompose_enabled": "true",
	}}

	for _, lane := range []Lane{LaneLookup, LaneEnumeration} {
		g, err := Project(context.Background(), r, r, lane)
		if err != nil {
			t.Fatalf("Project(%s): %v", lane, err)
		}
		for id := range complexReasoningOnly {
			n := nodeByIDIn(g, id)
			if n.Activation != ActivationInactive {
				t.Errorf("lane %s: node %q Activation = %q, want %q",
					lane, id, n.Activation, ActivationInactive)
			}
			if n.Reason != "lane_skipped" {
				t.Errorf("lane %s: node %q Reason = %q, want %q",
					lane, id, n.Reason, "lane_skipped")
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Self-RAG / verifier launch gate
// ---------------------------------------------------------------------------

// Self-RAG's goroutine starts only when the citation validator or the legacy
// verifier is on. With both off it can never fire, no matter what
// chat_self_rag_enabled says.
func TestProjectSelfRAGInactiveWithoutCitationValidation(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_self_rag_enabled":       "true",
		"citation_validation_enabled": "false",
	}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeSelfRAG)
	if n.Activation != ActivationInactive {
		t.Fatalf("Activation = %q, want %q", n.Activation, ActivationInactive)
	}
	if n.Reason != "requires:citation_validation" {
		t.Fatalf("Reason = %q, want %q", n.Reason, "requires:citation_validation")
	}
	if n.Condition == "" {
		t.Error("no German Condition telling the operator what to switch on")
	}
}

// always_run alone does not help: without the validator (and without the
// legacy verifier) the goroutine that would call Self-RAG never starts.
func TestProjectSelfRAGInactiveWithAlwaysRunButNoLaunch(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_self_rag_enabled":               "true",
		"citation_validation_enabled":         "false",
		"chat_factuality_verifier_always_run": "true",
	}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	if n := nodeByIDIn(g, NodeSelfRAG); n.Activation != ActivationInactive {
		t.Fatalf("Activation = %q, want %q", n.Activation, ActivationInactive)
	}
}

// With the validator on but no always-run override, Self-RAG only fires when
// the validator raised a suspect — conditional, with an explanation.
func TestProjectSelfRAGConditionalBehindCitationValidator(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_self_rag_enabled":       "true",
		"citation_validation_enabled": "true",
	}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeSelfRAG)
	if n.Activation != ActivationConditional {
		t.Fatalf("Activation = %q, want %q", n.Activation, ActivationConditional)
	}
	if n.Condition == "" {
		t.Error("conditional Self-RAG node carries no German Condition")
	}
}

// always_run switches the cost gate off: Self-RAG then runs on every turn.
func TestProjectSelfRAGActiveWithAlwaysRun(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_self_rag_enabled":               "true",
		"citation_validation_enabled":         "true",
		"chat_factuality_verifier_always_run": "true",
	}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	if n := nodeByIDIn(g, NodeSelfRAG); n.Activation != ActivationActive {
		t.Fatalf("Activation = %q, want %q", n.Activation, ActivationActive)
	}
}

// The claim-level verifier sits behind the same gate as Self-RAG.
func TestProjectVerifierConditionalBehindCitationValidator(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_factuality_verifier_enabled": "true",
		"citation_validation_enabled":      "true",
	}}

	g, err := Project(context.Background(), r, r, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeFactVerifier)
	if n.Activation != ActivationConditional {
		t.Fatalf("Activation = %q, want %q", n.Activation, ActivationConditional)
	}

	// Validator off, always_run off: the verifier's own flag starts the
	// goroutine but no suspect can ever appear, so it cannot fire.
	r2 := fakeReader{vals: map[string]string{
		"chat_factuality_verifier_enabled": "true",
		"citation_validation_enabled":      "false",
	}}
	g2, err := Project(context.Background(), r2, r2, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	n2 := nodeByIDIn(g2, NodeFactVerifier)
	if n2.Activation != ActivationInactive || n2.Reason != "requires:citation_validation" {
		t.Fatalf("Activation/Reason = %q/%q, want %q/%q",
			n2.Activation, n2.Reason, ActivationInactive, "requires:citation_validation")
	}
}

// ---------------------------------------------------------------------------
// End-to-end proof that Phase 2 achieved its purpose
// ---------------------------------------------------------------------------

// TestProjectPhase2NodesAreEditable proves the whole point of Phase 2: nodes
// the canvas draws that used to project editable:false — because their
// Keys[0] activation gate was missing from the per-KB registry — now project
// editable:true.
//
// The expected node list below is HARDCODED, not derived from the registry.
// Computing the expectation by re-reading siteconfig.IsPerKB would make this
// test tautological: it would pass no matter what the registry contains,
// including a registry edit that silently undid this phase, because it would
// only ever be comparing the registry to itself. This list is the
// specification Phase 2 was built against; kbConfigRegistry in
// internal/siteconfig/registry.go is what is being tested against it.
func TestProjectPhase2NodesAreEditable(t *testing.T) {
	mustBeEditable := []struct {
		id  NodeID
		key string // Keys[0] — documented here so a failure names the exact registry row to check
	}{
		// Verification / correction cluster (Task 2).
		{NodeFactuality, "factcheck_in_chat"},
		{NodeFactVerifier, "chat_factuality_verifier_enabled"},
		{NodeRefine, "chat_factuality_gate_enabled"},
		{NodeSelfRAG, "chat_self_rag_enabled"},
		{NodeCitationCheck, "citation_validation_enabled"},
		{NodeSufficientCtx, "chat_sufficient_context_enabled"},
		// Retrieval + orchestrator drift fixes (Task 3). Verified against git
		// history (commit f40e634, "register the drifted retrieval and
		// orchestrator keys"): both keys below appear as new `+` rows in that
		// commit's diff of internal/siteconfig/registry.go, and both are the
		// Keys[0] of their node — unlike chat_graph_routing_enabled or
		// rerank_blend_alpha, which were already registered before that
		// commit and so are not Task 3 evidence.
		{NodeRecencyBoost, "recency_boost_enabled"},
		{NodeFeedbackBoost, "chat_feedback_boost_enabled"},
	}

	empty := fakeReader{vals: map[string]string{}}
	g, err := Project(context.Background(), empty, empty, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	for _, want := range mustBeEditable {
		n := nodeByIDIn(g, want.id)
		if n == nil {
			t.Fatalf("node %q missing from projection", want.id)
		}
		if len(n.Keys) == 0 || n.Keys[0] != want.key {
			t.Fatalf("node %q Keys[0] = %v, want first key %q — test's key expectation is stale",
				want.id, n.Keys, want.key)
		}
		if !n.Editable {
			t.Errorf("node %q Editable = false, want true — Phase 2 was supposed to register "+
				"%q in the per-KB registry (internal/siteconfig/registry.go)", want.id, want.key)
		}
	}
}

// TestProjectRetrievalThresholdNodeStaysNotEditable is the inverse control
// for the test above. Without it, TestProjectPhase2NodesAreEditable cannot
// distinguish "Phase 2 registered these specific nodes" from "IsPerKB now
// returns true for everything" — a registry bug of that shape would pass
// every assertion above and only this one would catch it.
//
// "retrieve" is the control: its Keys[0] is min_similarity_threshold, and by
// inspection of every NodeSpec in nodes.go against kbConfigRegistry in
// internal/siteconfig/registry.go it is one of exactly two nodes whose
// Keys[0] is not in the per-KB registry — every other node's on/off gate is.
// It is a deliberately different shape of key, not an oversight of this
// phase: NodeRetrieve is AlwaysOn (hybrid search always runs, there is no
// on/off gate to register), so Keys[0] here is a tuning threshold that
// IsPerKB is still consulted against, not an activation flag.
//
// The second such node is NodeKBRouter (chat_kb_router_enabled), which is
// covered by TestProjectKBRouterStaysNotEditable below for a different
// reason: that key is structurally global, not a tuning threshold.
func TestProjectRetrievalThresholdNodeStaysNotEditable(t *testing.T) {
	empty := fakeReader{vals: map[string]string{}}
	g, err := Project(context.Background(), empty, empty, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeRetrieve)
	if n == nil {
		t.Fatal("retrieve node missing from projection")
	}
	if len(n.Keys) == 0 || n.Keys[0] != "min_similarity_threshold" {
		t.Fatalf("retrieve node Keys[0] = %v, want first key %q — test's key expectation is stale", n.Keys, "min_similarity_threshold")
	}
	if n.Editable {
		t.Error("retrieve node Editable = true, want false — min_similarity_threshold is not in the per-KB registry")
	}
}

// TestProjectKBRouterStaysNotEditable pins the one key Phase 2 registered and
// then had to un-register. chat_kb_router_enabled is read by maybeRouteKB
// (internal/chat/http_send.go:198) BEFORE h.forKB installs the KB overlay
// (:203), so a per-KB override of it can never be read — the global value
// always wins. Registering it did not make the stage per-KB tunable; it only
// made this node claim to be, which is precisely the class of lie the
// workflow editor exists to eliminate.
//
// Reading it after forKB is not the missing fix either: "KB A's override
// decides whether we may route away from KB A" is incoherent. The key is
// structurally global, so the node must project editable:false.
func TestProjectKBRouterStaysNotEditable(t *testing.T) {
	empty := fakeReader{vals: map[string]string{}}
	g, err := Project(context.Background(), empty, empty, LaneComplex)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	n := nodeByIDIn(g, NodeKBRouter)
	if n == nil {
		t.Fatal("kb_router node missing from projection")
	}
	if len(n.Keys) == 0 || n.Keys[0] != "chat_kb_router_enabled" {
		t.Fatalf("kb_router node Keys[0] = %v, want first key %q — test's key expectation is stale",
			n.Keys, "chat_kb_router_enabled")
	}
	if n.Editable {
		t.Error("kb_router node Editable = true, want false — chat_kb_router_enabled must NOT be in the " +
			"per-KB registry (it is read before the KB overlay exists, so a per-KB override is unreadable)")
	}
}
