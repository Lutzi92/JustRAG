package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

// fakeDriftSearcher returns scripted results per call. Call 0 is the primer
// search (NodeKindFilter=community_summary); subsequent calls are follow-ups.
type fakeDriftSearcher struct {
	calls   []driftCall
	results []*vector.SearchResult
	errs    []error
	i       int
}
type driftCall struct {
	query string
	opts  vector.SearchOptions
}

func (f *fakeDriftSearcher) Search(ctx context.Context, kbID, query string, limit int, opts vector.SearchOptions) (*vector.SearchResult, error) {
	f.calls = append(f.calls, driftCall{query: query, opts: opts})
	idx := f.i
	f.i++
	if idx < len(f.errs) && f.errs[idx] != nil {
		return nil, f.errs[idx]
	}
	if idx < len(f.results) {
		return f.results[idx], nil
	}
	return &vector.SearchResult{}, nil
}

func driftChunk(id, name string) vector.SearchChunk {
	return vector.SearchChunk{ID: id, FileName: name, Content: "content-" + id}
}

func TestRunDriftChat_HappyPath(t *testing.T) {
	f := &fakeDriftSearcher{results: []*vector.SearchResult{
		{Chunks: []vector.SearchChunk{driftChunk("cs1", "community"), driftChunk("cs2", "community")}}, // primer
		{Chunks: []vector.SearchChunk{driftChunk("a1", "f1.pdf")}},                                     // follow-up 1
		{Chunks: []vector.SearchChunk{driftChunk("a2", "f2.pdf")}},                                     // follow-up 2
	}}
	gen := func(ctx context.Context, r *ai.ConfigResolver, kbID, query, primer, lang, model string) ([]string, error) {
		if primer == "" {
			t.Errorf("expected non-empty primer text")
		}
		return []string{"follow up 1", "follow up 2"}, nil
	}
	var stages []string
	emit := func(m map[string]any) {
		if evt, ok := m["agentTrajectory"].(TrajectoryEvent); ok {
			stages = append(stages, evt.Stage)
		}
	}
	cc, err := runDriftChatTestable(context.Background(), nil, f, gen,
		DriftChatParams{KbID: "kb", Query: "common themes?", Language: "en", MaxFollowups: 4, PrimerTopK: 6, SearchTopK: 8}, emit)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(f.calls) != 3 {
		t.Fatalf("want 3 searches (1 primer + 2 follow-ups), got %d", len(f.calls))
	}
	if f.calls[0].opts.NodeKindFilter != "community_summary" {
		t.Errorf("first search must be the community primer; got filter %q", f.calls[0].opts.NodeKindFilter)
	}
	if cc == nil || len(cc.Sources) == 0 || cc.Context == "" {
		t.Fatalf("expected assembled ChatContext with sources+context, got %#v", cc)
	}
	want := []string{"primer", "drift_followups", "search", "search", "answer"}
	if len(stages) != len(want) {
		t.Fatalf("stages: want %v, got %v", want, stages)
	}
	for i := range want {
		if stages[i] != want[i] {
			t.Errorf("stage %d: want %q got %q", i, want[i], stages[i])
		}
	}
}

func TestRunDriftChat_PrimerlessStillRuns(t *testing.T) {
	f := &fakeDriftSearcher{results: []*vector.SearchResult{
		{}, // primer empty
		{Chunks: []vector.SearchChunk{driftChunk("a1", "f1.pdf")}}, // follow-up
	}}
	var gotPrimer string
	gen := func(ctx context.Context, r *ai.ConfigResolver, kbID, query, primer, lang, model string) ([]string, error) {
		gotPrimer = primer
		return []string{"q1"}, nil
	}
	cc, err := runDriftChatTestable(context.Background(), nil, f, gen,
		DriftChatParams{KbID: "kb", Query: "broad?", Language: "en"}, func(map[string]any) {})
	if err != nil {
		t.Fatalf("primerless should still produce an answer: %v", err)
	}
	if gotPrimer != "" {
		t.Errorf("expected empty primer text, got %q", gotPrimer)
	}
	if cc == nil || len(cc.Sources) == 0 {
		t.Fatalf("expected sources from follow-up search")
	}
}

func TestRunDriftChat_FollowupGenFailsFallsBackToQuery(t *testing.T) {
	f := &fakeDriftSearcher{results: []*vector.SearchResult{
		{Chunks: []vector.SearchChunk{driftChunk("cs1", "community")}}, // primer
		{Chunks: []vector.SearchChunk{driftChunk("a1", "f1.pdf")}},     // the single original-query search
	}}
	gen := func(ctx context.Context, r *ai.ConfigResolver, kbID, query, primer, lang, model string) ([]string, error) {
		return nil, errors.New("llm down")
	}
	cc, err := runDriftChatTestable(context.Background(), nil, f, gen,
		DriftChatParams{KbID: "kb", Query: "the original question", Language: "en"}, func(map[string]any) {})
	if err != nil {
		t.Fatalf("fallback should not error: %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("want 2 searches (primer + original-query fallback), got %d", len(f.calls))
	}
	if f.calls[1].query != "the original question" {
		t.Errorf("fallback search query: want original question, got %q", f.calls[1].query)
	}
	if cc == nil {
		t.Fatal("want ChatContext")
	}
}

func TestRunDriftChat_FollowupSearchErrorSkips(t *testing.T) {
	f := &fakeDriftSearcher{
		results: []*vector.SearchResult{
			{Chunks: []vector.SearchChunk{driftChunk("cs1", "community")}}, // primer
			nil,                                                             // follow-up 1 errors
			{Chunks: []vector.SearchChunk{driftChunk("a2", "f2.pdf")}},     // follow-up 2 ok
		},
		errs: []error{nil, errors.New("search boom"), nil},
	}
	gen := func(ctx context.Context, r *ai.ConfigResolver, kbID, query, primer, lang, model string) ([]string, error) {
		return []string{"q1", "q2"}, nil
	}
	cc, err := runDriftChatTestable(context.Background(), nil, f, gen,
		DriftChatParams{KbID: "kb", Query: "q", Language: "en"}, func(map[string]any) {})
	if err != nil {
		t.Fatalf("one failed follow-up should not error the run: %v", err)
	}
	if cc == nil || len(cc.Sources) == 0 {
		t.Fatal("want sources from the surviving follow-up (+primer)")
	}
}

func TestRunDriftChat_EmptyEverythingReturnsError(t *testing.T) {
	f := &fakeDriftSearcher{results: []*vector.SearchResult{{}, {}}} // primer empty, follow-up empty
	gen := func(ctx context.Context, r *ai.ConfigResolver, kbID, query, primer, lang, model string) ([]string, error) {
		return []string{"q1"}, nil
	}
	_, err := runDriftChatTestable(context.Background(), nil, f, gen,
		DriftChatParams{KbID: "kb", Query: "q", Language: "en"}, func(map[string]any) {})
	if err == nil {
		t.Fatal("empty accumulated set must return an error so the caller falls through to the standard path")
	}
}
