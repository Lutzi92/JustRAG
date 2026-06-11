package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/kg"
)

// Compile-time check: any drift in kg.Store breaks the build here rather
// than at the call site, so tests can't silently diverge from the
// production interface.
var _ kg.Store = (*fakeKGStore)(nil)

type fakeKGStore struct {
	matches []kg.Entity
	err     error
	gotKbID string
	gotToks []string
	calls   int

	// Subgraph wiring (for ResolveGraphChunks tests)
	subgraph         kg.Subgraph
	subgraphErr      error
	subgraphCalls    int
	subgraphGotKbID  string
	subgraphGotID    int64
	subgraphGotDepth int

	// PPRChunks wiring (T1-4). pprChunks is the result, pprErr the
	// error to surface. pprCalls is a hit counter so tests can
	// assert no/exactly-one call without needing reflect on the
	// captured args.
	pprChunks  []string
	pprErr     error
	pprCalls   int
	pprSeedIDs []int64

	// PathChunks wiring (T1-5). Same shape as PPR — counters +
	// captured args + canned result.
	pathChunks []string
	pathErr    error
	pathCalls  int
	pathSrcID  int64
	pathDstID  int64
}

func (f *fakeKGStore) LookupEntityByName(_ context.Context, _, _ string) ([]kg.Entity, error) {
	return nil, nil
}

func (f *fakeKGStore) LookupSubgraph(_ context.Context, kbID string, entityID int64, depth int) (kg.Subgraph, error) {
	f.subgraphCalls++
	f.subgraphGotKbID = kbID
	f.subgraphGotID = entityID
	f.subgraphGotDepth = depth
	return f.subgraph, f.subgraphErr
}

func (f *fakeKGStore) MatchAliasesInTokens(_ context.Context, kbID string, toks []string) ([]kg.Entity, error) {
	f.calls++
	f.gotKbID = kbID
	f.gotToks = toks
	return f.matches, f.err
}

func (f *fakeKGStore) PPRChunks(_ context.Context, _ string, seedIDs []int64, _ int, _ int, _ kg.PPRConfig) ([]string, error) {
	f.pprCalls++
	f.pprSeedIDs = append([]int64(nil), seedIDs...)
	return f.pprChunks, f.pprErr
}

func (f *fakeKGStore) PathChunks(_ context.Context, _ string, srcID, dstID int64, _ int, _ kg.PathConfig) ([]string, error) {
	f.pathCalls++
	f.pathSrcID = srcID
	f.pathDstID = dstID
	return f.pathChunks, f.pathErr
}

