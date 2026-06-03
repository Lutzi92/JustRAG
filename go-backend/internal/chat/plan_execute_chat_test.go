package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

// fakeSearcher records every Search call and returns canned chunk lists.
type fakeSearcher struct {
	calls []searchCall
	resp  func(query string) []vector.SearchChunk
}

type searchCall struct {
	query      string
	subQueries []string
}

func (f *fakeSearcher) Search(ctx context.Context, kbID, query string, limit int, opts vector.SearchOptions) (*vector.SearchResult, error) {
	f.calls = append(f.calls, searchCall{query: query, subQueries: append([]string(nil), opts.SubQueries...)})
	if f.resp == nil {
		return &vector.SearchResult{}, nil
	}
	return &vector.SearchResult{Chunks: f.resp(query)}, nil
}

func chunk(id, file string) vector.SearchChunk {
	return vector.SearchChunk{ID: id, FileID: file, FileName: file, Content: id + " body", Score: 1.0}
}

func TestRunPlanExecuteChat_PlanThenAnswer(t *testing.T) {
	plan := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, lang, model string) ([]string, string, error) {
		return []string{"sub1", "sub2"}, "decomposed", nil
	}
	iter := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, summaries string, round int, lang, model string) (string, []string, string, error) {
		return "answer", nil, "sufficient", nil
	}
	searcher := &fakeSearcher{
		resp: func(query string) []vector.SearchChunk {
			return []vector.SearchChunk{chunk("c1", "f1")}
		},
	}
	emitted := []map[string]any{}
	emit := func(d map[string]any) { emitted = append(emitted, d) }

	cctx, err := runPlanExecuteChatTestable(context.Background(), nil, searcher, plan, nil, iter,
		PlanExecuteParams{
			KbID: "kb", Query: "Q?", Language: "en",
			MaxSubQueries: 3, MaxIterations: 3, TokenBudget: 8000,
		}, emit)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cctx == nil {
		t.Fatalf("nil context returned")
	}
	if len(searcher.calls) != 1 {
		t.Errorf("expected exactly 1 search call (Plan stage with SubQueries); got %d (%#v)", len(searcher.calls), searcher.calls)
	}
	if got := searcher.calls[0].subQueries; len(got) != 2 || got[0] != "sub1" || got[1] != "sub2" {
		t.Errorf("Plan stage SubQueries: want [sub1 sub2], got %#v", got)
	}
}

// TestRunPlanExecuteChat_PopulatesFinalChunks guards the eval-harness
// contract — see the supervisor test of the same name.
func TestRunPlanExecuteChat_PopulatesFinalChunks(t *testing.T) {
	plan := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, lang, model string) ([]string, string, error) {
		return []string{"sub1", "sub2"}, "decomposed", nil
	}
	iter := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, summaries string, round int, lang, model string) (string, []string, string, error) {
		return "answer", nil, "sufficient", nil
	}
	searcher := &fakeSearcher{
		resp: func(query string) []vector.SearchChunk {
			return []vector.SearchChunk{chunk("c1", "f1")}
		},
	}

	cctx, err := runPlanExecuteChatTestable(context.Background(), nil, searcher, plan, nil, iter,
		PlanExecuteParams{
			KbID: "kb", Query: "Q?", Language: "en",
			MaxSubQueries: 3, MaxIterations: 3, TokenBudget: 8000,
		}, func(map[string]any) {})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(cctx.FinalChunks) != len(cctx.Sources) || len(cctx.FinalChunks) == 0 {
		t.Fatalf("FinalChunks must mirror Sources and be non-empty: got %d chunks vs %d sources",
			len(cctx.FinalChunks), len(cctx.Sources))
	}
}

func TestRunPlanExecuteChat_PlanFailureFallsBackToSingleQuery(t *testing.T) {
	plan := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, lang, model string) ([]string, string, error) {
		return nil, "", errors.New("LLM down")
	}
	iter := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, summaries string, round int, lang, model string) (string, []string, string, error) {
		return "answer", nil, "ok", nil
	}
	searcher := &fakeSearcher{
		resp: func(q string) []vector.SearchChunk { return []vector.SearchChunk{chunk("c1", "f1")} },
	}
	cctx, err := runPlanExecuteChatTestable(context.Background(), nil, searcher, plan, nil, iter,
		PlanExecuteParams{KbID: "kb", Query: "Q?", Language: "en", MaxSubQueries: 3, MaxIterations: 3, TokenBudget: 8000},
		func(map[string]any) {})
	if err != nil {
		t.Fatalf("err: %v (Plan failure should be fail-open)", err)
	}
	if cctx == nil {
		t.Fatalf("nil context returned despite fail-open")
	}
	if len(searcher.calls) != 1 {
		t.Errorf("plan-failure fallback: want 1 search call (single-query baseline), got %d", len(searcher.calls))
	}
	if len(searcher.calls[0].subQueries) != 0 {
		t.Errorf("fallback search must NOT carry SubQueries, got %#v", searcher.calls[0].subQueries)
	}
}

