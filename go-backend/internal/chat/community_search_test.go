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
