package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/kg"
)

func tfEnt(id int64, name string) kg.Entity { return kg.Entity{ID: id, CanonicalName: name} }

func TestApplyTripleFilterResult_PruneAndExpand(t *testing.T) {
	matched := []kg.Entity{tfEnt(1, "A"), tfEnt(2, "B")}
	triples := []kg.IncidentTriple{
		{SrcID: 1, SrcName: "A", Rel: "r", DstID: 9, DstName: "Neighbour"},
	}
	got := applyTripleFilterResult(matched, triples, []int64{1}, []int{0})
	ids := map[int64]bool{}
	for _, e := range got {
		ids[e.ID] = true
	}
	if !ids[1] || ids[2] || !ids[9] {
		t.Errorf("want {1,9}, got %v", ids)
	}
}

func TestApplyTripleFilterResult_PruneToEmptyKeepsMatched(t *testing.T) {
	matched := []kg.Entity{tfEnt(1, "A")}
	got := applyTripleFilterResult(matched, nil, []int64{}, nil)
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("prune-to-empty must fall back to matched, got %v", got)
	}
}

type fakeTripleStore struct {
	triples []kg.IncidentTriple
	err     error
}

func (f *fakeTripleStore) IncidentTriples(_ context.Context, _ string, _ []int64, _ int) ([]kg.IncidentTriple, error) {
	return f.triples, f.err
}
func (f *fakeTripleStore) LookupEntityByName(context.Context, string, string) ([]kg.Entity, error) {
	return nil, nil
}
func (f *fakeTripleStore) LookupSubgraph(context.Context, string, int64, int) (kg.Subgraph, error) {
	return kg.Subgraph{}, nil
}
func (f *fakeTripleStore) MatchAliasesInTokens(context.Context, string, []string) ([]kg.Entity, error) {
	return nil, nil
}
func (f *fakeTripleStore) PPRChunks(context.Context, string, []int64, int, int, kg.PPRConfig) ([]string, error) {
	return nil, nil
}
func (f *fakeTripleStore) PPRPassages(context.Context, string, []int64, int, kg.PPRConfig) ([]string, error) {
	return nil, nil
}
func (f *fakeTripleStore) PathChunks(context.Context, string, int64, int64, int, kg.PathConfig) ([]string, error) {
	return nil, nil
}

func TestRefineSeeds_LLMErrorFallsBackToMatched(t *testing.T) {
	matched := []kg.Entity{tfEnt(1, "A")}
	store := &fakeTripleStore{triples: []kg.IncidentTriple{{SrcID: 1, DstID: 9}}}
	fn := func(context.Context, string, string, string, string, *ai.StructuredSpec) (string, error) {
		return "", errors.New("llm down")
	}
	got := refineSeedsWithTripleFilter(context.Background(), fn, store, "kb", "q", "m", 40, matched)
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("LLM error must fall back to matched, got %v", got)
	}
}

func TestRefineSeeds_IncidentTriplesErrorFallsBackToMatched(t *testing.T) {
	matched := []kg.Entity{tfEnt(1, "A")}
	store := &fakeTripleStore{err: errors.New("db down")}
	called := false
	fn := func(context.Context, string, string, string, string, *ai.StructuredSpec) (string, error) {
		called = true
		return "{}", nil
	}
	got := refineSeedsWithTripleFilter(context.Background(), fn, store, "kb", "q", "m", 40, matched)
	if called {
		t.Error("LLM must not be called when IncidentTriples errors")
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("store error must fall back to matched, got %v", got)
	}
}

func TestRefineSeeds_NoTriplesFallsBackToMatched(t *testing.T) {
	matched := []kg.Entity{tfEnt(1, "A")}
	store := &fakeTripleStore{triples: nil}
	called := false
	fn := func(context.Context, string, string, string, string, *ai.StructuredSpec) (string, error) {
		called = true
		return "{}", nil
	}
	got := refineSeedsWithTripleFilter(context.Background(), fn, store, "kb", "q", "m", 40, matched)
	if called {
		t.Error("LLM must not be called when there are no candidate triples")
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("no triples must fall back to matched, got %v", got)
	}
}

func TestRefineSeeds_Success(t *testing.T) {
	matched := []kg.Entity{tfEnt(1, "A"), tfEnt(2, "B")}
	store := &fakeTripleStore{triples: []kg.IncidentTriple{{SrcID: 1, SrcName: "A", Rel: "r", DstID: 9, DstName: "N"}}}
	fn := func(context.Context, string, string, string, string, *ai.StructuredSpec) (string, error) {
		return `{"keepSeedIds":[1],"relevantTripleIds":[0]}`, nil
	}
	got := refineSeedsWithTripleFilter(context.Background(), fn, store, "kb", "q", "m", 40, matched)
	ids := map[int64]bool{}
	for _, e := range got {
		ids[e.ID] = true
	}
	if !ids[1] || ids[2] || !ids[9] {
		t.Errorf("want pruned+expanded {1,9}, got %v", ids)
	}
}
