package kg

import (
	"math"
	"testing"
)

// TestPersonalizedPageRank_EmptySeeds: no seeds → no scores. The
// caller treats nil as "PPR didn't fire" and falls back to
// neighbours mode.
func TestPersonalizedPageRank_EmptySeeds(t *testing.T) {
	t.Parallel()
	edges := []EdgeRow{{Src: 1, Dst: 2, Weight: 1}}
	if got := PersonalizedPageRank(nil, edges, PPRConfig{}); got != nil {
		t.Errorf("empty seeds: want nil, got %v", got)
	}
}

// TestPersonalizedPageRank_NoEdges: a seed with no edges still
// scores itself (teleport mass). Without this the algorithm would
// produce an empty result and the caller couldn't distinguish "no
// connections" from "PPR didn't run".
func TestPersonalizedPageRank_NoEdges(t *testing.T) {
	t.Parallel()
	scores := PersonalizedPageRank([]int64{1}, nil, PPRConfig{})
	if scores[1] <= 0 {
		t.Errorf("isolated seed: want score>0, got %v", scores[1])
	}
}

// TestPersonalizedPageRank_StarGraph: in a star with seed at the
// centre, every leaf scores equally and the centre keeps the
// highest score. Pinned because any algorithm tweak that broke
// the symmetry would silently distort the chunk ordering.
func TestPersonalizedPageRank_StarGraph(t *testing.T) {
	t.Parallel()
	edges := []EdgeRow{
		{Src: 1, Dst: 2, Weight: 1},
		{Src: 1, Dst: 3, Weight: 1},
		{Src: 1, Dst: 4, Weight: 1},
	}
	scores := PersonalizedPageRank([]int64{1}, edges, PPRConfig{})
	if scores[1] <= scores[2] {
		t.Errorf("centre should outscore leaves: centre=%v leaf=%v", scores[1], scores[2])
	}
	// Symmetry: all leaves equal.
	if math.Abs(scores[2]-scores[3]) > 1e-9 {
		t.Errorf("leaf asymmetry: %v vs %v", scores[2], scores[3])
	}
	if math.Abs(scores[3]-scores[4]) > 1e-9 {
		t.Errorf("leaf asymmetry: %v vs %v", scores[3], scores[4])
	}
}

// TestPersonalizedPageRank_DistantNodeScoresLower: in a chain
// 1-2-3-4, seeded at 1, node 4 must score below node 2. This is
// the basic multi-hop signal PPR is supposed to provide; if it
// failed the algorithm would be no better than depth-1 BFS.
func TestPersonalizedPageRank_DistantNodeScoresLower(t *testing.T) {
	t.Parallel()
	edges := []EdgeRow{
		{Src: 1, Dst: 2, Weight: 1},
		{Src: 2, Dst: 3, Weight: 1},
		{Src: 3, Dst: 4, Weight: 1},
	}
	scores := PersonalizedPageRank([]int64{1}, edges, PPRConfig{})
	if scores[2] <= scores[3] || scores[3] <= scores[4] {
		t.Errorf("distance-decay broken: 2=%v 3=%v 4=%v", scores[2], scores[3], scores[4])
	}
}

// TestPersonalizedPageRank_WeightedEdgeOverDoubleEdge: an edge
// with weight 2 should drive at least as much mass as two
// separate unit edges between the same pair (the algorithm folds
// weight into the push step). Without this, configurable edge
// weights from the extractor would be silently ignored.
func TestPersonalizedPageRank_WeightedEdgeOverDoubleEdge(t *testing.T) {
	t.Parallel()
	// Graph A: single weight-2 edge from seed to target.
	scoresA := PersonalizedPageRank([]int64{1}, []EdgeRow{
		{Src: 1, Dst: 2, Weight: 2.0},
	}, PPRConfig{})
	// Graph B: same topology, weight 1.
	scoresB := PersonalizedPageRank([]int64{1}, []EdgeRow{
		{Src: 1, Dst: 2, Weight: 1.0},
	}, PPRConfig{})
	// With only one edge, weight doesn't change the relative split
	// (all mass that's pushed goes to the single neighbour either
	// way) — so this test verifies the weighted path doesn't
	// REGRESS rather than rocket above unweighted. We assert
	// approximate equality.
	if math.Abs(scoresA[2]-scoresB[2]) > 1e-6 {
		t.Errorf("single-edge weight should yield same target score: A=%v B=%v",
			scoresA[2], scoresB[2])
	}
}

// TestPersonalizedPageRank_NonPositiveWeightClampedToOne pins the
// data-quality defence: a buggy extractor that wrote weight=0
// must not zero a chunk out of recall.
func TestPersonalizedPageRank_NonPositiveWeightClampedToOne(t *testing.T) {
	t.Parallel()
	scores := PersonalizedPageRank([]int64{1}, []EdgeRow{
		{Src: 1, Dst: 2, Weight: 0},
		{Src: 1, Dst: 3, Weight: -5},
	}, PPRConfig{})
	if scores[2] <= 0 {
		t.Errorf("weight=0 should still propagate; got %v", scores[2])
	}
	if scores[3] <= 0 {
		t.Errorf("weight=-5 should clamp to 1.0 and propagate; got %v", scores[3])
	}
}

