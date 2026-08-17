package pipeline

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/siteconfig"
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	if n := nodeByIDIn(g, NodeFactuality); n.Activation != ActivationActive {
		t.Fatalf("Activation = %q, want %q", n.Activation, ActivationActive)
	}
}

// The unconditional stages — retrieval, reranking, answer generation — own no
// site_config key and must always render active.
//
// This used to be stated as "nodes with no keys are unconditional stages", and
// that is no longer true: NodeAgentBinding is keyless AND conditional on the
// agent/team link tables. The list below is enumerated rather than derived
// from `len(n.Keys) == 0` precisely so it keeps asserting what it means; a
// derived version would now be wrong, and a derived version that special-cased
// NodeAgentBinding would just be this list with extra steps. The keyless-but-
// not-unconditional case has its own guards below
// (TestProjectAgentBinding*).
func TestProjectKeylessNodesAreAlwaysActive(t *testing.T) {
	g, err := Project(context.Background(), fakeReader{vals: map[string]string{}}, fakeReader{vals: map[string]string{}}, LaneLookup, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneLookup, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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

	gOff, err := Project(context.Background(), off, off, LaneComplex, AgentBinding{})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	gOn, err := Project(context.Background(), on, on, LaneComplex, AgentBinding{})
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
	g, err := Project(context.Background(), fakeReader{vals: map[string]string{}}, fakeReader{vals: map[string]string{}}, LaneComplex, AgentBinding{})
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
		g, err := Project(context.Background(), r, r, lane, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneLookup, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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
	if _, err := Project(context.Background(), fakeReader{vals: map[string]string{}}, fakeReader{vals: map[string]string{}}, Lane("nonsense"), AgentBinding{}); err == nil {
		t.Fatal("Project accepted an unknown lane")
	}
}

func TestProjectReportsValueOrigins(t *testing.T) {
	global := fakeReader{vals: map[string]string{"crag_enabled": "false"}}
	overlaid := fakeReader{vals: map[string]string{"crag_enabled": "true"}}

	g, err := Project(context.Background(), overlaid, global, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), global, global, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), empty, empty, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneLookup, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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
	g2, err := Project(context.Background(), r2, r2, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), empty, empty, LaneComplex, AgentBinding{})
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
		g, err := Project(context.Background(), r, r, lane, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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

	g, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
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
	g2, err := Project(context.Background(), r2, r2, LaneComplex, AgentBinding{})
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
	g, err := Project(context.Background(), empty, empty, LaneComplex, AgentBinding{})
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
	g, err := Project(context.Background(), empty, empty, LaneComplex, AgentBinding{})
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
	g, err := Project(context.Background(), empty, empty, LaneComplex, AgentBinding{})
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

// ---------------------------------------------------------------------------
// Task 1 (Phase 4): registry field metadata on the wire
// ---------------------------------------------------------------------------

// TestProjectFields proves graph.Fields is a PROJECTION of the per-KB
// registry onto the node vocabulary — not a copy of either alone.
//
// Decision (see the doc comment on ProjectedGraph.Fields for the full
// reasoning): keys a node references but that have NO registry row are
// OMITTED, not synthesised. siteconfig.KBConfigField.Type is not optional —
// it tells the canvas which control to draw — and there is no source of
// truth for it outside the registry, so guessing would ship metadata that
// actively lies about a field's domain.
//
// The vocabulary below is built by walking Nodes() directly, NOT by asking
// siteconfig.All() what it thinks the per-KB surface is — comparing the
// registry to itself would prove nothing. Only the "is this vocabulary key
// registered" checks call into siteconfig, and only to decide the expected
// PRESENCE of an entry already anchored by the Nodes()-derived vocabulary.
func TestProjectFields(t *testing.T) {
	empty := fakeReader{vals: map[string]string{}}
	g, err := Project(context.Background(), empty, empty, LaneComplex, AgentBinding{})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	vocab := map[string]bool{}
	for _, n := range Nodes() {
		for _, k := range n.Keys {
			vocab[k] = true
		}
	}
	if len(vocab) == 0 {
		t.Fatal("no keys in node vocabulary — adjust the fixture")
	}

	// No entry in Fields may fall outside the vocabulary (a registry dump
	// would fail this) and every entry must be keyed by its own Key.
	for key, fld := range g.Fields {
		if !vocab[key] {
			t.Errorf("Fields contains %q, which no node's Keys references — Fields must be a "+
				"projection onto the vocabulary, not a registry dump", key)
		}
		if fld.Key != key {
			t.Errorf("Fields[%q].Key = %q, want %q (map key and entry Key must agree)", key, fld.Key, key)
		}
	}

	// Every vocabulary key that DOES have a registry row must be present.
	for key := range vocab {
		if _, ok := siteconfig.Field(key); ok {
			if _, present := g.Fields[key]; !present {
				t.Errorf("Fields missing registered vocabulary key %q", key)
			}
		}
	}

	// A known bool key.
	crag, ok := g.Fields["crag_enabled"]
	if !ok {
		t.Fatal(`Fields["crag_enabled"] missing`)
	}
	if crag.Type != siteconfig.FieldBool {
		t.Errorf("crag_enabled Type = %q, want %q", crag.Type, siteconfig.FieldBool)
	}

	// A known ranged int key.
	topN, ok := g.Fields["top_n_lookup"]
	if !ok {
		t.Fatal(`Fields["top_n_lookup"] missing`)
	}
	if topN.Min == nil || topN.Max == nil {
		t.Errorf("top_n_lookup Min/Max = %v/%v, want both non-nil", topN.Min, topN.Max)
	}

	// Two hardcoded, confirmed-unregistered vocabulary keys (pinned
	// separately by TestProjectRetrieveNodeStaysNotEditable and
	// TestProjectKBRouterStaysNotEditable via Editable==false) must be
	// ABSENT from Fields under the omit decision above. Hardcoded rather
	// than derived from a "not in siteconfig.Field" scan of the whole
	// vocabulary, so a bug that omits a key it should include cannot hide
	// behind this same loop finding it "correctly" absent.
	for _, key := range []string{"min_similarity_threshold", "chat_kb_router_enabled"} {
		if !vocab[key] {
			t.Fatalf("fixture stale: %q is no longer in the node vocabulary", key)
		}
		if _, ok := siteconfig.Field(key); ok {
			t.Fatalf("fixture stale: %q is now in the registry — pick a different unregistered example", key)
		}
		if _, present := g.Fields[key]; present {
			t.Errorf("Fields contains unregistered key %q — decision was to omit, not synthesise", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Phase 6 Task 1 — NodeAgentBinding
//
// NodeAgentBinding is the first node whose activation comes from somewhere
// other than site_config, and it is KEYLESS. Every key-driven guard in this
// package (TestNodeKeysAreActuallyRead, activationKeys in defaults_test.go)
// skips a keyless node by construction, so the tests below are the ONLY thing
// standing between this node and a silently wrong activation.
// ---------------------------------------------------------------------------

// projectWithBinding is the shared fixture: an otherwise-default deployment,
// projected on the complex lane with one binding.
func projectWithBinding(t *testing.T, b AgentBinding) *ProjectedGraph {
	t.Helper()
	empty := fakeReader{vals: map[string]string{}}
	g, err := Project(context.Background(), empty, empty, LaneComplex, b)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	return g
}

// The one the plan calls out for mutation testing: with nothing bound the node
// must be INACTIVE.
//
// Project's activation switch has a keyless shortcut (`len(spec.Keys) == 0` →
// active) that sits one line below NodeAgentBinding's own branch. Delete that
// branch and this node falls straight through it and renders "aktiv" on every
// KB in the deployment — a canvas claiming every KB has a default agent.
func TestProjectAgentBindingUnboundIsInactive(t *testing.T) {
	n := nodeByIDIn(projectWithBinding(t, AgentBinding{}), NodeAgentBinding)
	if n == nil {
		t.Fatal("agent_binding node missing from projection")
	}
	if n.Activation != ActivationInactive {
		t.Errorf("Activation = %q, want %q", n.Activation, ActivationInactive)
	}
	if n.Reason != "no_binding" {
		t.Errorf("Reason = %q, want %q", n.Reason, "no_binding")
	}
	if n.Condition == "" {
		t.Error("Condition is empty — an inactive node whose Reason is not about a " +
			"config key must say what an operator would have to do")
	}
	// A binding node with keys would mean somebody gave it a synthetic
	// site_config key, which is exactly what the design forbids.
	if len(n.Keys) != 0 {
		t.Errorf("Keys = %v, want empty — the binding owns no site_config key", n.Keys)
	}
	if n.Editable {
		t.Error("Editable = true — Editable is derived from Keys[0] and there is no Keys[0]")
	}
}

func TestProjectAgentBindingTeamIsActive(t *testing.T) {
	n := nodeByIDIn(projectWithBinding(t, AgentBinding{Kind: BindingTeam, Name: "Recherche-Team"}), NodeAgentBinding)
	if n == nil {
		t.Fatal("agent_binding node missing from projection")
	}
	if n.Activation != ActivationActive {
		t.Errorf("Activation = %q, want %q", n.Activation, ActivationActive)
	}
	if n.Reason != "" {
		t.Errorf("Reason = %q, want empty on an active node", n.Reason)
	}
	if !strings.Contains(n.Condition, "Recherche-Team") {
		t.Errorf("Condition = %q, want it to name the bound team", n.Condition)
	}
	if !strings.Contains(n.Condition, "Team") {
		t.Errorf("Condition = %q, want it to say this is a team", n.Condition)
	}
}

func TestProjectAgentBindingAgentIsActive(t *testing.T) {
	n := nodeByIDIn(projectWithBinding(t, AgentBinding{Kind: BindingAgent, Name: "Bibliotheks-Assistent"}), NodeAgentBinding)
	if n == nil {
		t.Fatal("agent_binding node missing from projection")
	}
	if n.Activation != ActivationActive {
		t.Errorf("Activation = %q, want %q", n.Activation, ActivationActive)
	}
	if !strings.Contains(n.Condition, "Bibliotheks-Assistent") {
		t.Errorf("Condition = %q, want it to name the bound agent", n.Condition)
	}
	if !strings.Contains(n.Condition, "Agenten") {
		t.Errorf("Condition = %q, want it to say this is an agent, not a team", n.Condition)
	}
}

// ---------------------------------------------------------------------------
// A bound-but-DISABLED default: the row exists, and nothing routes through it.
//
// agent_kb_links.is_default survives the agent being switched off. Chat-time
// resolution filters is_enabled (LoadAgentForChat / LoadTeamForChat), and so
// does the list the web client picks the default from, so such a KB really does
// answer with the standard path. The canvas used to be told nothing about the
// row at all — ListAttachedForKB filtered it away — and therefore projected
// „keine Vorgabe" on a KB that had one, which an admin could neither see nor
// clear, and which came back the moment anyone re-enabled the agent.
//
// The projection now has to say BOTH things at once: the row exists (so it can
// be cleared) and it does not run (so nothing below it is misreported).
// ---------------------------------------------------------------------------

func TestProjectDisabledBindingIsInactiveAndSaysWhy(t *testing.T) {
	n := nodeByIDIn(projectWithBinding(t,
		AgentBinding{Kind: BindingAgent, Name: "Bibliotheks-Assistent", Disabled: true}),
		NodeAgentBinding)
	if n == nil {
		t.Fatal("agent_binding node missing from projection")
	}
	// INACTIVE, not active: „aktiv" would claim the agent answers, and it does
	// not. Not conditional either — there is no circumstance in which a
	// switched-off agent takes a turn.
	if n.Activation != ActivationInactive {
		t.Errorf("Activation = %q, want %q — a switched-off agent answers nothing, "+
			"so anything but inaktiv is the lie this node exists to prevent",
			n.Activation, ActivationInactive)
	}
	// A DISTINCT reason from "no_binding": the two states need different
	// actions from the admin (nothing to do vs. a row to re-enable or clear),
	// and collapsing them is what made the row invisible in the first place.
	if n.Reason != "binding_disabled" {
		t.Errorf("Reason = %q, want %q — „nichts hinterlegt\" and „hinterlegt, aber "+
			"abgeschaltet\" are different states", n.Reason, "binding_disabled")
	}
	if !strings.Contains(n.Condition, "Bibliotheks-Assistent") {
		t.Errorf("Condition = %q, want it to name the bound agent — an admin cannot "+
			"clear what the canvas will not name", n.Condition)
	}
	if !strings.Contains(n.Condition, "abgeschaltet") {
		t.Errorf("Condition = %q, want it to say the binding is switched off", n.Condition)
	}
}

func TestProjectDisabledTeamBindingIsInactiveAndNamesTheTeam(t *testing.T) {
	n := nodeByIDIn(projectWithBinding(t,
		AgentBinding{Kind: BindingTeam, Name: "Recherche-Team", Disabled: true}),
		NodeAgentBinding)
	if n == nil {
		t.Fatal("agent_binding node missing from projection")
	}
	if n.Activation != ActivationInactive || n.Reason != "binding_disabled" {
		t.Errorf("node = %q/%q, want inactive/binding_disabled", n.Activation, n.Reason)
	}
	if !strings.Contains(n.Condition, "Recherche-Team") {
		t.Errorf("Condition = %q, want it to name the bound team", n.Condition)
	}
	if !strings.Contains(n.Condition, "Team") {
		t.Errorf("Condition = %q, want it to say this is a team", n.Condition)
	}
}

// The other half, and the one that makes the projection honest rather than
// merely informative: a disabled binding must change NOTHING below it. The
// orchestrator really is the ordinary fallback, and it really does simply run.
//
// This is the assertion most likely to pass vacuously — "OrchTeam is absent" is
// also true of a projection that produced nothing — so it checks the fallback
// is present AND still active, which a demoted (bound-KB) projection is not.
func TestProjectDisabledBindingChangesNoOrchestrator(t *testing.T) {
	g := projectWithBinding(t,
		AgentBinding{Kind: BindingTeam, Name: "Recherche-Team", Disabled: true})

	if c := orchCandidate(g, chat.OrchTeam); c != nil {
		t.Fatalf("OrchTeam projected for a DISABLED binding: %+v — chat-time "+
			"resolution rejects a switched-off team, so the turn never gets there",
			c)
	}
	if len(g.Orchestrators) == 0 {
		t.Fatal("no orchestrator candidates at all — the assertion above passed vacuously")
	}
	last := g.Orchestrators[len(g.Orchestrators)-1]
	if last.Activation != ActivationActive || last.Condition != "" {
		t.Errorf("fallback %q = %q/%q, want active with no condition — demoting it "+
			"would claim the KB's default takes turns away from it, and it does not",
			last.Orchestrator, last.Activation, last.Condition)
	}
}

// The PrepareChatContext bypass is driven by the same Bound() predicate, so it
// gets its own guard: with a disabled binding a lookup turn reaches the
// standard path exactly as on an unbound KB, and CRAG must still read „aktiv".
func TestProjectDisabledBindingDoesNotBypassTheStandardPath(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"crag_enabled":             "true",
		"adaptive_routing_enabled": "false",
	}}
	g, err := Project(context.Background(), r, r, LaneLookup,
		AgentBinding{Kind: BindingTeam, Name: "Recherche-Team", Disabled: true})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	n := nodeByIDIn(g, NodeCRAGGrade)
	if n == nil {
		t.Fatal("crag_grade node missing from projection")
	}
	if n.Activation != ActivationActive {
		t.Errorf("crag_grade = %q/%q, want aktiv — a switched-off default takes no "+
			"turn into the deep-chat path, so the standard path really does run",
			n.Activation, n.Reason)
	}
}

// ---------------------------------------------------------------------------
// A bound team with no ENABLED member — the second way a link row can exist and
// never fire, and the one the canvas used to miss entirely.
//
// The team itself is switched ON, so is_enabled says nothing is wrong. But
// LoadTeamForChat filters its members by is_enabled too, and
// resolveTeamSelection (internal/chat/http_send.go) turns zero members into
// (nil, "empty_team"): the selection is dropped and the FULL standard path
// runs. A team can reach that state by having its members switched off, or by
// being created empty — CreateTeam accepts memberIds: [].
//
// Before this, such a KB drew the node aktiv, projected OrchTeam, and demoted
// every PrepareChatContext-owned stage to bedingt on all three lanes, for a
// binding that can never take a single turn.
// ---------------------------------------------------------------------------

func TestProjectEmptyTeamBindingIsInactiveAndSaysWhy(t *testing.T) {
	n := nodeByIDIn(projectWithBinding(t,
		AgentBinding{Kind: BindingTeam, Name: "Recherche-Team", EmptyTeam: true}),
		NodeAgentBinding)
	if n == nil {
		t.Fatal("agent_binding node missing from projection")
	}
	if n.Activation != ActivationInactive {
		t.Errorf("Activation = %q, want %q — a team with nobody in it answers "+
			"nothing, and the standard path runs instead",
			n.Activation, ActivationInactive)
	}
	// Its OWN reason, not "binding_disabled". „Abgeschaltet" would send the
	// admin to a switch that is already on; „keine aktiven Mitglieder" is the
	// work that actually needs doing.
	if n.Reason != "binding_empty_team" {
		t.Errorf("Reason = %q, want %q — an unstaffed team and a switched-off one "+
			"need different things from an admin", n.Reason, "binding_empty_team")
	}
	if !strings.Contains(n.Condition, "Recherche-Team") {
		t.Errorf("Condition = %q, want it to name the bound team", n.Condition)
	}
	if !strings.Contains(n.Condition, "Mitglied") {
		t.Errorf("Condition = %q, want it to say the team has no active member — "+
			"that is the whole difference from binding_disabled", n.Condition)
	}
	// The negation guard: „abgeschaltet" here would be the wrong instruction,
	// and it is the word a future editor is most likely to reach for.
	if strings.Contains(n.Condition, "abgeschaltet") {
		t.Errorf("Condition = %q, must NOT call an enabled team „abgeschaltet\" — "+
			"the switch is already on", n.Condition)
	}
}

// A team that is BOTH switched off and unstaffed keeps the disabled reason
// (the more fundamental fault) but must not promise that re-enabling alone
// brings the binding back — it does not.
func TestProjectDisabledAndEmptyTeamSaysBothFixesAreNeeded(t *testing.T) {
	n := nodeByIDIn(projectWithBinding(t, AgentBinding{
		Kind: BindingTeam, Name: "Recherche-Team", Disabled: true, EmptyTeam: true,
	}), NodeAgentBinding)
	if n == nil {
		t.Fatal("agent_binding node missing from projection")
	}
	if n.Activation != ActivationInactive || n.Reason != "binding_disabled" {
		t.Errorf("node = %q/%q, want inactive/binding_disabled", n.Activation, n.Reason)
	}
	if !strings.Contains(n.Condition, "Mitglied") {
		t.Errorf("Condition = %q, want the member half of the fix as well — "+
			"switching the team back on alone changes nothing", n.Condition)
	}
	if strings.Contains(n.Condition, "sobald jemand") {
		t.Errorf("Condition = %q, must not promise the binding springs back when "+
			"someone re-enables the team: it stays inert until it is staffed",
			n.Condition)
	}
}

// The half that makes the projection honest rather than merely informative: an
// empty team must change NOTHING below it. Same shape as the disabled guard,
// including its anti-vacuity check on the fallback.
func TestProjectEmptyTeamBindingChangesNoOrchestrator(t *testing.T) {
	g := projectWithBinding(t,
		AgentBinding{Kind: BindingTeam, Name: "Recherche-Team", EmptyTeam: true})

	if c := orchCandidate(g, chat.OrchTeam); c != nil {
		t.Fatalf("OrchTeam projected for a team with no active member: %+v — "+
			"resolveTeamSelection drops the selection („empty_team\") and the turn "+
			"never gets there", c)
	}
	if len(g.Orchestrators) == 0 {
		t.Fatal("no orchestrator candidates at all — the assertion above passed vacuously")
	}
	last := g.Orchestrators[len(g.Orchestrators)-1]
	if last.Activation != ActivationActive || last.Condition != "" {
		t.Errorf("fallback %q = %q/%q, want active with no condition — an unstaffed "+
			"team takes no turn away from it",
			last.Orchestrator, last.Activation, last.Condition)
	}
}

// And the bypass, which is what the review actually caught: on a KB bound to an
// empty team, a lookup turn runs the full standard path, so CRAG must read
// „aktiv" rather than „bedingt".
func TestProjectEmptyTeamBindingDoesNotBypassTheStandardPath(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"crag_enabled":             "true",
		"adaptive_routing_enabled": "false",
	}}
	g, err := Project(context.Background(), r, r, LaneLookup,
		AgentBinding{Kind: BindingTeam, Name: "Recherche-Team", EmptyTeam: true})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	n := nodeByIDIn(g, NodeCRAGGrade)
	if n == nil {
		t.Fatal("crag_grade node missing from projection")
	}
	if n.Activation != ActivationActive {
		t.Errorf("crag_grade = %q/%q, want aktiv — a team with no active member "+
			"never takes the deep-chat path, so the standard path really does run",
			n.Activation, n.Reason)
	}
}

// Bound() is the single predicate every guard above hangs off. Pinned directly
// so a future reader cannot conclude it merely means "a row exists": it means
// „the binding changes what runs", and both a switched-off entry and an
// unstaffed team fail that.
func TestAgentBindingBoundIsFalseWhenItCannotRun(t *testing.T) {
	cases := []struct {
		name string
		b    AgentBinding
		want bool
	}{
		{"enabled agent", AgentBinding{Kind: BindingAgent}, true},
		{"enabled team", AgentBinding{Kind: BindingTeam}, true},
		{"disabled agent", AgentBinding{Kind: BindingAgent, Disabled: true}, false},
		{"disabled team", AgentBinding{Kind: BindingTeam, Disabled: true}, false},
		// Teams only: an agent has no membership, so EmptyTeam must never be
		// set for one — and if it somehow were, Bound() is allowed to be
		// conservative. What must NOT happen is a bound team with nobody in it
		// reading as live.
		{"empty team", AgentBinding{Kind: BindingTeam, EmptyTeam: true}, false},
		{"disabled and empty team", AgentBinding{Kind: BindingTeam, Disabled: true, EmptyTeam: true}, false},
		{"nothing bound", AgentBinding{}, false},
		{"unknown", AgentBinding{Kind: BindingUnknown}, false},
	}
	for _, c := range cases {
		if got := c.b.Bound(); got != c.want {
			t.Errorf("%s: Bound() = %v, want %v — Bound means „the binding changes "+
				"what runs\", not „a row exists\"", c.name, got, c.want)
		}
	}
}

// The node's Help text is the only place the three-part truth about the
// binding is written down for an operator, and the spec (§7.3) requires all
// three parts. The third — that the API/OpenAI-compat/MCP surfaces ignore the
// binding entirely — is the one nobody would notice going missing, because
// nothing else in the product says it anywhere.
//
// This asserts the CLAIMS, not the vocabulary. The first version checked for
// bare tokens ("neue", "Web", "umste", …) and a Help rewritten to state the
// exact OPPOSITE of two of the three truths — „greift im Web-Chat und in JEDEM
// Chat … du kannst sie nicht umstellen“ — kept it green, because "neue" was
// still satisfied by the first sentence and "umste" by the word "umstellen"
// inside the negation. A token that appears inside its own negation cannot
// guard a claim, so every claim below carries a `want` pattern that only the
// affirmative phrasing matches AND, where the claim has a natural negation, a
// `reject` pattern that fails loudly when someone writes it.
func TestAgentBindingHelpStatesTheThreeTruths(t *testing.T) {
	spec, ok := NodeByID(NodeAgentBinding)
	if !ok {
		t.Fatal("NodeAgentBinding is not in the vocabulary")
	}

	claims := []struct {
		claim  string
		want   *regexp.Regexp
		reject *regexp.Regexp
	}{
		{
			claim: "die Vorgabe greift nur im Web-Chat und nur für NEU gestartete Chats",
			// "nur … neu(e|gestartete) … Chats" — a restriction, not a mention.
			want:   regexp.MustCompile(`(?i)nur[^.]{0,40}neu[^.]{0,20}chats`),
			reject: regexp.MustCompile(`(?i)jede[mnrs]?\s+chat|in allen chats|auch für laufende`),
		},
		{
			claim:  "die Vorgabe greift im Web-Chat",
			want:   regexp.MustCompile(`(?i)web`),
			reject: nil,
		},
		{
			claim: "im laufenden Chat kannst du sie auf etwas anderes umstellen",
			// The override must be tied to the running chat; the bare verb is
			// not enough, because the negation contains it too.
			want:   regexp.MustCompile(`(?i)(im laufenden chat|während des chats|jederzeit)[^.]{0,60}(umstellen|umstellbar|ändern|wechseln)`),
			reject: regexp.MustCompile(`(?i)(nicht|nie)\s+(mehr\s+)?(umstellen|ändern|wechseln|umstellbar)`),
		},
		{
			claim: "API, OpenAI-Schnittstelle und MCP ignorieren die Vorgabe",
			// All three surfaces named, and the verb that says what they do
			// with the binding — in one sentence, so naming MCP somewhere else
			// entirely does not satisfy it.
			want:   regexp.MustCompile(`(?i)api[^.]*openai[^.]*mcp[^.]*ignorier`),
			reject: regexp.MustCompile(`(?i)(api|openai|mcp)[^.]{0,80}(gilt auch|berücksichtig|verwenden die vorgabe|nutzen die vorgabe)`),
		},
	}

	for _, c := range claims {
		if !c.want.MatchString(spec.Help) {
			t.Errorf("Help does not state the claim %q (no match for %s) — required by "+
				"spec §7.3.\nHelp = %q", c.claim, c.want, spec.Help)
		}
		if c.reject != nil && c.reject.MatchString(spec.Help) {
			t.Errorf("Help states the NEGATION of the claim %q (matched %s) — that is not "+
				"what the code does.\nHelp = %q", c.claim, c.reject, spec.Help)
		}
	}
}

// ---------------------------------------------------------------------------
// Phase 6 Task 2 — TeamSelected reaches orchestratorCandidates
// ---------------------------------------------------------------------------

func orchCandidate(g *ProjectedGraph, o chat.Orchestrator) *OrchestratorCandidate {
	for i := range g.Orchestrators {
		if g.Orchestrators[i].Orchestrator == o {
			return &g.Orchestrators[i]
		}
	}
	return nil
}

// The other one the plan calls out for mutation testing, and the one most
// likely to pass vacuously: without a binding, OrchTeam must not be projected
// AT ALL. It is the current (broken) behaviour, so a test asserting it can
// look green while asserting nothing — the paired
// TestProjectBoundTeamAddsConditionalOrchTeam above is what proves this
// assertion can go red.
func TestProjectWithoutBindingHasNoOrchTeam(t *testing.T) {
	g := projectWithBinding(t, AgentBinding{})
	if c := orchCandidate(g, chat.OrchTeam); c != nil {
		t.Fatalf("OrchTeam projected with nothing bound: %+v (all: %+v)", c, g.Orchestrators)
	}
	// Guard against the vacuous version of this test: if the projection
	// returned no candidates at all the assertion above would also pass.
	if len(g.Orchestrators) == 0 {
		t.Fatal("no orchestrator candidates at all — the assertion above passed vacuously")
	}
}

func TestProjectBoundTeamAddsConditionalOrchTeam(t *testing.T) {
	g := projectWithBinding(t, AgentBinding{Kind: BindingTeam, Name: "Recherche-Team"})

	c := orchCandidate(g, chat.OrchTeam)
	if c == nil {
		t.Fatalf("OrchTeam not projected with a team bound: %+v", g.Orchestrators)
	}
	if c.Activation != ActivationConditional {
		t.Errorf("OrchTeam Activation = %q, want %q — the user can override the "+
			"binding per chat and the non-browser surfaces ignore it",
			c.Activation, ActivationConditional)
	}
	if c.Condition == "" {
		t.Error("OrchTeam has no Condition — a conditional candidate must say when it fires")
	}

	// The FALLBACK ORCHESTRATOR must not move. Project feeds it into
	// complexBypassCondition, so folding TeamSelected into `base` instead of
	// making it a predicate would rewrite every PrepareChatContext-owned node's
	// explanation.
	last := g.Orchestrators[len(g.Orchestrators)-1]
	if last.Orchestrator != chat.OrchStandard {
		t.Errorf("fallback = %q, want %q — a binding must not change which "+
			"orchestrator wins when the request carries no selection",
			last.Orchestrator, chat.OrchStandard)
	}
	// …but its ACTIVATION must: on a bound KB the fallback is not what the
	// default web chat does, so claiming it plainly runs is the misreport this
	// phase exists to fix, one rung lower down.
	if last.Activation != ActivationConditional {
		t.Errorf("fallback %q is %q, want %q — with a default bound it runs only "+
			"when the binding does not reach the turn", last.Orchestrator,
			last.Activation, ActivationConditional)
	}
	if !strings.Contains(last.Condition, "Vorgabe dieser KB") {
		t.Errorf("fallback Condition = %q, want it to say that it runs only when the "+
			"KB's default does not apply", last.Condition)
	}
}

// Precedence order, with a fixture in which a WRONG order produces a different
// list. The all-defaults fixture cannot do that: with every orchestrator flag
// off, the corpus-table and global-synthesis predicates both collapse into the
// fallback and get dropped, so team is the only entry and "team is first" holds
// no matter where its predicate sits. Moving the team predicate to last in
// `predicates` left the whole package green — this is that gap.
//
// So: turn on the corpus table, DRIFT and the supervisor, and pin the FULL
// sequence against chat.SelectOrchestrator's ladder (team > corpus_table >
// drift > supervisor). The canvas lists candidates in this order; listing them
// out of precedence order would misstate which one wins when two could fire.
func TestProjectCandidatesFollowSelectOrchestratorPrecedence(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_corpus_table_enabled": "true",
		"chat_drift_enabled":        "true",
		"chat_supervisor_enabled":   "true",
	}}
	g, err := Project(context.Background(), r, r, LaneComplex,
		AgentBinding{Kind: BindingTeam, Name: "Recherche-Team"})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	want := []chat.Orchestrator{
		chat.OrchTeam,        // TeamSelected — above everything but comparison
		chat.OrchCorpusTable, // IsCorpusQuery
		chat.OrchDrift,       // IsGlobalSynthesis
		chat.OrchSupervisor,  // the fallback
	}
	got := make([]chat.Orchestrator, 0, len(g.Orchestrators))
	for _, c := range g.Orchestrators {
		got = append(got, c.Orchestrator)
	}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %v, want %v (position %d differs) — candidates must "+
				"be listed in chat.SelectOrchestrator's precedence order", got, want, i)
		}
	}
}