// TestNeedsGraphTraversal exercises the routing predicate's full decision
// table. Each case sets up the gate/store wiring, calls the predicate
// once, and asserts (fired, outcome, store.calls). Cases are kept in
// the same order as the predicate's branches so the table reads top-to-
// bottom alongside the function body.
//
//   - disabled_short_circuits: gate off → no store call, no fire. Pinned
//     via the call counter to catch any future "always look up" regression.
//   - skipped_route_<query_type>: non-complex query types skip the
//     heuristic before consulting the store. Each is a separate subtest
//     so the failing query type is visible in the `-run` selector.
//   - no_entity_match: gate on, query has tokens but the KB's aliases
//     don't match — outcome skipped_no_entity, exactly one store call.
//   - fires_on_entity_match: alias match → fires and surfaces the entity.
//   - db_error_*: a store error or a nil store both become outcome
//     db_error without firing — chat proceeds without graph chunks
//     rather than failing the turn.
func TestNeedsGraphTraversal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	gateOn := map[string]*string{"chat_graph_routing_enabled": strPtr("true")}

	type wantMatch struct {
		id            int64
		canonicalName string
	}

	cases := []struct {
		name        string
		newStore    func() *fakeKGStore // nil → pass nil store to predicate
		flags       map[string]*string
		query       string
		queryType   string
		wantFired   bool
		wantOutcome string
		wantCalls   int
		wantMatch   *wantMatch
	}{
		{
			name:        "disabled_short_circuits",
			newStore:    func() *fakeKGStore { return &fakeKGStore{} },
			flags:       map[string]*string{},
			query:       "Welche Verträge des PPM-Teams referenzieren X?",
			queryType:   "complex_reasoning",
			wantFired:   false,
			wantOutcome: "skipped_disabled",
			wantCalls:   0,
		},
		{
			name:        "skipped_route_lookup",
			newStore:    func() *fakeKGStore { return &fakeKGStore{} },
			flags:       gateOn,
			query:       "any query",
			queryType:   "lookup",
			wantFired:   false,
			wantOutcome: "skipped_route",
			wantCalls:   0,
		},
		{
			name:        "skipped_route_enumeration",
			newStore:    func() *fakeKGStore { return &fakeKGStore{} },
			flags:       gateOn,
			query:       "any query",
			queryType:   "enumeration",
			wantFired:   false,
			wantOutcome: "skipped_route",
			wantCalls:   0,
		},
		{
			name:        "skipped_route_global_synthesis",
			newStore:    func() *fakeKGStore { return &fakeKGStore{} },
			flags:       gateOn,
			query:       "any query",
			queryType:   "global_synthesis",
			wantFired:   false,
			wantOutcome: "skipped_route",
			wantCalls:   0,
		},
		{
			name:        "skipped_route_unknown",
			newStore:    func() *fakeKGStore { return &fakeKGStore{} },
			flags:       gateOn,
			query:       "any query",
			queryType:   "unknown",
			wantFired:   false,
			wantOutcome: "skipped_route",
			wantCalls:   0,
		},
		{
			name:        "skipped_route_empty_query_type",
			newStore:    func() *fakeKGStore { return &fakeKGStore{} },
			flags:       gateOn,
			query:       "any query",
			queryType:   "",
			wantFired:   false,
			wantOutcome: "skipped_route",
			wantCalls:   0,
		},
		{
			name:        "no_entity_match",
			newStore:    func() *fakeKGStore { return &fakeKGStore{matches: nil} },
			flags:       gateOn,
			query:       "Welche generischen Themen?",
			queryType:   "complex_reasoning",
			wantFired:   false,
			wantOutcome: "skipped_no_entity",
			wantCalls:   1,
		},
		{
			name: "fires_on_entity_match",
			newStore: func() *fakeKGStore {
				return &fakeKGStore{
					matches: []kg.Entity{
						{ID: 42, CanonicalName: "PPM-Team", Type: "organization", Aliases: []string{"PPM", "PPM-Team"}},
					},
				}
			},
			flags:     gateOn,
			query:     "Welche Verträge des PPM-Teams referenzieren Richtlinie X?",
			queryType: "complex_reasoning",
			wantFired: true,
			wantCalls: 1,
			wantMatch: &wantMatch{id: 42, canonicalName: "PPM-Team"},
		},
		{
			name:        "db_error_from_store",
			newStore:    func() *fakeKGStore { return &fakeKGStore{err: errors.New("db boom")} },
			flags:       gateOn,
			query:       "PPM-Team",
			queryType:   "complex_reasoning",
			wantFired:   false,
			wantOutcome: "db_error",
			wantCalls:   1,
		},
		{
			name:        "db_error_nil_store",
			newStore:    nil, // pass nil into the predicate
			flags:       gateOn,
			query:       "PPM",
			queryType:   "complex_reasoning",
			wantFired:   false,
			wantOutcome: "db_error",
			wantCalls:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &fakeSiteConfigReader{values: tc.flags}
			var store kg.Store
			var fake *fakeKGStore
			if tc.newStore != nil {
				fake = tc.newStore()
				store = fake
			}

			dec := NeedsGraphTraversal(ctx, store, r, "kb-1", tc.query, tc.queryType)

			if dec.Fired != tc.wantFired {
				t.Errorf("fired: got %v, want %v (outcome=%q)", dec.Fired, tc.wantFired, dec.Outcome)
			}
			if tc.wantOutcome != "" && dec.Outcome != tc.wantOutcome {
				t.Errorf("outcome: got %q, want %q", dec.Outcome, tc.wantOutcome)
			}
			if fake != nil && fake.calls != tc.wantCalls {
				t.Errorf("store.calls: got %d, want %d", fake.calls, tc.wantCalls)
			}
			if tc.wantMatch != nil {
				if len(dec.MatchedEntities) != 1 {
					t.Fatalf("matched entities: got %d, want 1: %+v", len(dec.MatchedEntities), dec.MatchedEntities)
				}
				if dec.MatchedEntities[0].ID != tc.wantMatch.id {
					t.Errorf("matched entity ID: got %d, want %d", dec.MatchedEntities[0].ID, tc.wantMatch.id)
				}
				if dec.MatchedEntities[0].CanonicalName != tc.wantMatch.canonicalName {
					t.Errorf("matched entity name: got %q, want %q", dec.MatchedEntities[0].CanonicalName, tc.wantMatch.canonicalName)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ResolveGraphChunks — chunk-id resolution for AP-C4 RRF injection
// ---------------------------------------------------------------------------

// TestResolveGraphChunks_NotFiredReturnsNil: when the routing
// decision didn't fire, no subgraph lookup happens. Skipping the
// LookupSubgraph round-trip on every non-fired turn is the whole
// reason the gate exists.
func TestResolveGraphChunks_NotFiredReturnsNil(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{}
	dec := GraphTraversalDecision{Fired: false, Outcome: "skipped_no_entity"}
	got := ResolveGraphChunks(context.Background(), store, "kb-1", dec, 15)
	if got != nil {
		t.Errorf("expected nil chunks, got %v", got)
	}
	if store.subgraphCalls != 0 {
		t.Errorf("LookupSubgraph called %d times despite decision not fired", store.subgraphCalls)
	}
}

// TestResolveGraphChunks_FiredEmptyMatchedReturnsNil: the decision
// was fired but no entities are attached (defensive case — should
// never happen but the resolver must not panic).
func TestResolveGraphChunks_FiredEmptyMatchedReturnsNil(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{}
	dec := GraphTraversalDecision{Fired: true, MatchedEntities: nil}
	got := ResolveGraphChunks(context.Background(), store, "kb-1", dec, 15)
	if got != nil {
		t.Errorf("expected nil chunks for empty matches, got %v", got)
	}
	if store.subgraphCalls != 0 {
		t.Errorf("LookupSubgraph called %d times despite empty matches", store.subgraphCalls)
	}
}

// TestResolveGraphChunks_NilStoreReturnsNil: defensive — a nil
// store must not panic. Production wires a real store, but a
// future code path (eval harness, tests) might not.
func TestResolveGraphChunks_NilStoreReturnsNil(t *testing.T) {
	t.Parallel()
	dec := GraphTraversalDecision{
		Fired:           true,
		MatchedEntities: []kg.Entity{{ID: 42, CanonicalName: "PPM"}},
	}
	got := ResolveGraphChunks(context.Background(), nil, "kb-1", dec, 15)
	if got != nil {
		t.Errorf("nil store must yield nil, got %v", got)
	}
}

// TestResolveGraphChunks_HappyPath: fired decision + non-empty
// subgraph chunk list → resolver returns those chunks (capped),
// uses depth=1, queries the top-1 entity ID.
func TestResolveGraphChunks_HappyPath(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{
		subgraph: kg.Subgraph{
			ChunkIDs: []string{"c1", "c2", "c3", "c4", "c5"},
		},
	}
	dec := GraphTraversalDecision{
		Fired: true,
		MatchedEntities: []kg.Entity{
			{ID: 42, CanonicalName: "PPM-Team"},
			{ID: 99, CanonicalName: "HRZ"}, // top-1 only — second is ignored
		},
	}
	got := ResolveGraphChunks(context.Background(), store, "kb-1", dec, 15)
	if len(got) != 5 {
		t.Errorf("expected 5 chunks, got %d: %v", len(got), got)
	}
	if store.subgraphCalls != 1 {
		t.Errorf("expected 1 LookupSubgraph call, got %d", store.subgraphCalls)
	}
	if store.subgraphGotID != 42 {
		t.Errorf("expected entity ID 42 (top match), got %d", store.subgraphGotID)
	}
	if store.subgraphGotDepth != 1 {
		t.Errorf("expected depth=1, got %d", store.subgraphGotDepth)
	}
	if store.subgraphGotKbID != "kb-1" {
		t.Errorf("kbID not propagated: got %q", store.subgraphGotKbID)
	}
}

// TestResolveGraphChunks_RespectsMaxCap: subgraph returns more
// chunks than the cap — resolver trims. Order is preserved (subgraph
// returns chunks in edge-traversal order; first N are nearest the
// root).
func TestResolveGraphChunks_RespectsMaxCap(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{
		subgraph: kg.Subgraph{
			ChunkIDs: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"},
		},
	}
	dec := GraphTraversalDecision{
		Fired:           true,
		MatchedEntities: []kg.Entity{{ID: 42}},
	}
	got := ResolveGraphChunks(context.Background(), store, "kb-1", dec, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3 chunks (cap), got %d: %v", len(got), got)
	}
	for i, want := range []string{"c1", "c2", "c3"} {
		if got[i] != want {
			t.Errorf("[%d]: got %q, want %q", i, got[i], want)
		}
	}
}

// TestResolveGraphChunks_StoreErrorReturnsNil: LookupSubgraph
// failure is fail-open. The chat pipeline proceeds without graph
// chunks rather than failing the whole turn.
func TestResolveGraphChunks_StoreErrorReturnsNil(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{subgraphErr: errors.New("db boom")}
	dec := GraphTraversalDecision{
		Fired:           true,
		MatchedEntities: []kg.Entity{{ID: 42}},
	}
	got := ResolveGraphChunks(context.Background(), store, "kb-1", dec, 15)
	if got != nil {
		t.Errorf("store error must yield nil, got %v", got)
	}
}

// TestResolveGraphChunks_EmptySubgraphReturnsNil: subgraph is
// reachable but has zero chunk_ids (orphan entity with no edges
// carrying evidence). Treat as no-injection.
func TestResolveGraphChunks_EmptySubgraphReturnsNil(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{subgraph: kg.Subgraph{ChunkIDs: nil}}
	dec := GraphTraversalDecision{
		Fired:           true,
		MatchedEntities: []kg.Entity{{ID: 42}},
	}
	got := ResolveGraphChunks(context.Background(), store, "kb-1", dec, 15)
	if got != nil {
		t.Errorf("empty subgraph must yield nil, got %v", got)
	}
}

// TestResolveGraphChunks_DeduplicatesChunkIDs: a subgraph with
// repeated chunk_ids (multiple edges sharing one evidence chunk)
// must not produce duplicate entries in the resolved list.
// kg.Subgraph already dedupes during construction, but the
// resolver enforces it defensively too — a future store change
// shouldn't silently inflate the RRF input.
func TestResolveGraphChunks_DeduplicatesChunkIDs(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{
		subgraph: kg.Subgraph{ChunkIDs: []string{"c1", "c2", "c1", "c3", "c2"}},
	}
	dec := GraphTraversalDecision{
		Fired:           true,
		MatchedEntities: []kg.Entity{{ID: 42}},
	}
	got := ResolveGraphChunks(context.Background(), store, "kb-1", dec, 15)
	if len(got) != 3 {
		t.Errorf("expected 3 unique chunks, got %d: %v", len(got), got)
	}
	for i, want := range []string{"c1", "c2", "c3"} {
		if got[i] != want {
			t.Errorf("[%d]: got %q, want %q (first-occurrence order)", i, got[i], want)
		}
	}
}

// ---------------------------------------------------------------------------
// ResolveGraphChunksIfEnabled — gate-aware wrapper
// ---------------------------------------------------------------------------

// TestResolveGraphChunksIfEnabled_GateOffReturnsNil: even when the
// router fired, the chunk-injection sub-flag must guard the
// LookupSubgraph round-trip. Operators running graph_routing in
// diagnostic mode (decision event only) MUST NOT pay the subgraph
// fetch cost.
func TestResolveGraphChunksIfEnabled_GateOffReturnsNil(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{
		subgraph: kg.Subgraph{ChunkIDs: []string{"c1", "c2"}},
	}
	r := &fakeSiteConfigReader{values: map[string]*string{
		// Diagnostic gate on, inject_chunks unset (default off).
		"chat_graph_routing_enabled": strPtr("true"),
	}}
	dec := GraphTraversalDecision{
		Fired:           true,
		MatchedEntities: []kg.Entity{{ID: 42, CanonicalName: "PPM"}},
	}
	got := ResolveGraphChunksIfEnabled(context.Background(), store, r, "kb-1", dec)
	if got != nil {
		t.Errorf("expected nil when inject_chunks gate is off, got %v", got)
	}
	if store.subgraphCalls != 0 {
		t.Errorf("LookupSubgraph called %d times despite gate being off", store.subgraphCalls)
	}
}

// TestResolveGraphChunksIfEnabled_NotFiredReturnsNilNoFetch: the
// decision wasn't fired (no entity match). The wrapper must
// short-circuit before checking the gate or hitting the store.
func TestResolveGraphChunksIfEnabled_NotFiredReturnsNilNoFetch(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{
		subgraph: kg.Subgraph{ChunkIDs: []string{"c1"}},
	}
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_graph_routing_inject_chunks": strPtr("true"),
	}}
	dec := GraphTraversalDecision{Fired: false, Outcome: "skipped_no_entity"}
	got := ResolveGraphChunksIfEnabled(context.Background(), store, r, "kb-1", dec)
	if got != nil {
		t.Errorf("expected nil when not fired, got %v", got)
	}
	if store.subgraphCalls != 0 {
		t.Errorf("LookupSubgraph called %d times despite not-fired", store.subgraphCalls)
	}
}

// TestResolveGraphChunksIfEnabled_BothOnResolves: the happy path —
// router fired AND inject_chunks gate on. Wrapper calls
// ResolveGraphChunks with the configured cap and returns the result.
func TestResolveGraphChunksIfEnabled_BothOnResolves(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{
		subgraph: kg.Subgraph{ChunkIDs: []string{"c1", "c2", "c3"}},
	}
	r := &fakeSiteConfigReader{values: map[string]*string{
		"chat_graph_routing_inject_chunks": strPtr("true"),
		"chat_graph_routing_max_chunks":    strPtr("2"), // explicit cap override
	}}
	dec := GraphTraversalDecision{
		Fired:           true,
		MatchedEntities: []kg.Entity{{ID: 42, CanonicalName: "PPM"}},
	}
	got := ResolveGraphChunksIfEnabled(context.Background(), store, r, "kb-1", dec)
	if len(got) != 2 {
		t.Errorf("expected 2 chunks (cap=2), got %d: %v", len(got), got)
	}
	if store.subgraphCalls != 1 {
		t.Errorf("expected 1 LookupSubgraph call, got %d", store.subgraphCalls)
	}
}

// ---------------------------------------------------------------------------
// ResolveGraphChunksIfEnabled dispatch tests (T1-4 / T1-5)
// ---------------------------------------------------------------------------
//
// These tests exercise the path_mode trichotomy: each mode routes to a
// distinct Store method, and the unselected methods must NOT be called.
// Cross-method counters on fakeKGStore make those negative assertions
// cheap and visible in failure output.

func TestResolveGraphChunksIfEnabled_PathModePPRRoutesToPPRChunks(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{pprChunks: []string{"c-ppr-1", "c-ppr-2"}}
	dec := GraphTraversalDecision{
		Fired:           true,
		MatchedEntities: []kg.Entity{{ID: 42, CanonicalName: "PPM"}},
	}
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_graph_routing_inject_chunks": strPtr("true"),
		"chat_graph_routing_path_mode":     strPtr("ppr"),
	}}
	got := ResolveGraphChunksIfEnabled(context.Background(), store, reader, "kb-1", dec)
	if len(got) != 2 || got[0] != "c-ppr-1" {
		t.Errorf("ppr mode: want [c-ppr-1 c-ppr-2], got %v", got)
	}
	if store.pprCalls != 1 {
		t.Errorf("ppr mode: want 1 PPRChunks call, got %d", store.pprCalls)
	}
	if store.subgraphCalls != 0 {
		t.Errorf("ppr mode must not touch LookupSubgraph; got %d calls", store.subgraphCalls)
	}
	if store.pathCalls != 0 {
		t.Errorf("ppr mode must not touch PathChunks; got %d calls", store.pathCalls)
	}
	if len(store.pprSeedIDs) != 1 || store.pprSeedIDs[0] != 42 {
		t.Errorf("ppr seeds: want [42], got %v", store.pprSeedIDs)
	}
}

