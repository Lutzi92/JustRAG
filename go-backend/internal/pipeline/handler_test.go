package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
)

type fakeStore struct {
	overrides map[string]*string
	err       error
}

func (f fakeStore) ListKBOverrides(ctx context.Context, kbID string) (map[string]*string, error) {
	return f.overrides, f.err
}

// The read-only tests below never write; recordingStore (preset_apply_test.go)
// is the fake that actually records an apply.
func (f fakeStore) UpsertBatch(ctx context.Context, kbID string, kv map[string]*string) error {
	return nil
}

// fakeBindings is an in-memory bindingSource. The zero value is the common
// case: a KB with nothing attached, which resolves to "nothing bound".
type fakeBindings struct {
	opts []BindingOption
	err  error
}

func (f fakeBindings) ListBindingOptions(ctx context.Context, kbID string) ([]BindingOption, error) {
	return f.opts, f.err
}

func newTestHandler(overrides map[string]*string, globals map[string]string) *Handler {
	return NewHandler(fakeStore{overrides: overrides}, fakeReader{vals: globals}, fakeBindings{})
}

func newBindingHandler(b fakeBindings) *Handler {
	return NewHandler(fakeStore{}, fakeReader{vals: map[string]string{}}, b)
}

func doGet(t *testing.T, h *Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.GetWorkflow(rec, req)
	return rec
}

func TestGetWorkflowDefaultsToComplexLane(t *testing.T) {
	rec := doGet(t, newTestHandler(nil, map[string]string{}), "/api/kb/kb-1/workflow")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var g ProjectedGraph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if g.Lane != LaneComplex {
		t.Fatalf("Lane = %q, want %q", g.Lane, LaneComplex)
	}
	if len(g.Nodes) == 0 {
		t.Fatal("projection returned no nodes")
	}
}

func TestGetWorkflowHonoursLaneParam(t *testing.T) {
	rec := doGet(t, newTestHandler(nil, map[string]string{}), "/api/kb/kb-1/workflow?lane=lookup")

	var g ProjectedGraph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if g.Lane != LaneLookup {
		t.Fatalf("Lane = %q, want %q", g.Lane, LaneLookup)
	}
}