// A bound AGENT routes through OrchTeam too, and that is not a simplification:
// internal/chat/http_send.go sets OrchestratorInputs.TeamSelected from
// `teamSel != nil`, and resolveTeamSelection returns a non-nil selection for
// body.AgentID exactly as it does for body.TeamID. The OrchTeam branch then
// runs the lone agent as a one-member team. Projecting an agent binding as
// "changes nothing" would be the misreport this phase exists to fix, just in
// the other direction.
func TestProjectBoundAgentAlsoAddsOrchTeam(t *testing.T) {
	g := projectWithBinding(t, AgentBinding{Kind: BindingAgent, Name: "Bibliotheks-Assistent"})
	c := orchCandidate(g, chat.OrchTeam)
	if c == nil {
		t.Fatalf("OrchTeam not projected with an agent bound: %+v", g.Orchestrators)
	}
	if c.Activation != ActivationConditional {
		t.Errorf("OrchTeam Activation = %q, want %q", c.Activation, ActivationConditional)
	}
}

// The team gate in SelectOrchestrator does not require complex_reasoning — it
// sits above complexAndUnenhanced entirely — so a bound default is a candidate
// on the lookup and enumeration lanes too. TestProjectLookupLaneYieldsStandardOnly
// pins the unbound case; this pins that the binding really does widen it.
func TestProjectBoundTeamIsACandidateOnEveryLane(t *testing.T) {
	empty := fakeReader{vals: map[string]string{}}
	for _, lane := range []Lane{LaneLookup, LaneEnumeration, LaneComplex} {
		g, err := Project(context.Background(), empty, empty, lane, AgentBinding{Kind: BindingTeam, Name: "T"})
		if err != nil {
			t.Fatalf("Project(%s): %v", lane, err)
		}
		if orchCandidate(g, chat.OrchTeam) == nil {
			t.Errorf("lane %s: OrchTeam not projected with a team bound: %+v", lane, g.Orchestrators)
		}
	}
}