func TestResolveGraphChunksIfEnabled_PathModePathsRoutesToPathChunks(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{pathChunks: []string{"c-p-1"}}
	dec := GraphTraversalDecision{
		Fired: true,
		MatchedEntities: []kg.Entity{
			{ID: 11, CanonicalName: "A"},
			{ID: 22, CanonicalName: "B"},
		},
	}
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_graph_routing_inject_chunks": strPtr("true"),
		"chat_graph_routing_path_mode":     strPtr("paths"),
	}}
	got := ResolveGraphChunksIfEnabled(context.Background(), store, reader, "kb-1", dec)
	if len(got) != 1 || got[0] != "c-p-1" {
		t.Errorf("paths mode: want [c-p-1], got %v", got)
	}
	if store.pathCalls != 1 {
		t.Errorf("paths mode: want 1 PathChunks call (single pair), got %d", store.pathCalls)
	}
	if store.subgraphCalls != 0 {
		t.Errorf("paths mode must not touch LookupSubgraph; got %d calls", store.subgraphCalls)
	}
	if store.pathSrcID != 11 || store.pathDstID != 22 {
		t.Errorf("paths mode src/dst: want (11,22), got (%d,%d)", store.pathSrcID, store.pathDstID)
	}
}

func TestResolveGraphChunksIfEnabled_PathsModeFallsBackToNeighborsWhenOnlyOneEntity(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{
		subgraph: kg.Subgraph{ChunkIDs: []string{"c-nb-1"}},
	}
	dec := GraphTraversalDecision{
		Fired:           true,
		MatchedEntities: []kg.Entity{{ID: 11, CanonicalName: "A"}},
	}
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_graph_routing_inject_chunks": strPtr("true"),
		"chat_graph_routing_path_mode":     strPtr("paths"),
	}}
	got := ResolveGraphChunksIfEnabled(context.Background(), store, reader, "kb-1", dec)
	if len(got) != 1 || got[0] != "c-nb-1" {
		t.Errorf("single-entity paths mode should fall back to neighbours, got %v", got)
	}
	if store.pathCalls != 0 {
		t.Errorf("paths fallback must not call PathChunks; got %d", store.pathCalls)
	}
	if store.subgraphCalls != 1 {
		t.Errorf("neighbours fallback expects 1 LookupSubgraph call, got %d", store.subgraphCalls)
	}
}

