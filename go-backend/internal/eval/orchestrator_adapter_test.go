package eval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/vector"
)

// stubSiteCfg returns hard-coded values for the gate keys. Any other key
// returns nil so the gate helpers' defaults fire.
type stubSiteCfg struct{ values map[string]string }

func (s *stubSiteCfg) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if v, ok := s.values[key]; ok {
		return &v, nil
	}
	return nil, nil
}

func TestSelectOrchestrator_StandardWhenAllGatesOff(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeComplexReasoning)
	if got != OrchestratorStandard {
		t.Fatalf("got %q, want %q", got, OrchestratorStandard)
	}
	if reason != "fallback_no_orchestrator_enabled" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestSelectOrchestrator_StandardWhenNotComplexReasoning(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{
		"chat_supervisor_enabled":   "true",
		"chat_plan_execute_enabled": "true",
		"chat_agentic_enabled":      "true",
	}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeLookup)
	if got != OrchestratorStandard {
		t.Fatalf("got %q, want %q", got, OrchestratorStandard)
	}
	if reason != "fallback_query_type_lookup" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestSelectOrchestrator_SupervisorWinsWhenEnabled(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{
		"chat_supervisor_enabled":   "true",
		"chat_plan_execute_enabled": "true",
		"chat_agentic_enabled":      "true",
	}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeComplexReasoning)
	if got != OrchestratorSupervisor {
		t.Fatalf("got %q, want %q", got, OrchestratorSupervisor)
	}
	if reason != "complex_reasoning_supervisor_gate" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestSelectOrchestrator_PlanExecuteDAGWhenSupervisorOff(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{
		"chat_plan_execute_enabled": "true",
		"chat_plan_execute_dag":     "true",
	}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeComplexReasoning)
	if got != OrchestratorPlanExecuteDAG {
		t.Fatalf("got %q, want %q", got, OrchestratorPlanExecuteDAG)
	}
	if reason != "complex_reasoning_plan_execute_dag_gate" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestSelectOrchestrator_PlanExecuteFlatWhenDAGOff(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{
		"chat_plan_execute_enabled": "true",
	}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeComplexReasoning)
	if got != OrchestratorPlanExecute {
		t.Fatalf("got %q, want %q", got, OrchestratorPlanExecute)
	}
	if reason != "complex_reasoning_plan_execute_gate" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestSelectOrchestrator_AgenticLast(t *testing.T) {
	cfg := &stubSiteCfg{values: map[string]string{
		"chat_agentic_enabled": "true",
	}}
	got, reason := SelectOrchestrator(context.Background(), cfg, vector.QueryTypeComplexReasoning)
	if got != OrchestratorAgentic {
		t.Fatalf("got %q, want %q", got, OrchestratorAgentic)
	}
	if reason != "complex_reasoning_agentic_gate" {
		t.Fatalf("got reason %q", reason)
	}
}

func TestBuildAgentTrace_PlanExecuteDAG(t *testing.T) {
	events := []chat.TrajectoryEvent{
		{Stage: "plan", Queries: []string{"a", "b", "c"}},
		{Stage: "search", Step: 1, Query: "a"},
		{Stage: "search", Step: 2, Query: "b"},
		{Stage: "answer", Decision: "answered"},
	}
	got := BuildAgentTrace(OrchestratorPlanExecuteDAG, "complex_reasoning_plan_execute_dag_gate", events, PlanShapeInputs{DAG: true, MaxDepth: 4, Iterative: true})
	if got.Orchestrator != OrchestratorPlanExecuteDAG {
		t.Fatalf("orchestrator: got %q", got.Orchestrator)
	}
	if got.Plan == nil {
		t.Fatal("Plan must be non-nil for plan_execute*")
	}
	if got.Plan.NodeCount != 3 || got.Plan.MaxDepth != 4 || !got.Plan.DAG || !got.Plan.Iterative {
		t.Fatalf("Plan = %+v", *got.Plan)
	}
	if got.Tools["kb_search"] != 2 {
		t.Fatalf("Tools[kb_search] = %d, want 2", got.Tools["kb_search"])
	}
}