// ---------------------------------------------------------------------------
// Phase 6 Tasks 1-2, review round 1 — a bound default demotes what sits below
// the team gate.
//
// The team gate in chat.SelectOrchestrator sits ABOVE the complexity check, so
// on a KB with a default bound the ordinary web chat goes to chat.OrchTeam on
// every lane. Everything the canvas draws as plainly running below that gate is
// therefore a claim about turns the binding does NOT reach.
// ---------------------------------------------------------------------------

// The supervisor is the one orchestrator that runs the sufficient-context gate
// itself, which is why NodeSufficientCtx is exempted from the complex-lane
// bypass when the supervisor wins the fallback. That exemption is a statement
// about the fallback, and on a bound KB the fallback is not the common path:
// RunTeamChat never calls ai.JudgeContextSufficiency. Drawing „aktiv“ there
// tells a KB admin the abstention gate protects their users when it does not.
func TestProjectSufficientContextConditionalUnderSupervisorWhenBound(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_supervisor_enabled":         "true",
		"chat_sufficient_context_enabled": "true",
	}}

	for _, b := range []AgentBinding{
		{Kind: BindingTeam, Name: "Recherche-Team"},
		{Kind: BindingAgent, Name: "Bibliotheks-Assistent"},
	} {
		g, err := Project(context.Background(), r, r, LaneComplex, b)
		if err != nil {
			t.Fatalf("Project: %v", err)
		}
		n := nodeByIDIn(g, NodeSufficientCtx)
		if n == nil {
			t.Fatal("sufficient_ctx node missing from projection")
		}
		// Conditional, NOT inactive: a user can switch the agent off inside the
		// chat, and then the supervisor — and its gate — really does run.
		if n.Activation != ActivationConditional {
			t.Errorf("%s binding: Activation = %q, want %q — the default web chat goes "+
				"to the team, which never runs this gate", b.Kind, n.Activation, ActivationConditional)
		}
		if n.Reason != "orchestrator_bypass" {
			t.Errorf("%s binding: Reason = %q, want %q", b.Kind, n.Reason, "orchestrator_bypass")
		}
		// The condition must name the bound default, not just say "an
		// orchestrator answers directly" — the operator's next question is
		// "which one, and can I change it?".
		if !strings.Contains(n.Condition, b.Name) {
			t.Errorf("%s binding: Condition = %q, want it to name the bound default %q",
				b.Kind, n.Condition, b.Name)
		}
		if !strings.Contains(n.Condition, "Supervisor") {
			t.Errorf("%s binding: Condition = %q, want it to say the supervisor still "+
				"runs this check when the binding does not apply", b.Kind, n.Condition)
		}
	}
}