func TestGetWorkflowRejectsUnknownLane(t *testing.T) {
	rec := doGet(t, newTestHandler(nil, map[string]string{}), "/api/kb/kb-1/workflow?lane=nonsense")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// A KB override must be visible through the overlay.
//
// Two corrections from the brief's original draft, which toggled
// NodeFactuality via "chat_factuality_verifier_enabled":
//
//  1. NodeFactuality's actual Keys[0] is "factcheck_in_chat" — the real
//     master gate for post-response factchecking (internal/chat/siteconfig.go:224,
//     default true), changed during Task 5 after this brief was written.
//  2. Unlike Project()'s own unit tests (project_test.go), which pass a plain
//     fakeReader as both `r` and `global` and so bypass per-KB-registry
//     membership entirely, this handler builds a REAL
//     siteconfig.KBOverlayReader (siteconfig.NewKBOverlay) over the store's
//     overrides — exactly like production. KBOverlayReader only applies an
//     override for a key where siteconfig.IsPerKB(key) is true; it silently
//     falls through to the global value otherwise. "factcheck_in_chat" is
//     NOT in the per-KB registry yet (verification nodes are Editable: false
//     until Phase 2 — see the brief's Design point / self-review), so keying
//     this test on it would exercise a code path that can never fire in
//     production and the test would fail for a reason that has nothing to do
//     with a bug in the handler.
//
// So this test uses NodeCRAGGrade / "crag_enabled" instead: it is
// NodeCRAGGrade's Keys[0] AND it is a real per-KB-registry entry
// (internal/siteconfig/registry.go), so the KB override actually survives
// the overlay — this is the genuine "does a KB admin's override change what
// the KB projects?" path the test is meant to cover.
func TestGetWorkflowAppliesKBOverride(t *testing.T) {
	on := "true"
	h := newTestHandler(
		map[string]*string{"crag_enabled": &on},
		map[string]string{"crag_enabled": "false"},
	)

	// lane=lookup, not the default complex lane: on complex, CRAG projects
	// ActivationConditional regardless of its flag because the orchestrator
	// bypasses PrepareChatContext entirely (see prepareChatContextOwned), so
	// the complex lane cannot distinguish "override applied" from "override
	// lost". Lookup is where crag_enabled=true still means "runs".
	rec := doGet(t, h, "/api/kb/kb-1/workflow?lane=lookup")

	var g ProjectedGraph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	n := nodeByIDIn(&g, NodeCRAGGrade)
	if n.Activation != ActivationActive {
		t.Fatalf("Activation = %q, want %q — the KB override was not applied",
			n.Activation, ActivationActive)
	}
	if n.Origins["crag_enabled"] != "kb" {
		t.Fatalf("Origins = %q, want %q",
			n.Origins["crag_enabled"], "kb")
	}
}

// ---------------------------------------------------------------------------
// Phase 6 Task 3 — the binding on the endpoint
//
// Tasks 1 and 2 made Project react to a binding; until this task the handler
// passed a zero AgentBinding, so the live endpoint reported "nothing bound" for
// every KB in the deployment. The tests below are what stops that regressing:
// each of them passes with the projection intact and the handler rewired back
// to AgentBinding{}, which is exactly the shape of guard this branch has
// shipped vacuously before — so each was mutation-tested against that revert.
// ---------------------------------------------------------------------------

const teamID = "11111111-1111-1111-1111-111111111111"
const agentID = "22222222-2222-2222-2222-222222222222"

// boundTeam is a KB with one attachable agent and one attachable team, the team
// being the KB default — the shape ListAttachedForKB returns for a KB that has
// had a default set.
func boundTeam() fakeBindings {
	return fakeBindings{opts: []BindingOption{
		{Kind: BindingAgent, ID: agentID, Name: "Bibliotheks-Assistent"},
		{Kind: BindingTeam, ID: teamID, Name: "Recherche-Team", IsDefault: true},
	}}
}

func getGraph(t *testing.T, h *Handler, target string) *ProjectedGraph {
	t.Helper()
	rec := doGet(t, h, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var g ProjectedGraph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &g
}

// The load-bearing one: the resolved binding must reach Project, not just the
// wire. Asserted through the NODE and the ORCHESTRATOR list — the two things
// Project derives from the binding — so a handler that serialised the binding
// into the response without feeding it to Project still fails here.
func TestGetWorkflowProjectsTheResolvedBinding(t *testing.T) {
	g := getGraph(t, newBindingHandler(boundTeam()), "/api/kb/kb-1/workflow")

	n := nodeByIDIn(g, NodeAgentBinding)
	if n == nil {
		t.Fatal("agent_binding node missing from projection")
	}
	if n.Activation != ActivationActive {
		t.Errorf("Activation = %q, want %q — the handler did not pass the KB's "+
			"binding into Project", n.Activation, ActivationActive)
	}
	if !strings.Contains(n.Condition, "Recherche-Team") {
		t.Errorf("Condition = %q, want it to name the bound team", n.Condition)
	}

	var team *OrchestratorCandidate
	for i := range g.Orchestrators {
		if g.Orchestrators[i].Orchestrator == chat.OrchTeam {
			team = &g.Orchestrators[i]
		}
	}
	if team == nil {
		t.Fatalf("OrchTeam is not a candidate on a KB with a default team — got %v",
			g.Orchestrators)
	}
	if team.Activation != ActivationConditional {
		t.Errorf("OrchTeam Activation = %q, want %q", team.Activation, ActivationConditional)
	}
}

// A bound AGENT is not a bound team on the wire, and the node must say which.
func TestGetWorkflowProjectsABoundAgent(t *testing.T) {
	h := newBindingHandler(fakeBindings{opts: []BindingOption{
		{Kind: BindingAgent, ID: agentID, Name: "Bibliotheks-Assistent", IsDefault: true},
	}})
	g := getGraph(t, h, "/api/kb/kb-1/workflow")

	if g.AgentBinding.Kind != BindingAgent {
		t.Errorf("Kind = %q, want %q", g.AgentBinding.Kind, BindingAgent)
	}
	if g.AgentBinding.ID != agentID {
		t.Errorf("ID = %q, want %q", g.AgentBinding.ID, agentID)
	}
	n := nodeByIDIn(g, NodeAgentBinding)
	if n.Activation != ActivationActive {
		t.Errorf("Activation = %q, want %q", n.Activation, ActivationActive)
	}
	if !strings.Contains(n.Condition, "Agenten") {
		t.Errorf("Condition = %q, want it to say this is an agent, not a team", n.Condition)
	}
}

// The attachable set travels with the graph, so Task 4's inspector needs no
// second request — including the ids it writes back with.
func TestGetWorkflowCarriesAttachableOptions(t *testing.T) {
	g := getGraph(t, newBindingHandler(boundTeam()), "/api/kb/kb-1/workflow")

	if len(g.AgentBinding.Options) != 2 {
		t.Fatalf("Options = %v, want both the attachable agent and the attachable team",
			g.AgentBinding.Options)
	}
	byID := map[string]BindingOption{}
	for _, o := range g.AgentBinding.Options {
		byID[o.ID] = o
	}
	if got := byID[agentID]; got.Kind != BindingAgent || got.Name != "Bibliotheks-Assistent" {
		t.Errorf("agent option = %+v, want kind=agent with its name", got)
	}
	if got := byID[teamID]; got.Kind != BindingTeam || got.Name != "Recherche-Team" {
		t.Errorf("team option = %+v, want kind=team with its name", got)
	}

	// The top level names the bound one exactly once — the option rows carry
	// no isDefault, deliberately (see BindingOption).
	if g.AgentBinding.Kind != BindingTeam || g.AgentBinding.ID != teamID ||
		g.AgentBinding.Name != "Recherche-Team" {
		t.Errorf("resolved binding = %+v, want the flagged team", g.AgentBinding)
	}
}

// Nothing attached is genuinely "nothing bound" — the one case where the empty
// answer is a claim we are entitled to make.
func TestGetWorkflowUnboundKB(t *testing.T) {
	g := getGraph(t, newBindingHandler(fakeBindings{}), "/api/kb/kb-1/workflow")

	if g.AgentBinding.Kind != BindingNone {
		t.Errorf("Kind = %q, want empty", g.AgentBinding.Kind)
	}
	if g.AgentBinding.Options == nil {
		t.Error("Options is nil — it must marshal as [], not null")
	}
	n := nodeByIDIn(g, NodeAgentBinding)
	if n.Activation != ActivationInactive || n.Reason != "no_binding" {
		t.Errorf("node = %q/%q, want inactive/no_binding", n.Activation, n.Reason)
	}
}

// A default pointing at a DISABLED agent must reach the endpoint intact:
// named at the top level (so the inspector can clear it), flagged disabled,
// and projected inactive. The end-to-end version of the unit tests in
// project_test.go — the piece those cannot cover is bindingInfoFrom, which is
// what carries Disabled up from the flagged option to the top level. Drop that
// one assignment and the node goes back to claiming a switched-off agent
// answers every new chat.
func TestGetWorkflowDisabledBindingStaysVisibleAndClearable(t *testing.T) {
	h := newBindingHandler(fakeBindings{opts: []BindingOption{
		{Kind: BindingAgent, ID: agentID, Name: "Abgeschaltet", IsDefault: true, Disabled: true},
	}})
	g := getGraph(t, h, "/api/kb/kb-1/workflow")

	if g.AgentBinding.Kind != BindingAgent || g.AgentBinding.ID != agentID {
		t.Fatalf("resolved binding = %+v, want the disabled agent named — the "+
			"inspector clears by PUTting isDefault:false at this id, so an empty "+
			"id is an unclearable binding", g.AgentBinding)
	}
	if !g.AgentBinding.Disabled {
		t.Error("Disabled = false on the wire — the inspector would then offer the " +
			"binding as if it worked")
	}
	if len(g.AgentBinding.Options) != 1 || !g.AgentBinding.Options[0].Disabled {
		t.Errorf("Options = %+v, want the disabled option listed AND flagged, so the "+
			"dropdown can show it without offering it", g.AgentBinding.Options)
	}
	n := nodeByIDIn(g, NodeAgentBinding)
	if n.Activation != ActivationInactive || n.Reason != "binding_disabled" {
		t.Errorf("node = %q/%q, want inactive/binding_disabled", n.Activation, n.Reason)
	}
}

// A failed binding read must not fail the graph, and must not answer the
// question it could not read. "Nothing bound" is a claim about the KB; a KB
// whose agent listing is down is far more likely to HAVE a default than to be
// the one KB that has none.
func TestGetWorkflowSurvivesUnreadableBinding(t *testing.T) {
	h := newBindingHandler(fakeBindings{err: errors.New("agent store unreachable")})
	g := getGraph(t, h, "/api/kb/kb-1/workflow")

	if len(g.Nodes) == 0 {
		t.Fatal("no nodes — a failed binding read must not cost the whole graph")
	}
	if g.AgentBinding.Kind != BindingUnknown {
		t.Errorf("Kind = %q, want %q — a failed read must not report itself as "+
			"„nothing bound\"", g.AgentBinding.Kind, BindingUnknown)
	}
	n := nodeByIDIn(g, NodeAgentBinding)
	if n.Activation == ActivationInactive {
		t.Error("Activation = inactive — that is the claim „diese KB hat keine " +
			"Vorgabe\", which a failed read cannot make")
	}
	if n.Activation != ActivationConditional || n.Reason != "binding_unknown" {
		t.Errorf("node = %q/%q, want conditional/binding_unknown", n.Activation, n.Reason)
	}
	if n.Condition == "" {
		t.Error("Condition is empty — an unknown binding must say so in German")
	}
}