func TestRunPlanExecuteChat_EmptyPlanFallsBackToSingleQuery(t *testing.T) {
	plan := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, lang, model string) ([]string, string, error) {
		return []string{}, "too vague", nil
	}
	iter := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, summaries string, round int, lang, model string) (string, []string, string, error) {
		return "answer", nil, "ok", nil
	}
	searcher := &fakeSearcher{
		resp: func(q string) []vector.SearchChunk { return []vector.SearchChunk{chunk("c1", "f1")} },
	}
	_, err := runPlanExecuteChatTestable(context.Background(), nil, searcher, plan, nil, iter,
		PlanExecuteParams{KbID: "kb", Query: "Q?", Language: "en", MaxSubQueries: 3, MaxIterations: 3, TokenBudget: 8000},
		func(map[string]any) {})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(searcher.calls) != 1 || len(searcher.calls[0].subQueries) != 0 {
		t.Errorf("empty plan should fall back to plain single-query search, got %#v", searcher.calls)
	}
}

func TestRunPlanExecuteChat_IterateAddsChunksThenAnswers(t *testing.T) {
	planCalled := 0
	iterCalls := 0
	plan := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, lang, model string) ([]string, string, error) {
		planCalled++
		return []string{"sub1"}, "ok", nil
	}
	iter := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, summaries string, round int, lang, model string) (string, []string, string, error) {
		iterCalls++
		if iterCalls == 1 {
			return "search", []string{"follow-up"}, "missing X", nil
		}
		return "answer", nil, "ok", nil
	}
	searcher := &fakeSearcher{
		resp: func(query string) []vector.SearchChunk {
			if query == "Q?" {
				return []vector.SearchChunk{chunk("c1", "f1")}
			}
			return []vector.SearchChunk{chunk("c2", "f2")}
		},
	}
	cctx, err := runPlanExecuteChatTestable(context.Background(), nil, searcher, plan, nil, iter,
		PlanExecuteParams{KbID: "kb", Query: "Q?", Language: "en", MaxSubQueries: 3, MaxIterations: 3, TokenBudget: 8000},
		func(map[string]any) {})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if planCalled != 1 {
		t.Errorf("plan called %d times, want 1", planCalled)
	}
	if iterCalls != 2 {
		t.Errorf("iter called %d times, want 2", iterCalls)
	}
	// 1 Plan search + 1 Iterate search = 2 total
	if len(searcher.calls) != 2 {
		t.Errorf("expected 2 searches (Plan + 1 follow-up), got %d", len(searcher.calls))
	}
	if cctx == nil || len(cctx.Sources) != 2 {
		t.Errorf("expected 2 sources accumulated, got %d", len(cctx.Sources))
	}
}

func TestRunPlanExecuteChat_NoProgressBreaksLoop(t *testing.T) {
	plan := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, lang, model string) ([]string, string, error) {
		return []string{"sub"}, "ok", nil
	}
	iter := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, summaries string, round int, lang, model string) (string, []string, string, error) {
		return "search", []string{"more"}, "still missing", nil
	}
	// Searcher always returns the same chunk → dedup drops everything after round 1.
	searcher := &fakeSearcher{
		resp: func(q string) []vector.SearchChunk { return []vector.SearchChunk{chunk("c1", "f1")} },
	}
	_, err := runPlanExecuteChatTestable(context.Background(), nil, searcher, plan, nil, iter,
		PlanExecuteParams{KbID: "kb", Query: "Q?", Language: "en", MaxSubQueries: 3, MaxIterations: 5, TokenBudget: 8000},
		func(map[string]any) {})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Plan + at most 2 no-progress rounds before bailing → 3 searches max.
	if len(searcher.calls) > 3 {
		t.Errorf("no-progress should bail after 2 streak; saw %d searches", len(searcher.calls))
	}
}

