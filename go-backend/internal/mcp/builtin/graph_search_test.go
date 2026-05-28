package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/kg"
)

// Compile-time check: kg.Store drift surfaces here rather than at a call
// site that happens to type-check by coincidence.
var _ kg.Store = (*fakeKGStore)(nil)

type fakeKGStore struct {
	candidates  []kg.Entity
	subgraph    kg.Subgraph
	lookupErr   error
	subgraphErr error
	gotKbID     string
	gotEntity   string
	gotEntityID int64
	gotDepth    int
}

func (f *fakeKGStore) LookupEntityByName(_ context.Context, kbID, name string) ([]kg.Entity, error) {
	f.gotKbID = kbID
	f.gotEntity = name
	return f.candidates, f.lookupErr
}

func (f *fakeKGStore) LookupSubgraph(_ context.Context, _ string, entityID int64, depth int) (kg.Subgraph, error) {
	f.gotEntityID = entityID
	f.gotDepth = depth
	return f.subgraph, f.subgraphErr
}

func (f *fakeKGStore) MatchAliasesInTokens(_ context.Context, _ string, _ []string) ([]kg.Entity, error) {
	return nil, nil
}

// PPRChunks / PathChunks: the MCP graph_search tool does not call
// either, so a stub returning nil is enough to satisfy the
// kg.Store interface here. Any test that wants to exercise these
// paths lives in the chat package's graph_routing tests.
func (f *fakeKGStore) PPRChunks(_ context.Context, _ string, _ []int64, _ int, _ int, _ kg.PPRConfig) ([]string, error) {
	return nil, nil
}

func (f *fakeKGStore) PathChunks(_ context.Context, _ string, _, _ int64, _ int, _ kg.PathConfig) ([]string, error) {
	return nil, nil
}

func TestGraphSearch_RequiresEntity(t *testing.T) {
	t.Parallel()
	tool := NewGraphSearch(&fakeKGStore{})
	_, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"kb_id":"kb-1"}`))
	if err == nil {
		t.Error("expected error on missing entity")
	}
}

func TestGraphSearch_RequiresKbID(t *testing.T) {
	t.Parallel()
	tool := NewGraphSearch(&fakeKGStore{})
	_, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"entity":"PPM"}`))
	if err == nil {
		t.Error("expected error on missing kb_id")
	}
}

// TestGraphSearch_NoMatchReturnsCleanEmpty: when the entity name
// resolves to zero candidates, the tool must return a non-error
// result with explanatory Meta — distinct from "tool errored". The
// LLM uses this signal to decide whether to retry with a different
// entity name.
func TestGraphSearch_NoMatchReturnsCleanEmpty(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{candidates: nil}
	tool := NewGraphSearch(store)
	got, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"entity":"Unknown","kb_id":"kb-1"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Meta["resolved_count"].(int) != 0 {
		t.Errorf("resolved_count should be 0 for no-match, got %v", got.Meta["resolved_count"])
	}
	if got.Meta["resolution_note"] == nil {
		t.Error("resolution_note should be present on no-match so the LLM gets a clean signal")
	}
}

// TestGraphSearch_DepthClamping: depth=0 → 1, depth=99 → 2. Pinned
// because the LLM occasionally emits 5+ for "deep search" — we
// don't want to walk a 5-hop subgraph that fully connects the KB.
func TestGraphSearch_DepthClamping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input int
		want  int
	}{
		{0, 1}, // unset → 1
		{1, 1},
		{2, 2},
		{5, 2},  // clamped down
		{-3, 1}, // clamped up
	}
	for _, tc := range cases {
		store := &fakeKGStore{
			candidates: []kg.Entity{{ID: 42, CanonicalName: "PPM-Team", Type: "organization"}},
			subgraph:   kg.Subgraph{Root: kg.Entity{ID: 42, CanonicalName: "PPM-Team"}},
		}
		tool := NewGraphSearch(store)
		args, _ := json.Marshal(map[string]any{
			"entity": "PPM-Team",
			"kb_id":  "kb-1",
			"depth":  tc.input,
		})
		_, err := tool.Handler.Invoke(context.Background(), args)
		if err != nil {
			t.Fatalf("input=%d: unexpected error: %v", tc.input, err)
		}
		if store.gotDepth != tc.want {
			t.Errorf("input=%d: store saw depth=%d, want %d", tc.input, store.gotDepth, tc.want)
		}
	}
}

// TestGraphSearch_RelationFilter: edges whose `rel` substring-matches
// the filter survive; others drop. Case-insensitive because the LLM
// tends to phrase filters in natural language ("Works") while the
// stored relations are snake_case ("works_in").
func TestGraphSearch_RelationFilter(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{
		candidates: []kg.Entity{{ID: 1, CanonicalName: "Kurz"}},
		subgraph: kg.Subgraph{
			Root: kg.Entity{ID: 1, CanonicalName: "Kurz"},
			Edges: []kg.Edge{
				{Direction: "out", Rel: "works_in", Other: kg.Entity{ID: 2, CanonicalName: "PPM-Team"}},
				{Direction: "out", Rel: "leads", Other: kg.Entity{ID: 3, CanonicalName: "Project X"}},
				{Direction: "in", Rel: "manages", Other: kg.Entity{ID: 4, CanonicalName: "HRZ"}},
			},
		},
	}
	tool := NewGraphSearch(store)
	got, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"entity":"Kurz","kb_id":"kb-1","relation_filter":"WORK"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Decode structured payload.
	var payload map[string]any
	if err := json.Unmarshal(got.Structured, &payload); err != nil {
		t.Fatal(err)
	}
	edges, _ := payload["edges"].([]any)
	if len(edges) != 1 {
		t.Errorf("expected 1 edge after filter, got %d", len(edges))
	}
	if got.Meta["edge_count"].(int) != 1 {
		t.Errorf("edge_count meta should reflect filter, got %v", got.Meta["edge_count"])
	}
}

// TestGraphSearch_PropagatesLookupError: a DB failure on entity
// lookup must surface as a tool error so the planner / fallback
// can react.
func TestGraphSearch_PropagatesLookupError(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{lookupErr: errors.New("db boom")}
	tool := NewGraphSearch(store)
	_, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"entity":"PPM","kb_id":"kb-1"}`))
	if err == nil {
		t.Error("expected error to bubble")
	}
}