func TestBuildAgentTrace_Supervisor(t *testing.T) {
	events := []chat.TrajectoryEvent{
		{Stage: "decision", Decision: "agent_dispatch", Reason: "retriever", Findings: 5},
		{Stage: "search", Step: 1},
	}
	got := BuildAgentTrace(OrchestratorSupervisor, "complex_reasoning_supervisor_gate", events, PlanShapeInputs{})
	if got.Specialist != "retriever" {
		t.Fatalf("Specialist = %q", got.Specialist)
	}
	if got.Plan != nil {
		t.Fatalf("Plan must be nil for supervisor; got %+v", *got.Plan)
	}
}

func TestBuildAgentTrace_Agentic(t *testing.T) {
	events := []chat.TrajectoryEvent{
		{Stage: "hop", Step: 1},
		{Stage: "hop", Step: 2},
		{Stage: "answer"},
	}
	got := BuildAgentTrace(OrchestratorAgentic, "complex_reasoning_agentic_gate", events, PlanShapeInputs{})
	if got.Hops != 2 {
		t.Fatalf("Hops = %d, want 2", got.Hops)
	}
	if got.Plan != nil {
		t.Fatalf("Plan must be nil for agentic; got %+v", *got.Plan)
	}
}

func TestBuildAgentTrace_Standard(t *testing.T) {
	// Standard branch typically passes no events (the standard path
	// doesn't go through CollectEmit). The trace records only the
	// orchestrator label + dispatch reason.
	got := BuildAgentTrace(OrchestratorStandard, "fallback_query_type_lookup", nil, PlanShapeInputs{})
	if got.Orchestrator != OrchestratorStandard {
		t.Fatalf("orchestrator: got %q", got.Orchestrator)
	}
	if got.DispatchReason != "fallback_query_type_lookup" {
		t.Fatalf("DispatchReason: got %q", got.DispatchReason)
	}
	if got.Plan != nil || got.Hops != 0 || got.Specialist != "" {
		t.Fatalf("standard trace must not populate orchestrator-specific fields; got %+v", got)
	}
}

// fakeClassifierAIConfigStore is a minimal ai.ConfigStore backing a real
// *ai.ConfigResolver that talks to an httptest server — needed because
// OrchestratorDispatchAdapter.Search classifies the query type (and thus
// picks the orchestrator) through the real ai.ClassifyQueryComplexity call,
// not through Question.QueryType.
type fakeClassifierAIConfigStore struct {
	baseURL string
	model   string
}

func (f fakeClassifierAIConfigStore) GetActiveAIProvider(context.Context) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{ID: "prov-test", Name: "Test", APIKey: "test-key", BaseURL: f.baseURL}, nil
}

func (f fakeClassifierAIConfigStore) GetAIProviderByID(context.Context, string) (*ai.AIProviderInfo, error) {
	return nil, nil
}

func (f fakeClassifierAIConfigStore) GetAIModelsByProvider(context.Context, string) ([]ai.AIModelInfo, error) {
	return []ai.AIModelInfo{{Name: f.model}}, nil
}

