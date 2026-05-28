package chat

import (
	"context"
	"sync"
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

// fakeSupSearcher returns canned chunks keyed by the SearchOptions
// QueryType, so the retriever arm (complex_reasoning) and the
// enumerator arm (enumeration) get distinct result lists from one
// shared searcher. Concurrency-safe — RunMulti dispatches in parallel.
type fakeSupSearcher struct {
	mu     sync.Mutex
	byType map[string][]vector.SearchChunk
	calls  []string
}

func (f *fakeSupSearcher) Search(_ context.Context, _, _ string, _ int, opts vector.SearchOptions) (*vector.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, opts.QueryType)
	return &vector.SearchResult{Chunks: f.byType[opts.QueryType]}, nil
}

func supChunk(id string, score float64) vector.SearchChunk {
	return vector.SearchChunk{
		ID: id, FileID: "f-" + id, FileName: id + ".pdf",
		Content: "content " + id, Score: score, VectorScore: 0.9,
	}
}

func TestRunSupervisorChat_MultiSpecialistMergesBothArms(t *testing.T) {
	s := &fakeSupSearcher{byType: map[string][]vector.SearchChunk{
		vector.QueryTypeComplexReasoning: {supChunk("A", 0.9), supChunk("B", 0.8), supChunk("D", 0.6)},
		vector.QueryTypeEnumeration:      {supChunk("C", 0.85), supChunk("B", 0.7)},
	}}
	classify := func(_, _ string) bool { return true } // enumeration intent → both arms

	ctxOut, err := runSupervisorChatTestable(
		context.Background(),
		nil,
		s,
		classify,
		SupervisorChatParams{KbID: "kb1", Query: "Q", Language: "en", MultiSpecialist: true},
		func(map[string]any) {},
	)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(s.calls) != 2 {
		t.Fatalf("expected both specialists to search, got calls=%v", s.calls)
	}

	gotIDs := map[string]int{}
	for _, src := range ctxOut.Sources {
		gotIDs[src.ChunkID]++
	}
	for _, want := range []string{"A", "C"} {
		if gotIDs[want] == 0 {
			t.Errorf("merged sources missing %q (both arms should contribute); got %v", want, gotIDs)
		}
	}
	if gotIDs["B"] != 1 {
		t.Errorf("overlapping chunk B should dedupe to one source, got %d", gotIDs["B"])
	}
}

func TestRunSupervisorChat_SingleSpecialistByDefault(t *testing.T) {
	s := &fakeSupSearcher{byType: map[string][]vector.SearchChunk{
		vector.QueryTypeComplexReasoning: {supChunk("A", 0.9)},
		vector.QueryTypeEnumeration:      {supChunk("C", 0.85)},
	}}
	classify := func(_, _ string) bool { return true } // enumeration intent

	ctxOut, err := runSupervisorChatTestable(
		context.Background(),
		nil,
		s,
		classify,
		SupervisorChatParams{KbID: "kb1", Query: "Q", Language: "en"}, // MultiSpecialist defaults false
		func(map[string]any) {},
	)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(s.calls) != 1 || s.calls[0] != vector.QueryTypeEnumeration {
		t.Fatalf("legacy single-dispatch should issue exactly one enumeration search, got %v", s.calls)
	}
	for _, src := range ctxOut.Sources {
		if src.ChunkID == "A" {
			t.Errorf("retriever arm should not run in single-specialist mode; got chunk A")
		}
	}
}
