package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/vector"
)

// fakeAgenticSearcher satisfies the searchInvoker interface used by
// RunAgenticChat for test isolation. Returns canned results per query.
type fakeAgenticSearcher struct {
	results map[string]*vector.SearchResult
	calls   []string // queries seen in order
	errOn   string   // when set, return error for this exact query
}

func (f *fakeAgenticSearcher) Search(_ context.Context, _, query string, _ int, _ vector.SearchOptions) (*vector.SearchResult, error) {
	f.calls = append(f.calls, query)
	if f.errOn != "" && f.errOn == query {
		return nil, errors.New("simulated search error")
	}
	if r, ok := f.results[query]; ok {
		return r, nil
	}
	return &vector.SearchResult{Chunks: []vector.SearchChunk{}}, nil
}

// stubCritiqueQueue lets the test control the critique decisions per-call.
type stubCritiqueQueue struct {
	responses []critiqueDecision
	idx       int
}

type critiqueDecision struct {
	sufficient bool
	followUp   string
	err        error
}

func (s *stubCritiqueQueue) next(_ context.Context, _ *ai.ConfigResolver, _, _, _, _, _ string) (bool, string, error) {
	if s.idx >= len(s.responses) {
		return false, "", errors.New("stubCritiqueQueue: out of canned responses")
	}
	r := s.responses[s.idx]
	s.idx++
	return r.sufficient, r.followUp, r.err
}

func makeChunk(id, fileID, content string) vector.SearchChunk {
	return vector.SearchChunk{
		ID:       id,
		FileID:   fileID,
		FileName: "f-" + fileID,
		Content:  content,
		Score:    0.9,
	}
}

func agenticOutcomeCount(outcome string) float64 {
	return testutil.ToFloat64(observability.AgenticDecisionTotalForTest().WithLabelValues(outcome))
}

func TestRunAgenticChat_SufficientAfterFirstHop(t *testing.T) {
	searcher := &fakeAgenticSearcher{
		results: map[string]*vector.SearchResult{
			"original": {Chunks: []vector.SearchChunk{makeChunk("c1", "f1", "answer is here")}},
		},
	}
	critique := &stubCritiqueQueue{responses: []critiqueDecision{
		{sufficient: true},
	}}

	beforeOutcome := agenticOutcomeCount("early_stop_sufficient")

	params := AgenticChatParams{
		KbID:     "kb1",
		Query:    "original",
		Language: "en",
		MaxHops:  3,
	}
	ctxResult, err := runAgenticChatTestable(context.Background(), nil, searcher, critique.next, params, func(map[string]any) {})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctxResult.Sources) != 1 {
		t.Errorf("sources count: got %d, want 1", len(ctxResult.Sources))
	}
	if len(searcher.calls) != 1 {
		t.Errorf("search calls: got %d, want 1", len(searcher.calls))
	}
	if got := agenticOutcomeCount("early_stop_sufficient") - beforeOutcome; got != 1 {
		t.Errorf("early_stop_sufficient counter delta: got %v, want 1", got)
	}
}

// TestRunAgenticChat_PopulatesFinalChunks guards the eval-harness
// contract — see the supervisor test of the same name.
func TestRunAgenticChat_PopulatesFinalChunks(t *testing.T) {
	searcher := &fakeAgenticSearcher{
		results: map[string]*vector.SearchResult{
			"original": {Chunks: []vector.SearchChunk{makeChunk("c1", "f1", "answer is here")}},
		},
	}
	critique := &stubCritiqueQueue{responses: []critiqueDecision{{sufficient: true}}}
	params := AgenticChatParams{KbID: "kb1", Query: "original", Language: "en", MaxHops: 3}

	ctxResult, err := runAgenticChatTestable(context.Background(), nil, searcher, critique.next, params, func(map[string]any) {})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctxResult.FinalChunks) != len(ctxResult.Sources) || len(ctxResult.FinalChunks) == 0 {
		t.Fatalf("FinalChunks must mirror Sources and be non-empty: got %d chunks vs %d sources",
			len(ctxResult.FinalChunks), len(ctxResult.Sources))
	}
}

