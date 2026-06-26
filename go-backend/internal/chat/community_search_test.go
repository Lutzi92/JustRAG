package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

type fakeCommSearcher struct {
	res    *vector.SearchResult
	err    error
	calls  int
	gotOpt vector.SearchOptions
}

func (f *fakeCommSearcher) Search(_ context.Context, _, _ string, _ int, opts vector.SearchOptions) (*vector.SearchResult, error) {
	f.calls++
	f.gotOpt = opts
	return f.res, f.err
}

func TestRetrieveCommunityPrimerIDs(t *testing.T) {
	s := &fakeCommSearcher{res: &vector.SearchResult{Chunks: []vector.SearchChunk{{ID: "c1"}, {ID: "c2"}}}}
	ids := retrieveCommunityPrimerIDs(context.Background(), s, "kb", "q", 8)
	if len(ids) != 2 || ids[0] != "c1" || ids[1] != "c2" {
		t.Errorf("want [c1 c2], got %v", ids)
	}
	if s.gotOpt.NodeKindFilter != "community_summary" {
		t.Errorf("must filter to community_summary, got %q", s.gotOpt.NodeKindFilter)
	}
}

func TestRetrieveCommunityPrimerIDs_ErrorIsNil(t *testing.T) {
	s := &fakeCommSearcher{err: errors.New("boom")}
	if ids := retrieveCommunityPrimerIDs(context.Background(), s, "kb", "q", 8); ids != nil {
		t.Errorf("search error must yield nil, got %v", ids)
	}
}

func TestRetrieveCommunityPrimers_ReturnsChunks(t *testing.T) {
	s := &fakeCommSearcher{res: &vector.SearchResult{Chunks: []vector.SearchChunk{
		{ID: "c1", Content: "Cluster A summary"},
		{ID: "c2", Content: "Cluster B summary"},
	}}}
	got := retrieveCommunityPrimers(context.Background(), s, "kb", "themes?", 6)
	if len(got) != 2 || got[0].Content != "Cluster A summary" {
		t.Fatalf("want 2 chunks with content, got %#v", got)
	}
	if s.gotOpt.NodeKindFilter != "community_summary" {
		t.Errorf("want NodeKindFilter=community_summary, got %q", s.gotOpt.NodeKindFilter)
	}
}

func TestRetrieveCommunityPrimers_ErrorAndGuards(t *testing.T) {
	errS := &fakeCommSearcher{err: errors.New("boom")}
	if got := retrieveCommunityPrimers(context.Background(), errS, "kb", "q", 6); got != nil {
		t.Errorf("search error: want nil, got %#v", got)
	}
	// nil searcher / topK<1 → nil (guard before any Search call)
	if got := retrieveCommunityPrimers(context.Background(), nil, "kb", "q", 6); got != nil {
		t.Errorf("nil searcher: want nil, got %#v", got)
	}
	if got := retrieveCommunityPrimers(context.Background(), errS, "kb", "q", 0); got != nil {
		t.Errorf("topK<1: want nil, got %#v", got)
	}
}

func TestRetrieveCommunityPrimerIDs_SkipsEmptyIDs(t *testing.T) {
	s := &fakeCommSearcher{res: &vector.SearchResult{Chunks: []vector.SearchChunk{
		{ID: "c1", Content: "a"}, {ID: "", Content: "skip-empty-id"}, {ID: "c2", Content: "b"},
	}}}
	ids := retrieveCommunityPrimerIDs(context.Background(), s, "kb", "q", 6)
	if len(ids) != 2 || ids[0] != "c1" || ids[1] != "c2" {
		t.Errorf("MVP ID extraction (empty-id skip) regression: want [c1 c2], got %#v", ids)
	}
}

func TestShouldInjectCommunityPrimer(t *testing.T) {
	on := &fakeSiteConfigReader{values: map[string]*string{"chat_community_search_enabled": strPtr("true")}}
	off := &fakeSiteConfigReader{values: map[string]*string{}}
	ctx := context.Background()
	if !shouldInjectCommunityPrimer(ctx, on, "complex_reasoning", "summarise all documents") {
		t.Error("should fire: enabled + complex + global-synthesis")
	}
	if shouldInjectCommunityPrimer(ctx, off, "complex_reasoning", "summarise all documents") {
		t.Error("must not fire when flag off")
	}
	if shouldInjectCommunityPrimer(ctx, on, "lookup", "summarise all documents") {
		t.Error("must not fire for non-complex query")
	}
	if shouldInjectCommunityPrimer(ctx, on, "complex_reasoning", "who is the CFO") {
		t.Error("must not fire for non-global-synthesis query")
	}
}