func TestResolveGraphChunksIfEnabled_PPREmptyResultFallsBackToNeighbors(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{
		pprChunks: nil, // PPR returns no chunks
		subgraph:  kg.Subgraph{ChunkIDs: []string{"c-nb-fallback"}},
	}
	dec := GraphTraversalDecision{
		Fired:           true,
		MatchedEntities: []kg.Entity{{ID: 42, CanonicalName: "PPM"}},
	}
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_graph_routing_inject_chunks": strPtr("true"),
		"chat_graph_routing_path_mode":     strPtr("ppr"),
	}}
	got := ResolveGraphChunksIfEnabled(context.Background(), store, reader, "kb-1", dec)
	if len(got) != 1 || got[0] != "c-nb-fallback" {
		t.Errorf("ppr-empty should fall back to neighbours, got %v", got)
	}
	if store.pprCalls != 1 {
		t.Errorf("ppr should have been attempted: got %d calls", store.pprCalls)
	}
	if store.subgraphCalls != 1 {
		t.Errorf("neighbours fallback expects 1 LookupSubgraph call, got %d", store.subgraphCalls)
	}
}

func TestResolveGraphChunksIfEnabled_DefaultModeIsNeighbors(t *testing.T) {
	t.Parallel()
	store := &fakeKGStore{
		subgraph: kg.Subgraph{ChunkIDs: []string{"c-nb"}},
	}
	dec := GraphTraversalDecision{
		Fired:           true,
		MatchedEntities: []kg.Entity{{ID: 42, CanonicalName: "PPM"}},
	}
	// path_mode unset → defaults to neighbours.
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_graph_routing_inject_chunks": strPtr("true"),
	}}
	got := ResolveGraphChunksIfEnabled(context.Background(), store, reader, "kb-1", dec)
	if len(got) != 1 || got[0] != "c-nb" {
		t.Errorf("default mode: want neighbours result, got %v", got)
	}
	if store.subgraphCalls != 1 {
		t.Errorf("default mode: expected 1 LookupSubgraph call, got %d", store.subgraphCalls)
	}
	if store.pprCalls != 0 || store.pathCalls != 0 {
		t.Errorf("default mode must not call PPR or Paths; ppr=%d path=%d", store.pprCalls, store.pathCalls)
	}
}

