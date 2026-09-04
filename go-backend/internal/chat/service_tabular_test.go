package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/tabular/sqlexec"
	"github.com/justrag/go-backend/internal/vector"
)

// tabRecordingSearcher satisfies vector.Searcher and records what the
// retrieval stage actually received — the query string and the forced
// keyword-arm flag are exactly the two retrieval hints the tabular router
// is supposed to hand down.
type tabRecordingSearcher struct {
	gotQuery string
	gotForce bool
	chunks   []vector.SearchChunk
}

func (s *tabRecordingSearcher) Search(_ context.Context, _, query string, _ int, opts vector.SearchOptions) (*vector.SearchResult, error) {
	s.gotQuery = query
	s.gotForce = opts.ForceBM25SimpleArm
	return &vector.SearchResult{Chunks: s.chunks}, nil
}

func (s *tabRecordingSearcher) ExpandNeighbors(_ context.Context, chunks []vector.SearchChunk, _ int, _, _ string) []vector.SearchChunk {
	return chunks
}

func tabTestChunks() []vector.SearchChunk {
	return []vector.SearchChunk{
		{ID: "c1", FileID: "f1", FileName: "a.pdf", Content: "alpha", Score: 0.9, VectorScore: 0.9},
		{ID: "c2", FileID: "f1", FileName: "a.pdf", Content: "beta", Score: 0.8, VectorScore: 0.8},
		{ID: "c3", FileID: "f2", FileName: "b.pdf", Content: "gamma", Score: 0.7, VectorScore: 0.7},
	}
}

// tabFiringRouter wires the Task-4 fakes into a router that fires and
// returns a row addendum for the "Fläche von Raum <id>" question.
func tabFiringRouter() *TabularRouter {
	cat := &fakeCat{has: true, entries: []tabular.CatalogEntry{testTabularEntry()}}
	gen := &fakeGen{steps: []genStep{{prop: sqlProp(`SELECT flaeche FROM tabular.gebaeude LIMIT 5`)}}}
	ex := &fakeExec{results: []execStep{{res: &sqlexec.Result{
		Columns: []string{"flaeche"}, Rows: []map[string]any{{"flaeche": 42}}, RowCount: 1,
	}}}}
	return newTestRouter(cat, ex, gen.fn, testTabularCfg(), nil)
}

const tabTestQuery = `Fläche von Raum 01.1440.055_.10`
const tabTestPromoted = `Fläche von Raum "01.1440.055_.10"`

func TestPrepareChatContext_TabularRouterAppliesHintsAndAddendum(t *testing.T) {
	searcher := &tabRecordingSearcher{chunks: tabTestChunks()}
	var evs []map[string]any

	chatCtx, err := PrepareChatContext(context.Background(), nil, searcher, nil, ChatContextParams{
		KbID:          "kb1",
		SearchQuery:   tabTestQuery,
		Language:      "de",
		TabularRouter: tabFiringRouter(),
		Emit:          collectEvents(&evs),
	})
	if err != nil {
		t.Fatalf("PrepareChatContext: %v", err)
	}

	// Retrieval hints reached the searcher.
	if searcher.gotQuery != tabTestPromoted {
		t.Fatalf("search query = %q, want the id-quoted promotion %q", searcher.gotQuery, tabTestPromoted)
	}
	if !searcher.gotForce {
		t.Fatalf("SearchOptions.ForceBM25SimpleArm = false, want true for a KB with tabular data")
	}

	// Trace surfaced on the ChatContext.
	if chatCtx.TabularTrace == nil || chatCtx.TabularTrace.Outcome != "fired_ok" {
		t.Fatalf("TabularTrace = %+v, want Outcome fired_ok", chatCtx.TabularTrace)
	}

	// Addendum injected BEFORE the CONTEXT block.
	marker := "\n\nCONTEXT:\n"
	addIdx := strings.Index(chatCtx.SystemPrompt, "TABELLENABFRAGE")
	ctxIdx := strings.Index(chatCtx.SystemPrompt, marker)
	if addIdx < 0 {
		t.Fatalf("system prompt is missing the tabular addendum:\n%s", chatCtx.SystemPrompt)
	}
	if ctxIdx < 0 {
		t.Fatalf("system prompt is missing the CONTEXT marker")
	}
	if addIdx > ctxIdx {
		t.Fatalf("tabular addendum at %d must come before CONTEXT at %d", addIdx, ctxIdx)
	}

	// The router's events flowed through the path's Emit.
	types := eventTypes(evs)
	found := false
	for _, tp := range types {
		if tp == "tabular_router_fired" {
			found = true
		}
	}
	if !found {
		t.Fatalf("router events not forwarded through params.Emit: %v", types)
	}

	// The user-visible query is untouched: EnhancedQuery reporting stays
	// on the original phrasing (the fake searcher returns none, so it is
	// empty, but the params were never mutated).
	if chatCtx.EnhancedQuery != "" {
		t.Fatalf("EnhancedQuery = %q, want empty (fake searcher sets none)", chatCtx.EnhancedQuery)
	}
}

// TestPrepareChatContext_NilTabularRouterUnchanged is the regression guard:
// a deployment without a router (no read-only DSN, publicapi / openaicompat
// / mcpserver callers) must produce the exact prompt it produced before the
// hook existed.
func TestPrepareChatContext_NilTabularRouterUnchanged(t *testing.T) {
	searcher := &tabRecordingSearcher{chunks: tabTestChunks()}

	chatCtx, err := PrepareChatContext(context.Background(), nil, searcher, nil, ChatContextParams{
		KbID:        "kb1",
		SearchQuery: tabTestQuery,
		Language:    "de",
	})
	if err != nil {
		t.Fatalf("PrepareChatContext: %v", err)
	}
	if searcher.gotQuery != tabTestQuery {
		t.Fatalf("search query = %q, want the original %q", searcher.gotQuery, tabTestQuery)
	}
	if searcher.gotForce {
		t.Fatalf("ForceBM25SimpleArm must stay false without a router")
	}
	if chatCtx.TabularTrace != nil {
		t.Fatalf("TabularTrace = %+v, want nil without a router", chatCtx.TabularTrace)
	}
	want := prompts.ChatSystemPromptWithDate("de", "") + "\n\nCONTEXT:\n" + chatCtx.Context
	if chatCtx.SystemPrompt != want {
		t.Fatalf("system prompt drifted from the pre-hook assembly:\ngot:  %q\nwant: %q",
			chatCtx.SystemPrompt, want)
	}
}
