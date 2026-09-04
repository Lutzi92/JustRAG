package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

// tabSupSearcher records the query and the forced keyword-arm flag the
// specialist's SearchOptions carried — i.e. what agents.Input forwarded.
type tabSupSearcher struct {
	gotQuery string
	gotForce bool
}

func (s *tabSupSearcher) Search(_ context.Context, _, query string, _ int, opts vector.SearchOptions) (*vector.SearchResult, error) {
	s.gotQuery = query
	s.gotForce = opts.ForceBM25SimpleArm
	return &vector.SearchResult{Chunks: []vector.SearchChunk{
		supChunk("A", 0.9), supChunk("B", 0.8), supChunk("C", 0.7),
	}}, nil
}

func TestRunSupervisorChat_TabularRouterHintsAndAddendum(t *testing.T) {
	s := &tabSupSearcher{}
	var evs []map[string]any

	ctxOut, err := runSupervisorChatTestable(
		context.Background(),
		nil,
		s,
		func(_, _ string) bool { return false }, // retriever arm
		SupervisorChatParams{
			KbID:          "kb1",
			Query:         tabTestQuery,
			Language:      "de",
			TabularRouter: tabFiringRouter(),
		},
		collectEvents(&evs),
	)
	if err != nil {
		t.Fatalf("runSupervisorChatTestable: %v", err)
	}

	if s.gotQuery != tabTestPromoted {
		t.Fatalf("specialist query = %q, want the id-quoted promotion %q", s.gotQuery, tabTestPromoted)
	}
	if !s.gotForce {
		t.Fatalf("agents.Input did not forward ForceBM25SimpleArm into SearchOptions")
	}
	if ctxOut.TabularTrace == nil || ctxOut.TabularTrace.Outcome != "fired_ok" {
		t.Fatalf("TabularTrace = %+v, want Outcome fired_ok", ctxOut.TabularTrace)
	}

	addIdx := strings.Index(ctxOut.SystemPrompt, "TABELLENABFRAGE")
	ctxIdx := strings.Index(ctxOut.SystemPrompt, "\n\nCONTEXT:\n")
	if addIdx < 0 {
		t.Fatalf("supervisor system prompt is missing the tabular addendum:\n%s", ctxOut.SystemPrompt)
	}
	if addIdx > ctxIdx {
		t.Fatalf("tabular addendum at %d must come before CONTEXT at %d", addIdx, ctxIdx)
	}

	found := false
	for _, tp := range eventTypes(evs) {
		if tp == "tabular_router_fired" {
			found = true
		}
	}
	if !found {
		t.Fatalf("router events not forwarded through the supervisor's emit: %v", eventTypes(evs))
	}
}

func TestRunSupervisorChat_NilTabularRouterUnchanged(t *testing.T) {
	s := &tabSupSearcher{}
	ctxOut, err := runSupervisorChatTestable(
		context.Background(),
		nil,
		s,
		func(_, _ string) bool { return false },
		SupervisorChatParams{KbID: "kb1", Query: tabTestQuery, Language: "de"},
		func(map[string]any) {},
	)
	if err != nil {
		t.Fatalf("runSupervisorChatTestable: %v", err)
	}
	if s.gotQuery != tabTestQuery {
		t.Fatalf("specialist query = %q, want the original %q", s.gotQuery, tabTestQuery)
	}
	if s.gotForce {
		t.Fatalf("ForceBM25SimpleArm must stay false without a router")
	}
	if ctxOut.TabularTrace != nil {
		t.Fatalf("TabularTrace = %+v, want nil without a router", ctxOut.TabularTrace)
	}
	if strings.Contains(ctxOut.SystemPrompt, "TABELLENABFRAGE") {
		t.Fatalf("no router ⇒ no tabular addendum")
	}
}
