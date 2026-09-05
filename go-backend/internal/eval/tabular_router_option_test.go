package eval

import (
	"context"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/tabular/sqlexec"
)

// TestOrchestratorDispatchAdapterCarriesTabularRouter is the R51 regression
// guard. cmd/eval builds the tabular router once and hands it to
// NewOrchestratorDispatchAdapter — the DEFAULT --production-context mode.
// If the variadic option is not forwarded to the embedded production
// adapter, the router silently disappears from every default eval run (it
// used to be wired only into the --orchestrator-dispatch=false branch, which
// must stay router-free for byte-stable historical diffs).
func TestOrchestratorDispatchAdapterCarriesTabularRouter(t *testing.T) {
	// A router with no dependencies is enough: Run is nil-safe and skips,
	// which still produces a TabularTrace — that trace is what proves the
	// router reached chat.ChatContextParams.
	router := chat.NewTabularRouter(nil, nil, nil, nil)

	a := NewOrchestratorDispatchAdapter(
		nil,
		fakeBaseSearcher{},
		&stubSiteCfg{values: map[string]string{}},
		nil,
		EvalFlags{},
		WithTabularRouter(router),
	)
	if a.prod == nil {
		t.Fatal("dispatch adapter has no embedded production adapter")
	}
	if a.prod.tabularRouter != router {
		t.Fatal("NewOrchestratorDispatchAdapter dropped the tabular router option")
	}

	// End-to-end through the standard-fallback route: the production
	// adapter must put the router on the ChatContextParams, not just hold
	// it in a field.
	if _, err := a.prod.Search(context.Background(), Question{ID: "q1", KbID: "kb1", Question: "Q?"}, 5); err != nil {
		t.Fatalf("prod.Search: %v", err)
	}
	chatCtx, ok := a.prod.ChatContextForQuestion("q1")
	if !ok {
		t.Fatal("no cached ChatContext for q1")
	}
	if chatCtx.TabularTrace == nil {
		t.Fatal("the router never ran: ChatContextParams.TabularRouter was not set")
	}
}

// TestProductionContextAdapterWithoutRouter is the negative half: the
// byte-stable branch (no option) must leave the router unset, so the trace
// is absent and the pipeline is exactly the pre-router one.
func TestProductionContextAdapterWithoutRouter(t *testing.T) {
	a := NewProductionContextAdapter(nil, fakeBaseSearcher{}, &stubSiteCfg{values: map[string]string{}}, EvalFlags{})
	if a.tabularRouter != nil {
		t.Fatal("no option ⇒ no router")
	}
	if _, err := a.Search(context.Background(), Question{ID: "q1", KbID: "kb1", Question: "Q?"}, 5); err != nil {
		t.Fatalf("Search: %v", err)
	}
	chatCtx, ok := a.ChatContextForQuestion("q1")
	if !ok {
		t.Fatal("no cached ChatContext for q1")
	}
	if chatCtx.TabularTrace != nil {
		t.Fatalf("TabularTrace = %+v, want nil without a router", chatCtx.TabularTrace)
	}
}

// --- Carry 3 (task-7-brief): RunTrajectory must wire the tabular router the
// same way the production Supervisor and standard ("off") paths do. -------

// evalFakeTabularCat / evalFakeTabularExec / newEvalFakeTabularGen mirror
// internal/chat's own tabular_router_test.go fakes, re-implemented here
// against the exported chat.TabularCatalogReader / sqlexec.Executor /
// chat.TabularSQLGenerator surfaces (those fakes are unexported to package
// chat). Just enough to prove the router actually ran an SQL-generation
// call — the one externally observable side effect that doesn't depend on
// RunTrajectory's emit plumbing: the router's own raw events
// (tabular_router_fired, tabular_router_sql, ...) use a bare map shape that
// CollectEmit silently drops (it only keeps events wrapped as
// {"agentTrajectory": chat.TrajectoryEvent{...}}), so a generator call
// count is the only way to see the router ran from outside package chat.
type evalFakeTabularCat struct{ entry tabular.CatalogEntry }

func (f evalFakeTabularCat) HasDataForKB(context.Context, string) (bool, error) { return true, nil }
func (f evalFakeTabularCat) ListByKB(context.Context, string) ([]tabular.CatalogEntry, error) {
	return []tabular.CatalogEntry{f.entry}, nil
}
func (f evalFakeTabularCat) LookupValues(context.Context, []tabular.CatalogEntry, []string, int) ([]tabular.ValueHit, error) {
	return nil, nil
}

func evalTabularCatalogEntry() tabular.CatalogEntry {
	return tabular.CatalogEntry{
		KBID: "kb1", FileID: "f1", FileName: "Gebäudeliste.xlsx",
		SheetName: "Gebäudeliste", TableName: "gebaeude", SheetKind: "table",
		RowCount: 10,
		Columns: []tabular.ColumnSpec{
			{Original: "Liegenschaft", Name: "liegenschaft", Type: tabular.TypeText},
		},
	}
}