func TestChatGraphRoutingPathMode_UnknownNormalisesToNeighbors(t *testing.T) {
	t.Parallel()
	// "everything else" — typos, deprecated values, blank, mixed case
	// — must funnel back to the safe neighbours default.
	cases := []string{"", "BFS", "personalized_pagerank", "NEIGHBORS", "  ppr  ", "PPR"}
	for _, in := range cases {
		reader := &fakeSiteConfigReader{values: map[string]*string{
			"chat_graph_routing_path_mode": strPtr(in),
		}}
		got := ChatGraphRoutingPathMode(context.Background(), reader)
		want := "neighbors"
		// Whitespace + uppercase "ppr" must normalise to "ppr".
		switch in {
		case "  ppr  ", "PPR":
			want = "ppr"
		}
		if got != want {
			t.Errorf("path_mode(%q): want %q, got %q", in, want, got)
		}
	}
}

// TestTokeniseForGraph_Behaviour pins the documented contract.
// Drift here would silently change which queries trigger the
// heuristic. Each case is its own t.Run subtest so a failure
// reports the case name (and is selectable via -run) rather than
// a bare line number.
func TestTokeniseForGraph_Behaviour(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"hyphenated_compound", "PPM-Team und HRZ", []string{"PPM", "Team", "und", "HRZ"}},
		// "im" (<3 chars) drops out — the predicate filters short tokens.
		{"drops_short_tokens", "Eberhard Kurz arbeitet im PPM-Team", []string{"Eberhard", "Kurz", "arbeitet", "PPM", "Team"}},
		{"empty_input", "", nil},
		{"all_short_tokens", "a b c", nil},
		// case preserved (kg.Store lowercases at match time, not here)
		{"case_preserved", "foo Foo FOO", []string{"foo", "Foo", "FOO"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := tokeniseForGraph(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("%q: got %d tokens %v, want %d %v", c.in, len(got), got, len(c.want), c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("%q[%d]: got %q, want %q", c.in, i, got[i], c.want[i])
				}
			}
		})
	}
}
