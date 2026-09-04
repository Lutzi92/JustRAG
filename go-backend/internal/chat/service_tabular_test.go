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

// TestPrepareChatContext_TabularRouterNoTablesUnchanged is the case that
// covers every non-spreadsheet KB in a deployment where the router IS wired:
// the gate answers "no tabular data" and the turn must be indistinguishable
// from the pre-hook pipeline — original query, no forced arm, byte-identical
// prompt. Only the trace records that the router looked.
func TestPrepareChatContext_TabularRouterNoTablesUnchanged(t *testing.T) {
	searcher := &tabRecordingSearcher{chunks: tabTestChunks()}
	cat := &fakeCat{has: false}
	router := newTestRouter(cat, &fakeExec{}, (&fakeGen{}).fn, testTabularCfg(), nil)

	chatCtx, err := PrepareChatContext(context.Background(), nil, searcher, nil, ChatContextParams{
		KbID:          "kb1",
		SearchQuery:   tabTestQuery,
		Language:      "de",
		TabularRouter: router,
	})
	if err != nil {
		t.Fatalf("PrepareChatContext: %v", err)
	}
	if searcher.gotQuery != tabTestQuery {
		t.Fatalf("search query = %q, want the original %q", searcher.gotQuery, tabTestQuery)
	}
	if searcher.gotForce {
		t.Fatalf("ForceBM25SimpleArm must stay false on a KB with no tables")
	}
	if chatCtx.TabularTrace == nil || chatCtx.TabularTrace.Outcome != "skipped_no_tables" {
		t.Fatalf("TabularTrace = %+v, want Outcome skipped_no_tables", chatCtx.TabularTrace)
	}
	want := prompts.ChatSystemPromptWithDate("de", "") + "\n\nCONTEXT:\n" + chatCtx.Context
	if chatCtx.SystemPrompt != want {
		t.Fatalf("a no-tables KB must get the pre-hook prompt:\ngot:  %q\nwant: %q",
			chatCtx.SystemPrompt, want)
	}
}

// TestPrepareChatContext_TabularRouterSkipsPromotionUnderEnhance guards R50:
// under an explicit Enhance mode the search service rewrites/expands the
// query it is given and persists the outcome as messages.enhanced_query, so
// handing it the router's quoted phrasing would surface router-inserted
// quotes to the user. The other two hints must survive.
func TestPrepareChatContext_TabularRouterSkipsPromotionUnderEnhance(t *testing.T) {
	searcher := &tabRecordingSearcher{chunks: tabTestChunks()}

	chatCtx, err := PrepareChatContext(context.Background(), nil, searcher, nil, ChatContextParams{
		KbID:          "kb1",
		SearchQuery:   tabTestQuery,
		Language:      "de",
		Enhance:       "rewrite",
		TabularRouter: tabFiringRouter(),
	})
	if err != nil {
		t.Fatalf("PrepareChatContext: %v", err)
	}
	if searcher.gotQuery != tabTestQuery {
		t.Fatalf("search query = %q, want the ORIGINAL %q under Enhance", searcher.gotQuery, tabTestQuery)
	}
	if !searcher.gotForce {
		t.Fatalf("the forced keyword arm must survive an Enhance mode")
	}
	if !strings.Contains(chatCtx.SystemPrompt, "TABELLENABFRAGE") {
		t.Fatalf("the SQL addendum must survive an Enhance mode")
	}
}

// TestPrepareChatContext_TabularRouterUsesPerKBConfig guards R49: the chat
// handler overlays its SiteConfigReader per KB, so the config the router
// obeys must be the one resolved from THIS request's reader — not the
// wiring-time cfgFn, which closes over the global reader only.
func TestPrepareChatContext_TabularRouterUsesPerKBConfig(t *testing.T) {
	// cfgFn says "enabled"; the per-KB reader says the master flag is off.
	// The reader must win, or a per-KB kill switch is a silent no-op.
	searcher := &tabRecordingSearcher{chunks: tabTestChunks()}
	reader := stubReader{vals: map[string]string{"chat_tabular_query_enabled": "false"}}

	chatCtx, err := PrepareChatContext(context.Background(), nil, searcher, reader, ChatContextParams{
		KbID:          "kb1",
		SearchQuery:   tabTestQuery,
		Language:      "de",
		TabularRouter: tabFiringRouter(),
	})
	if err != nil {
		t.Fatalf("PrepareChatContext: %v", err)
	}
	if chatCtx.TabularTrace == nil || chatCtx.TabularTrace.Outcome != "skipped_disabled" {
		t.Fatalf("TabularTrace = %+v, want skipped_disabled from the per-KB reader", chatCtx.TabularTrace)
	}
	if searcher.gotQuery != tabTestQuery || searcher.gotForce {
		t.Fatalf("a disabled router must leave retrieval untouched (query %q, force %v)",
			searcher.gotQuery, searcher.gotForce)
	}

	// The mirror image: the same cfgFn, a reader that switches both flags
	// on — the router fires.
	searcher2 := &tabRecordingSearcher{chunks: tabTestChunks()}
	on := stubReader{vals: map[string]string{
		"chat_tabular_query_enabled":  "true",
		"chat_tabular_router_enabled": "true",
	}}
	chatCtx2, err := PrepareChatContext(context.Background(), nil, searcher2, on, ChatContextParams{
		KbID:          "kb1",
		SearchQuery:   tabTestQuery,
		Language:      "de",
		TabularRouter: tabFiringRouter(),
	})
	if err != nil {
		t.Fatalf("PrepareChatContext: %v", err)
	}
	if chatCtx2.TabularTrace == nil || chatCtx2.TabularTrace.Outcome != "fired_ok" {
		t.Fatalf("TabularTrace = %+v, want fired_ok when the per-KB reader enables it", chatCtx2.TabularTrace)
	}
	if searcher2.gotQuery != tabTestPromoted {
		t.Fatalf("search query = %q, want %q", searcher2.gotQuery, tabTestPromoted)
	}
}