// fakeDispatcher records each call and returns canned chunks so we can
// assert that the plan-execute orchestrator routed through the MCP
// dispatcher (Phase 2 §2.1) when the Tools field is set.
type fakeDispatcher struct {
	calls []dispatchCall
	resp  func(toolName string, args json.RawMessage) []vector.SearchChunk
}

type dispatchCall struct {
	tool string
	args string
}

func (f *fakeDispatcher) Dispatch(_ context.Context, _, name string, args json.RawMessage) (DispatchedToolResult, error) {
	f.calls = append(f.calls, dispatchCall{tool: name, args: string(args)})
	chunks := []vector.SearchChunk{}
	if f.resp != nil {
		chunks = f.resp(name, args)
	}
	return DispatchedToolResult{Chunks: chunks}, nil
}

func TestRunPlanExecuteChat_RoutesThroughMCPDispatcherWhenSet(t *testing.T) {
	plan := func(_ context.Context, _ *ai.ConfigResolver, _, _, _, _ string) ([]string, string, error) {
		return []string{"sub1"}, "decomposed", nil
	}
	iterCount := 0
	iter := func(_ context.Context, _ *ai.ConfigResolver, _, _, _ string, _ int, _, _ string) (string, []string, string, error) {
		iterCount++
		if iterCount == 1 {
			return "search", []string{"follow-up"}, "needs more", nil
		}
		return "answer", nil, "ok", nil
	}
	// The legacy direct searcher serves the Plan stage; the dispatcher
	// is responsible for the Iterate-stage searches.
	searcher := &fakeSearcher{
		resp: func(_ string) []vector.SearchChunk { return []vector.SearchChunk{chunk("c-plan", "f-plan")} },
	}
	disp := &fakeDispatcher{
		resp: func(_ string, _ json.RawMessage) []vector.SearchChunk {
			return []vector.SearchChunk{chunk("c-iter", "f-iter")}
		},
	}

	cctx, err := runPlanExecuteChatTestable(context.Background(), nil, searcher, plan, nil, iter,
		PlanExecuteParams{
			KbID: "kb-test", Query: "Q?", Language: "en",
			MaxSubQueries: 3, MaxIterations: 3, TokenBudget: 8000,
			Tools: disp,
		},
		func(map[string]any) {})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cctx == nil || len(cctx.Sources) != 2 {
		t.Errorf("expected 2 accumulated sources (plan + iter), got %d", len(cctx.Sources))
	}
	if len(searcher.calls) != 1 {
		t.Errorf("plan stage should still call the legacy searcher exactly once; got %d", len(searcher.calls))
	}
	if len(disp.calls) != 1 {
		t.Fatalf("iterate should dispatch through MCP exactly once; got %d", len(disp.calls))
	}
	if disp.calls[0].tool != "kb_search" {
		t.Errorf("iterate dispatch tool = %q, want kb_search", disp.calls[0].tool)
	}
	if !contains(disp.calls[0].args, `"query":"follow-up"`) {
		t.Errorf("dispatch args should carry the iterate query, got %s", disp.calls[0].args)
	}
	if !contains(disp.calls[0].args, `"kb_id":"kb-test"`) {
		t.Errorf("dispatch args should carry kb_id, got %s", disp.calls[0].args)
	}
}

func contains(s, needle string) bool {
	return len(needle) == 0 || (len(s) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(s); i++ {
			if s[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})())
}

func TestRunPlanExecuteChat_MaxIterationsReached(t *testing.T) {
	plan := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, lang, model string) ([]string, string, error) {
		return []string{"sub"}, "ok", nil
	}
	round := 0
	iter := func(ctx context.Context, resolver *ai.ConfigResolver, kbID, q, summaries string, r int, lang, model string) (string, []string, string, error) {
		round++
		return "search", []string{"q" + strconv.Itoa(round)}, "more", nil
	}
	searcher := &fakeSearcher{
		// Each call returns a fresh chunk so dedup never trips no-progress.
		resp: func(q string) []vector.SearchChunk {
			return []vector.SearchChunk{chunk("c-"+q, "f-"+q)}
		},
	}
	_, err := runPlanExecuteChatTestable(context.Background(), nil, searcher, plan, nil, iter,
		PlanExecuteParams{KbID: "kb", Query: "Q?", Language: "en", MaxSubQueries: 3, MaxIterations: 2, TokenBudget: 8000},
		func(map[string]any) {})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Plan + 2 iterations = 3 searches (and no more, despite iter saying "search").
	if len(searcher.calls) != 3 {
		t.Errorf("max_iter_reached: want exactly 3 searches (Plan + 2 iter), got %d", len(searcher.calls))
	}
}
