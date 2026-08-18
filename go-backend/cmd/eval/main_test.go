package main

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/vector"
)

// stubSearcher returns a fixed chunk so the adapter's field mapping is the
// only thing under test.
type stubSearcher struct{ chunk vector.SearchChunk }

func (s *stubSearcher) Search(context.Context, string, string, int, vector.SearchOptions) (*vector.SearchResult, error) {
	return &vector.SearchResult{Chunks: []vector.SearchChunk{s.chunk}}, nil
}

func (s *stubSearcher) ExpandNeighbors(_ context.Context, chunks []vector.SearchChunk, _ int, _, _ string) []vector.SearchChunk {
	return chunks
}

// TestLegacySearchAdapterPropagatesFileName guards the retrieval-only eval
// path against silently dropping FileName. Goldens authored with
// must_cite_file_names (the re-ingest-resilient form — file UUIDs are
// regenerated on delete + re-upload) match on RetrievedChunk.FileName; when
// the adapter leaves it empty every such question scores recall 0.000 with
// no error, which reads as a retrieval regression rather than a harness bug.
func TestLegacySearchAdapterPropagatesFileName(t *testing.T) {
	a := &legacySearchAdapter{svc: &stubSearcher{chunk: vector.SearchChunk{
		FileID:   "file-uuid",
		FileName: "Stud.IP-Update.md",
		Score:    0.9,
	}}}

	got, err := a.Search(context.Background(), eval.Question{ID: "Q1", KbID: "kb"}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if got[0].FileName != "Stud.IP-Update.md" {
		t.Errorf("FileName = %q, want %q", got[0].FileName, "Stud.IP-Update.md")
	}
	if got[0].FileID != "file-uuid" {
		t.Errorf("FileID = %q, want %q", got[0].FileID, "file-uuid")
	}
}