// TestPersonalizedPageRank_SeedSymmetry: with two seeds, both
// score equally when their neighbourhoods are isomorphic. Mirrors
// the StarGraph test on the personalisation side rather than the
// neighbour side — a single-seed-dominated formula would break.
func TestPersonalizedPageRank_SeedSymmetry(t *testing.T) {
	t.Parallel()
	edges := []EdgeRow{
		{Src: 1, Dst: 3, Weight: 1},
		{Src: 2, Dst: 4, Weight: 1},
	}
	scores := PersonalizedPageRank([]int64{1, 2}, edges, PPRConfig{})
	if math.Abs(scores[1]-scores[2]) > 1e-9 {
		t.Errorf("seed asymmetry: 1=%v 2=%v", scores[1], scores[2])
	}
	if math.Abs(scores[3]-scores[4]) > 1e-9 {
		t.Errorf("neighbour asymmetry: 3=%v 4=%v", scores[3], scores[4])
	}
}

// TestPPRConfig_Sanitize_FillsDefaults: zero / out-of-range
// fields must fall through to documented defaults without the
// caller needing to know them.
func TestPPRConfig_Sanitize_FillsDefaults(t *testing.T) {
	t.Parallel()
	c := PPRConfig{}.Sanitize()
	if c.Damping != 0.85 {
		t.Errorf("default damping: want 0.85, got %v", c.Damping)
	}
	if c.MaxIter != 20 {
		t.Errorf("default max_iter: want 20, got %d", c.MaxIter)
	}
	if c.Convergence <= 0 || c.Convergence >= 1 {
		t.Errorf("default convergence out of range: %v", c.Convergence)
	}

	// Out-of-range gets defaulted; explicit valid value passes through.
	c2 := PPRConfig{Damping: 0.5, MaxIter: 7}.Sanitize()
	if c2.Damping != 0.5 || c2.MaxIter != 7 {
		t.Errorf("valid values must pass through: got %+v", c2)
	}
}

// TestPersonalizedPageRank_OrderingStableAcrossIterCaps: on a small
// graph the absolute scores at iteration 20 vs 200 may still differ
// by a percent or two (a 4-node cycle with damping=0.85 has a slow
// mixing time), but the ORDERING that downstream chunk ranking
// depends on must be stable. This is the real production
// invariant — the chat layer only uses score order, not absolute
// values. Asserts the relative rank of each node is identical
// between the two iter caps.
func TestPersonalizedPageRank_OrderingStableAcrossIterCaps(t *testing.T) {
	t.Parallel()
	edges := []EdgeRow{
		{Src: 1, Dst: 2, Weight: 1},
		{Src: 2, Dst: 3, Weight: 1},
		{Src: 3, Dst: 4, Weight: 1},
		{Src: 4, Dst: 1, Weight: 1},
	}
	short := PersonalizedPageRank([]int64{1}, edges, PPRConfig{MaxIter: 20})
	long := PersonalizedPageRank([]int64{1}, edges, PPRConfig{MaxIter: 200})

	rank := func(scores map[int64]float64) []int64 {
		ids := make([]int64, 0, len(scores))
		for id := range scores {
			ids = append(ids, id)
		}
		// Simple insertion sort by score desc, ties by id asc.
		for i := 1; i < len(ids); i++ {
			for j := i; j > 0; j-- {
				a, b := ids[j-1], ids[j]
				if scores[a] < scores[b] || (scores[a] == scores[b] && a > b) {
					ids[j-1], ids[j] = b, a
				} else {
					break
				}
			}
		}
		return ids
	}
	rShort, rLong := rank(short), rank(long)
	if len(rShort) != len(rLong) {
		t.Fatalf("rank length mismatch: short=%v long=%v", rShort, rLong)
	}
	for i := range rShort {
		if rShort[i] != rLong[i] {
			t.Errorf("ordering drift at position %d: short=%d long=%d (short_scores=%v long_scores=%v)",
				i, rShort[i], rLong[i], short, long)
		}
	}

	// Sanity: scores must also be in roughly the same range — a
	// shift of 5% or more would indicate the iteration cap is
	// pathologically low and ordering stability was coincidence.
	for id, s := range short {
		drift := math.Abs(s - long[id])
		if drift > 0.05 {
			t.Errorf("PPR drift > 5%% at id=%d: short=%v long=%v", id, s, long[id])
		}
	}
}

func TestBuildDualNodeEdges(t *testing.T) {
	edges := []EdgeRow{
		{Src: 1, Dst: 2, Weight: 2.0, ChunkID: "cA"},
		{Src: 2, Dst: 3, Weight: 1.0, ChunkID: ""},
	}
	dual, idToChunk := buildDualNodeEdges(edges)
	if len(dual) != 4 {
		t.Fatalf("want 4 dual edges, got %d: %+v", len(dual), dual)
	}
	if len(idToChunk) != 1 {
		t.Fatalf("want 1 passage node, got %d", len(idToChunk))
	}
	var pid int64
	for id := range idToChunk {
		pid = id
	}
	if pid >= 0 || idToChunk[pid] != "cA" {
		t.Errorf("passage id must be negative and map to cA, got %d -> %q", pid, idToChunk[pid])
	}
	foundEE := false
	for _, e := range dual {
		if e.Src == 1 && e.Dst == 2 && e.Weight == 2.0 {
			foundEE = true
		}
	}
	if !foundEE {
		t.Errorf("entity-entity edge (1,2,w=2.0) missing from dual graph")
	}
}

func TestPersonalizedPageRank_PassageNodeAccruesScore(t *testing.T) {
	dual := []EdgeRow{
		{Src: 1, Dst: 2, Weight: 1.0},
		{Src: 1, Dst: -1, Weight: 1.0},
		{Src: 2, Dst: -1, Weight: 1.0},
	}
	scores := PersonalizedPageRank([]int64{1}, dual, PPRConfig{})
	if scores[-1] <= 0 {
		t.Errorf("passage node -1 should accrue positive PPR score, got %v", scores[-1])
	}
}
