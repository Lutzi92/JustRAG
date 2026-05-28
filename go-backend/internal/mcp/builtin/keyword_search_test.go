package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

type fakeKeywordSearcher struct {
	chunks    []vector.SearchChunk
	err       error
	gotKbID   string
	gotQuery  string
	gotLimit  int
	gotFiles  []string
	callCount int
}

func (f *fakeKeywordSearcher) KeywordSearch(_ context.Context, kbID, query string, limit int, fileIDs []string) ([]vector.SearchChunk, error) {
	f.callCount++
	f.gotKbID = kbID
	f.gotQuery = query
	f.gotLimit = limit
	f.gotFiles = fileIDs
	return f.chunks, f.err
}

// TestKeywordSearch_RequiresQuery: empty query is a developer error
// (the planner shouldn't dispatch it). Surface as a tool error so
// the orchestrator's error-handling shows it on the trajectory.
func TestKeywordSearch_RequiresQuery(t *testing.T) {
	t.Parallel()
	tool := NewKeywordSearch(&fakeKeywordSearcher{})
	_, err := tool.Handler.Invoke(context.Background(), json.RawMessage(`{"kb_id":"kb-1"}`))
	if err == nil {
		t.Error("expected error on missing query")
	}
}

// TestKeywordSearch_RequiresKbID: the chat orchestrator is responsible
// for injecting kb_id into args before dispatch — the LLM is never
// shown its own KB ID. A missing kb_id signals a wiring regression
// in the dispatcher; surface as a hard error.
func TestKeywordSearch_RequiresKbID(t *testing.T) {
	t.Parallel()
	tool := NewKeywordSearch(&fakeKeywordSearcher{})
	_, err := tool.Handler.Invoke(context.Background(), json.RawMessage(`{"query":"foo"}`))
	if err == nil {
		t.Error("expected error on missing kb_id")
	}
}

// TestKeywordSearch_PassesAllArgs: every documented field reaches the
// underlying SearchService — top_k as limit, file_ids as filter.
func TestKeywordSearch_PassesAllArgs(t *testing.T) {
	t.Parallel()
	fake := &fakeKeywordSearcher{
		chunks: []vector.SearchChunk{
			{ID: "c1", FileID: "f1", Content: "hit", Score: 0.9},
		},
	}
	tool := NewKeywordSearch(fake)
	got, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"query":"§13 BGB","kb_id":"kb-1","top_k":7,"file_ids":["f1","f2"]}`),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.gotKbID != "kb-1" || fake.gotQuery != "§13 BGB" || fake.gotLimit != 7 {
		t.Errorf("args not propagated: kb=%q query=%q limit=%d", fake.gotKbID, fake.gotQuery, fake.gotLimit)
	}
	if len(fake.gotFiles) != 2 || fake.gotFiles[0] != "f1" {
		t.Errorf("file_ids not propagated: %v", fake.gotFiles)
	}
	if len(got.Chunks) != 1 || got.Chunks[0].ID != "c1" {
		t.Errorf("chunks not bubbled: %+v", got.Chunks)
	}
	if got.Meta["method"] != "bm25_only" {
		t.Errorf("method label missing: %v", got.Meta)
	}
}

// TestKeywordSearch_PropagatesError: the SearchService's error
// becomes the tool's error verbatim (wrapped with a tool prefix).
func TestKeywordSearch_PropagatesError(t *testing.T) {
	t.Parallel()
	fake := &fakeKeywordSearcher{err: errors.New("db boom")}
	tool := NewKeywordSearch(fake)
	_, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"query":"foo","kb_id":"kb-1"}`),
	)
	if err == nil {
		t.Fatal("expected error to bubble")
	}
}