func TestRunAgenticChat_MaxHopsReached(t *testing.T) {
	searcher := &fakeAgenticSearcher{
		results: map[string]*vector.SearchResult{
			"original": {Chunks: []vector.SearchChunk{makeChunk("c1", "f1", "first")}},
			"follow-1": {Chunks: []vector.SearchChunk{makeChunk("c2", "f2", "second")}},
			"follow-2": {Chunks: []vector.SearchChunk{makeChunk("c3", "f3", "third")}},
		},
	}
	critique := &stubCritiqueQueue{responses: []critiqueDecision{
		{sufficient: false, followUp: "follow-1"},
		{sufficient: false, followUp: "follow-2"},
	}}

	beforeOutcome := agenticOutcomeCount("max_hops_reached")

	params := AgenticChatParams{
		KbID:     "kb1",
		Query:    "original",
		Language: "en",
		MaxHops:  3,
	}
	ctxResult, err := runAgenticChatTestable(context.Background(), nil, searcher, critique.next, params, func(map[string]any) {})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctxResult.Sources) != 3 {
		t.Errorf("sources count: got %d, want 3", len(ctxResult.Sources))
	}
	if len(searcher.calls) != 3 {
		t.Errorf("search calls: got %d, want 3", len(searcher.calls))
	}
	if got := agenticOutcomeCount("max_hops_reached") - beforeOutcome; got != 1 {
		t.Errorf("max_hops_reached counter delta: got %v, want 1", got)
	}
}

func TestRunAgenticChat_NoProgressEarlyStop(t *testing.T) {
	// Hop 2 returns the same chunk as hop 1 → dedup yields zero new chunks
	// → loop bails with outcome="early_stop_no_progress".
	dup := makeChunk("c1", "f1", "first")
	searcher := &fakeAgenticSearcher{
		results: map[string]*vector.SearchResult{
			"original": {Chunks: []vector.SearchChunk{dup}},
			"follow-1": {Chunks: []vector.SearchChunk{dup}},
		},
	}
	critique := &stubCritiqueQueue{responses: []critiqueDecision{
		{sufficient: false, followUp: "follow-1"},
	}}

	beforeOutcome := agenticOutcomeCount("early_stop_no_progress")

	params := AgenticChatParams{
		KbID:     "kb1",
		Query:    "original",
		Language: "en",
		MaxHops:  3,
	}
	ctxResult, err := runAgenticChatTestable(context.Background(), nil, searcher, critique.next, params, func(map[string]any) {})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctxResult.Sources) != 1 {
		t.Errorf("sources: got %d, want 1 (dedup)", len(ctxResult.Sources))
	}
	if got := agenticOutcomeCount("early_stop_no_progress") - beforeOutcome; got != 1 {
		t.Errorf("early_stop_no_progress counter delta: got %v, want 1", got)
	}
}

func TestRunAgenticChat_JudgeErrorFailOpen(t *testing.T) {
	searcher := &fakeAgenticSearcher{
		results: map[string]*vector.SearchResult{
			"original": {Chunks: []vector.SearchChunk{makeChunk("c1", "f1", "first")}},
		},
	}
	critique := &stubCritiqueQueue{responses: []critiqueDecision{
		{err: errors.New("simulated judge error")},
	}}

	beforeOutcome := agenticOutcomeCount("judge_error")

	params := AgenticChatParams{
		KbID:     "kb1",
		Query:    "original",
		Language: "en",
		MaxHops:  3,
	}
	ctxResult, err := runAgenticChatTestable(context.Background(), nil, searcher, critique.next, params, func(map[string]any) {})
	if err != nil {
		t.Fatalf("agentic chat should fail-open on judge error, got: %v", err)
	}
	if len(ctxResult.Sources) != 1 {
		t.Errorf("sources: got %d, want 1 (use what we have)", len(ctxResult.Sources))
	}
	if got := agenticOutcomeCount("judge_error") - beforeOutcome; got != 1 {
		t.Errorf("judge_error counter delta: got %v, want 1", got)
	}
}

func TestRunAgenticChat_FirstHopZeroChunksReturnsError(t *testing.T) {
	searcher := &fakeAgenticSearcher{
		results: map[string]*vector.SearchResult{},
	}
	params := AgenticChatParams{
		KbID:     "kb1",
		Query:    "original",
		Language: "en",
		MaxHops:  3,
	}
	_, err := runAgenticChatTestable(context.Background(), nil, searcher, nil, params, func(map[string]any) {})
	if err == nil {
		t.Error("expected error when first hop returns 0 chunks")
	}
}