func (f fakeClassifierAIConfigStore) GetKBModelOverrides(context.Context, string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

// fakeOneChunkSearcher is a minimal vector.Searcher stand-in that returns
// one chunk. RunSupervisorChat hard-fails ("no chunks from specialist ...")
// on an empty result, which would push the test below through the
// standard-path fallback (a.prod.Search) — itself also carrying the
// tabular router via WithTabularRouter — and double-count the router
// invocation, masking a mutation that removes the Supervisor branch's own
// wiring. Returning one chunk here keeps the assertions isolated to the
// Supervisor branch.
type fakeOneChunkSearcher struct{}

func (fakeOneChunkSearcher) Search(context.Context, string, string, int, vector.SearchOptions) (*vector.SearchResult, error) {
	return &vector.SearchResult{Chunks: []vector.SearchChunk{
		{ID: "c1", Content: "content", FileID: "f1", FileName: "f.txt", Score: 1},
	}}, nil
}

func (fakeOneChunkSearcher) ExpandNeighbors(_ context.Context, chunks []vector.SearchChunk, _ int, _, _ string) []vector.SearchChunk {
	return chunks
}

// alwaysComplexServer starts an httptest server whose /chat/completions
// handler always answers as if the query classifier judged the query
// complex — good enough for every LLM call this test's dispatch path makes
// (query-complexity classification, and whatever the Supervisor's own
// specialist/answer path calls afterward; none of those responses are
// asserted on here).
func alwaysComplexServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `{"isComplex":true}`}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestOrchestratorDispatchAdapter_SupervisorFiresTabularRouterWithPerKBConfig
// is the task-14 finding-1 regression guard: OrchestratorDispatchAdapter's
// Supervisor branch built chat.SupervisorChatParams without TabularRouter /
// TabularRouterConfig at all, so a --production-context --orchestrator-
// dispatch=true run (the eval's default supervisor-in-prod mode) silently
// never exercised the tabular router — measured fire rate 0.767 in Run 2
// even though the router itself works (0 validator rejections once fired).
//
// Also proves the per-KB chat_tabular_router_max_rows override reaches the
// router (RowCap), mirroring
// TestRunTrajectory_SupervisorHonoursPerKBTabularRouterConfig in
// tabular_router_option_test.go: the router's own wiring-time cfgFn reports
// a "wrong" global MaxRows of 200, and only a.siteCfg's override (777) must
// win.
//
// Mutation guard: deleting `TabularRouter: a.prod.tabularRouter` and/or the
// `TabularRouterConfig: tabularCfg` field (and its resolution) from the
// OrchestratorSupervisor case in orchestrator_adapter.go's Search method
// makes this red — genCalls stays 0 and/or RowCap reads 200.
func TestOrchestratorDispatchAdapter_SupervisorFiresTabularRouterWithPerKBConfig(t *testing.T) {
	srv := alwaysComplexServer(t)
	aiResolver := ai.NewConfigResolver(fakeClassifierAIConfigStore{baseURL: srv.URL, model: "test-model"})

	var genCalls int
	exec := &evalFakeTabularExec{}
	router := chat.NewTabularRouter(
		evalFakeTabularCat{entry: evalTabularCatalogEntry()},
		exec,
		newEvalFakeTabularGen(&genCalls),
		// The router's own wiring-time cfgFn: the "wrong" global config a
		// per-KB siteCfg override must beat.
		func(context.Context) chat.TabularRouterConfig {
			cfg := evalFiringTabularRouterConfig()
			cfg.MaxRows = 200
			return cfg
		},
	)

	siteCfg := &stubSiteCfg{values: map[string]string{
		"chat_supervisor_enabled":      "true",
		"chat_tabular_query_enabled":   "true",
		"chat_tabular_router_enabled":  "true",
		"chat_tabular_router_max_rows": "777",
	}}

	a := NewOrchestratorDispatchAdapter(
		aiResolver,
		fakeOneChunkSearcher{},
		siteCfg,
		nil,
		EvalFlags{},
		WithTabularRouter(router),
	)

	q := Question{
		ID: "q1", KbID: "kb1", Language: "de",
		// "Vergleiche" trips the heuristic complexity marker (forcing the
		// LLM classification call rather than a heuristic short-circuit),
		// "wie viele" is the router's own aggregation cue (same query
		// shape TestRunTrajectory_SupervisorWiresTabularRouter uses).
		Question: "Vergleiche: wie viele Gebäude gibt es insgesamt?",
	}

	if _, err := a.Search(context.Background(), q, 5); err != nil {
		t.Fatalf("a.Search: %v (want the Supervisor branch to succeed with fakeOneChunkSearcher, not fall back to the standard path)", err)
	}

	if genCalls != 1 {
		t.Fatalf("tabular SQL generator calls = %d, want 1 — the Supervisor dispatch never wired the tabular router", genCalls)
	}
	if exec.calls != 1 {
		t.Fatalf("tabular executor calls = %d, want 1", exec.calls)
	}
	if exec.lastOpts.RowCap != 777 {
		t.Fatalf("executor RowCap = %d, want 777 (a.siteCfg's per-KB override) — "+
			"the adapter fell back to the router's own wiring-time cfgFn (RowCap 200) instead",
			exec.lastOpts.RowCap)
	}
}
