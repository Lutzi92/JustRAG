package agents

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

// fakeSearcher canned-result implementation. Safe for concurrent use
// (RunMulti dispatches specialists in parallel) via mu.
type fakeSearcher struct {
	mu     sync.Mutex
	calls  []searchCall
	respFn func(query string) []vector.SearchChunk
	errOn  string
	// respByType returns canned chunks keyed by opts.QueryType, letting
	// one shared searcher hand the retriever (complex_reasoning) and the
	// enumerator (enumeration) distinct result lists. Takes precedence
	// over respFn when set.
	respByType map[string][]vector.SearchChunk
	// errOnType fails any Search whose opts.QueryType matches — used to
	// exercise RunMulti's fail-open path when one specialist errors.
	errOnType string
}

type searchCall struct {
	query     string
	queryType string
}

func (f *fakeSearcher) Search(_ context.Context, _, query string, _ int, opts vector.SearchOptions) (*vector.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, searchCall{query: query, queryType: opts.QueryType})
	if f.errOnType != "" && f.errOnType == opts.QueryType {
		return nil, errors.New("simulated search error (by type)")
	}
	if f.errOn != "" && f.errOn == query {
		return nil, errors.New("simulated search error")
	}
	if f.respByType != nil {
		return &vector.SearchResult{Chunks: f.respByType[opts.QueryType]}, nil
	}
	if f.respFn == nil {
		return &vector.SearchResult{}, nil
	}
	return &vector.SearchResult{Chunks: f.respFn(query)}, nil
}

func chunk(content string, score float64) vector.SearchChunk {
	return vector.SearchChunk{ID: "id-" + content, FileID: "f-" + content, FileName: content, Content: content, Score: score}
}