type evalFakeTabularExec struct{ calls int }

func (f *evalFakeTabularExec) Execute(context.Context, string, sqlexec.Options) (*sqlexec.Result, error) {
	f.calls++
	return &sqlexec.Result{Columns: []string{"n"}, Rows: []map[string]any{{"n": 4}}, RowCount: 1}, nil
}

// newEvalFakeTabularGen returns a TabularSQLGenerator that always proposes
// the same trivial statement and increments calls — the signal the tests
// below assert on.
func newEvalFakeTabularGen(calls *int) chat.TabularSQLGenerator {
	return func(context.Context, ai.TabularSQLRequest, string, string) (ai.TabularSQLProposal, error) {
		*calls++
		sql := `SELECT count(*) AS n FROM tabular.gebaeude LIMIT 5`
		return ai.TabularSQLProposal{SQL: &sql, Rationale: "because", Confidence: 0.9}, nil
	}
}

func evalFiringTabularRouterConfig() chat.TabularRouterConfig {
	return chat.TabularRouterConfig{
		Enabled: true, Model: "fast-model", MaxRows: 200, MaxRepairs: 1,
		Timeout: 5 * time.Second, SchemaMaxTokens: 12000,
	}
}

// TestRunTrajectory_SupervisorWiresTabularRouter is the Carry 3 guard for
// TrajectoryModeSupervisor. Mutation guard: dropping
// `TabularRouter: deps.TabularRouter` from the chat.SupervisorChatParams
// literal in trajectory_runner.go's TrajectoryModeSupervisor case makes
// this red — the generator is never called because
// SupervisorChatParams.TabularRouter stays nil and RunSupervisorChat's
// `if params.TabularRouter != nil` guard skips the router entirely.
func TestRunTrajectory_SupervisorWiresTabularRouter(t *testing.T) {
	var genCalls int
	exec := &evalFakeTabularExec{}
	router := chat.NewTabularRouter(
		evalFakeTabularCat{entry: evalTabularCatalogEntry()},
		exec,
		newEvalFakeTabularGen(&genCalls),
		func(context.Context) chat.TabularRouterConfig { return evalFiringTabularRouterConfig() },
	)

	deps := TrajectoryRunDeps{
		SearchService: fakeBaseSearcher{},
		TabularRouter: router,
	}
	q := Question{ID: "q1", KbID: "kb1", Question: "Wie viele Gebäude gibt es?", Language: "de"}

	RunTrajectory(context.Background(), deps, q, TrajectoryModeSupervisor)

	if genCalls != 1 {
		t.Fatalf("tabular SQL generator calls = %d, want 1 — the router was not wired into the Supervisor trajectory path", genCalls)
	}
	if exec.calls != 1 {
		t.Fatalf("tabular executor calls = %d, want 1", exec.calls)
	}
}

// TestRunTrajectory_OffWiresTabularRouter is the Carry 3 guard for
// TrajectoryModeOff (chat.ChatContextParams, the standard path).
// deps.SiteReader is left nil deliberately: PrepareChatContext only
// resolves TabularRouterInput.Config from siteConfig when it is non-nil,
// so leaving it nil forces the router through its own wiring-time cfgFn —
// the one this test controls — the same way TestRunTrajectory_
// SupervisorWiresTabularRouter controls it for the Supervisor case.
//
// Mutation guard: dropping `TabularRouter: deps.TabularRouter` from the
// chat.ChatContextParams literal in trajectory_runner.go's TrajectoryModeOff
// case makes this red.
func TestRunTrajectory_OffWiresTabularRouter(t *testing.T) {
	var genCalls int
	exec := &evalFakeTabularExec{}
	router := chat.NewTabularRouter(
		evalFakeTabularCat{entry: evalTabularCatalogEntry()},
		exec,
		newEvalFakeTabularGen(&genCalls),
		func(context.Context) chat.TabularRouterConfig { return evalFiringTabularRouterConfig() },
	)

	deps := TrajectoryRunDeps{
		SearchService: fakeBaseSearcher{},
		TabularRouter: router,
	}
	q := Question{ID: "q1", KbID: "kb1", Question: "Wie viele Gebäude gibt es?", Language: "de"}

	RunTrajectory(context.Background(), deps, q, TrajectoryModeOff)

	if genCalls != 1 {
		t.Fatalf("tabular SQL generator calls = %d, want 1 — the router was not wired into the standard (off) trajectory path", genCalls)
	}
	if exec.calls != 1 {
		t.Fatalf("tabular executor calls = %d, want 1", exec.calls)
	}
}
