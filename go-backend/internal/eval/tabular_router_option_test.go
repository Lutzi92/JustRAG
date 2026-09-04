package eval

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
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