// The same misreport, one lane over: on a bound KB the standard path loses the
// LOOKUP and ENUMERATION lanes too.
//
// http_send.go:299 takes the deep-chat path when `isComplex || runCompare ||
// teamSel != nil`, and web/src/hooks/useChatStream.ts sends agentSelection.teamId
// with every message of a chat that started from the KB default. So a plain
// lookup on a bound KB goes to RunTeamChat and never reaches
// PrepareChatContext — while the canvas drew „CRAG-Bewertung: aktiv“.
//
// TestProjectPrepareChatContextStagesActiveOnLookupLane pins the UNBOUND case
// (they really are active there), which is what keeps this from being a
// blanket downgrade.
func TestProjectStandardPathStagesConditionalOnEveryLaneWhenBound(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"crag_enabled":                     "true",
		"adaptive_routing_enabled":         "false",
		"chat_context_compression_enabled": "true",
		"chat_sufficient_context_enabled":  "true",
	}}

	for _, lane := range []Lane{LaneLookup, LaneEnumeration} {
		g, err := Project(context.Background(), r, r, lane,
			AgentBinding{Kind: BindingTeam, Name: "Recherche-Team"})
		if err != nil {
			t.Fatalf("Project(%s): %v", lane, err)
		}
		for _, id := range []NodeID{NodeCRAGGrade, NodeCRAGRewrite, NodeCompression, NodeSufficientCtx} {
			n := nodeByIDIn(g, id)
			if n == nil {
				t.Fatalf("lane %s: node %q missing from projection", lane, id)
			}
			// Conditional, not inactive: without the binding on the turn the
			// standard path runs on these lanes exactly as before.
			if n.Activation != ActivationConditional {
				t.Errorf("lane %s: node %q Activation = %q, want %q — a bound default "+
					"routes every web turn into the deep-chat path",
					lane, id, n.Activation, ActivationConditional)
			}
			if n.Reason != "orchestrator_bypass" {
				t.Errorf("lane %s: node %q Reason = %q, want %q",
					lane, id, n.Reason, "orchestrator_bypass")
			}
			if !strings.Contains(n.Condition, "Recherche-Team") {
				t.Errorf("lane %s: node %q Condition = %q, want it to name the bound "+
					"default", lane, id, n.Condition)
			}
		}
	}
}