func TestSupervisor_RoutesEnumerationToEnumerator(t *testing.T) {
	s := &fakeSearcher{respFn: func(_ string) []vector.SearchChunk { return []vector.SearchChunk{chunk("hit", 0.9)} }}
	classifier := func(_, _ string) bool { return true } // always enumeration

	sup := NewSupervisor(
		NewRetrieverAgent(s, ""),
		NewEnumeratorAgent(s, "", classifier),
		classifier,
	)
	res, err := sup.Run(context.Background(), Input{KbID: "kb1", Query: "Welche…", Language: "de"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Specialist != "enumerator" {
		t.Errorf("Specialist = %q, want enumerator", res.Specialist)
	}
	if len(s.calls) != 1 || s.calls[0].queryType != vector.QueryTypeEnumeration {
		t.Errorf("expected one enumeration-type search, got %#v", s.calls)
	}
	if res.Notes == "" {
		t.Errorf("enumerator dispatch should populate Notes for trajectory rendering")
	}
}

func TestSupervisor_RoutesNonEnumerationToRetriever(t *testing.T) {
	s := &fakeSearcher{respFn: func(_ string) []vector.SearchChunk { return []vector.SearchChunk{chunk("hit", 0.9)} }}
	classifier := func(_, _ string) bool { return false } // never enumeration

	sup := NewSupervisor(
		NewRetrieverAgent(s, ""),
		NewEnumeratorAgent(s, "", classifier),
		classifier,
	)
	res, err := sup.Run(context.Background(), Input{KbID: "kb1", Query: "Wer hat …?", Language: "de"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Specialist != "retriever" {
		t.Errorf("Specialist = %q, want retriever", res.Specialist)
	}
	if len(s.calls) != 1 || s.calls[0].queryType != vector.QueryTypeComplexReasoning {
		t.Errorf("retriever should issue complex_reasoning search, got %#v", s.calls)
	}
}

func TestSupervisor_NilEnumeratorFallsBackToRetriever(t *testing.T) {
	s := &fakeSearcher{respFn: func(_ string) []vector.SearchChunk { return []vector.SearchChunk{chunk("hit", 0.9)} }}
	classifier := func(_, _ string) bool { return true } // would prefer enumerator if available
	sup := NewSupervisor(NewRetrieverAgent(s, ""), nil, classifier)

	res, err := sup.Run(context.Background(), Input{KbID: "kb1", Query: "anything"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Specialist != "retriever" {
		t.Errorf("Specialist = %q, want retriever (fallback when enumerator is nil)", res.Specialist)
	}
}

func TestSupervisor_PropagatesSpecialistError(t *testing.T) {
	s := &fakeSearcher{errOn: "doomed"}
	sup := NewSupervisor(NewRetrieverAgent(s, ""), nil, nil)
	_, err := sup.Run(context.Background(), Input{KbID: "kb1", Query: "doomed"})
	if err == nil {
		t.Error("expected specialist error to propagate")
	}
}

func TestSupervisor_RunMulti_MergesAndDedups(t *testing.T) {
	// Retriever (complex_reasoning) finds A, B; Enumerator (enumeration)
	// finds B, C. B overlaps — must survive once, RRF-boosted to the top.
	s := &fakeSearcher{respByType: map[string][]vector.SearchChunk{
		vector.QueryTypeComplexReasoning: {chunk("A", 0.9), chunk("B", 0.8)},
		vector.QueryTypeEnumeration:      {chunk("B", 0.85), chunk("C", 0.7)},
	}}
	classifier := func(_, _ string) bool { return true } // enumeration intent → both specialists run

	sup := NewSupervisor(NewRetrieverAgent(s, ""), NewEnumeratorAgent(s, "", classifier), classifier)
	res, err := sup.RunMulti(context.Background(), Input{KbID: "kb1", Query: "Q", Language: "de"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	counts := map[string]int{}
	for _, c := range res.Chunks {
		counts[c.ID]++
	}
	if len(res.Chunks) != 3 {
		t.Fatalf("expected 3 deduped chunks, got %d (%v)", len(res.Chunks), counts)
	}
	if counts["id-B"] != 1 {
		t.Errorf("B should appear exactly once after dedup, got %d", counts["id-B"])
	}
	// B is in both lists → highest RRF score → first.
	if res.Chunks[0].ID != "id-B" {
		t.Errorf("expected B first by RRF fusion, got %q", res.Chunks[0].ID)
	}
	// Dedup keeps the higher-scored copy (0.85 from the enumerator arm),
	// so downstream IsLowConfidence / TruncateChunksToFit see real scores.
	for _, c := range res.Chunks {
		if c.ID == "id-B" && c.Score != 0.85 {
			t.Errorf("B should keep the higher original score 0.85, got %v", c.Score)
		}
		if c.FileName == "" {
			t.Errorf("chunk %q lost FileName in the merge (citations would break)", c.ID)
		}
	}
	if !strings.Contains(res.Specialist, "retriever") || !strings.Contains(res.Specialist, "enumerator") {
		t.Errorf("Specialist label should name both specialists, got %q", res.Specialist)
	}
}

func TestSupervisor_RunMulti_NonEnumerationRunsRetrieverOnly(t *testing.T) {
	s := &fakeSearcher{respByType: map[string][]vector.SearchChunk{
		vector.QueryTypeComplexReasoning: {chunk("A", 0.9)},
		vector.QueryTypeEnumeration:      {chunk("B", 0.85)},
	}}
	classifier := func(_, _ string) bool { return false } // no enumeration intent → retriever only

	sup := NewSupervisor(NewRetrieverAgent(s, ""), NewEnumeratorAgent(s, "", classifier), classifier)
	res, err := sup.RunMulti(context.Background(), Input{KbID: "kb1", Query: "Q"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Specialist != "retriever" {
		t.Errorf("Specialist = %q, want retriever-only", res.Specialist)
	}
	if len(s.calls) != 1 || s.calls[0].queryType != vector.QueryTypeComplexReasoning {
		t.Errorf("expected exactly one complex_reasoning search, got %#v", s.calls)
	}
	if len(res.Chunks) != 1 || res.Chunks[0].ID != "id-A" {
		t.Errorf("expected only retriever chunk A, got %#v", res.Chunks)
	}
}

func TestSupervisor_RunMulti_FailsOpenWhenOneSpecialistErrors(t *testing.T) {
	s := &fakeSearcher{
		respByType: map[string][]vector.SearchChunk{
			vector.QueryTypeComplexReasoning: {chunk("A", 0.9)},
		},
		errOnType: vector.QueryTypeEnumeration, // enumerator arm fails
	}
	classifier := func(_, _ string) bool { return true } // both dispatched

	sup := NewSupervisor(NewRetrieverAgent(s, ""), NewEnumeratorAgent(s, "", classifier), classifier)
	res, err := sup.RunMulti(context.Background(), Input{KbID: "kb1", Query: "Q"})
	if err != nil {
		t.Fatalf("should fail open when one specialist still succeeds, got err: %v", err)
	}
	if len(res.Chunks) != 1 || res.Chunks[0].ID != "id-A" {
		t.Errorf("expected the surviving retriever chunk A, got %#v", res.Chunks)
	}
	if res.Specialist != "retriever" {
		t.Errorf("Specialist should name only the survivor, got %q", res.Specialist)
	}
}

func TestSupervisor_RunMulti_ErrorsWhenAllSpecialistsFail(t *testing.T) {
	// Both specialists issue Search(query="Q"); erroring on that query
	// fails every arm, so RunMulti has nothing to return.
	s := &fakeSearcher{errOn: "Q"}
	classifier := func(_, _ string) bool { return true }
	sup := NewSupervisor(NewRetrieverAgent(s, ""), NewEnumeratorAgent(s, "", classifier), classifier)

	_, err := sup.RunMulti(context.Background(), Input{KbID: "kb1", Query: "Q"})
	if err == nil {
		t.Error("expected an error when all specialists fail")
	}
}