// Every candidate below the team gate must stop claiming to run — including the
// fallback, which is the one the canvas prints as „aktiv“.
func TestProjectBoundDefaultDemotesEveryOtherCandidate(t *testing.T) {
	r := fakeReader{vals: map[string]string{
		"chat_corpus_table_enabled": "true",
		"chat_drift_enabled":        "true",
		"chat_supervisor_enabled":   "true",
	}}

	unbound, err := Project(context.Background(), r, r, LaneComplex, AgentBinding{})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	// Negative control: without a binding the fallback IS plainly active, so the
	// assertion below is about the binding and not about the fixture.
	if c := orchCandidate(unbound, chat.OrchSupervisor); c == nil || c.Activation != ActivationActive {
		t.Fatalf("unbound: supervisor = %+v, want the active fallback", c)
	}

	bound, err := Project(context.Background(), r, r, LaneComplex,
		AgentBinding{Kind: BindingTeam, Name: "Recherche-Team"})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	for _, c := range bound.Orchestrators {
		if c.Orchestrator == chat.OrchTeam {
			continue
		}
		if c.Activation != ActivationConditional {
			t.Errorf("candidate %q is %q on a KB with a default bound, want %q — it runs "+
				"only when the binding does not reach the turn",
				c.Orchestrator, c.Activation, ActivationConditional)
		}
		if !strings.Contains(c.Condition, "Vorgabe dieser KB") {
			t.Errorf("candidate %q Condition = %q, want it to say the KB default must "+
				"not apply", c.Orchestrator, c.Condition)
		}
	}
	// The corpus-table candidate keeps its OWN condition too: a reader must not
	// lose "only for comparison questions" when the binding clause is appended.
	if c := orchCandidate(bound, chat.OrchCorpusTable); c == nil ||
		!strings.Contains(c.Condition, "Vergleich") {
		t.Errorf("corpus_table candidate = %+v, want its own query condition kept", c)
	}
}
