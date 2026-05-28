package kg

import (
	"testing"
)

// TestPathConfig_Sanitize_FillsDefaults: documented defaults
// match the chat-side flag defaults. A drift between the two
// would silently make production behaviour diverge from what the
// snapshot captures.
func TestPathConfig_Sanitize_FillsDefaults(t *testing.T) {
	t.Parallel()
	c := PathConfig{}.Sanitize()
	if c.MaxLen != 3 {
		t.Errorf("default max_len: want 3, got %d", c.MaxLen)
	}
	if c.MaxPaths != 5 {
		t.Errorf("default max_paths: want 5, got %d", c.MaxPaths)
	}

	c2 := PathConfig{MaxLen: 2, MaxPaths: 10}.Sanitize()
	if c2.MaxLen != 2 || c2.MaxPaths != 10 {
		t.Errorf("valid values must pass through: got %+v", c2)
	}
}

// TestBuildPath_DedupesChunks: a path that traverses two edges
// extracted from the same chunk yields a single chunk_id in the
// output. Without dedup the chat layer would inject the same
// chunk twice and waste an RRF slot.
func TestBuildPath_DedupesChunks(t *testing.T) {
	t.Parallel()
	p := buildPath([]EdgeRow{
		{Src: 1, Dst: 2, ChunkID: "c1"},
		{Src: 2, Dst: 3, ChunkID: "c1"}, // same chunk
		{Src: 3, Dst: 4, ChunkID: "c2"},
	})
	if p.Length != 3 {
		t.Errorf("length: want 3, got %d", p.Length)
	}
	if len(p.ChunkIDs) != 2 {
		t.Errorf("dedup failed: got %v", p.ChunkIDs)
	}
	// Order preserved: first-seen wins.
	if p.ChunkIDs[0] != "c1" || p.ChunkIDs[1] != "c2" {
		t.Errorf("order broken: %v", p.ChunkIDs)
	}
}

// TestBuildPath_SkipsEmptyChunks: an edge without chunk_id (no
// evidence row, e.g. a derived relation) doesn't contribute to
// the chunk list but still counts toward Length.
func TestBuildPath_SkipsEmptyChunks(t *testing.T) {
	t.Parallel()
	p := buildPath([]EdgeRow{
		{Src: 1, Dst: 2, ChunkID: ""},
		{Src: 2, Dst: 3, ChunkID: "c1"},
	})
	if p.Length != 2 {
		t.Errorf("length: want 2, got %d", p.Length)
	}
	if len(p.ChunkIDs) != 1 || p.ChunkIDs[0] != "c1" {
		t.Errorf("empty-chunk skip failed: %v", p.ChunkIDs)
	}
}

// TestBuildPath_ScoreInverseLength: PathRAG's S(P) = 1/len(P) so
// a 1-hop path scores higher than a 3-hop path. Pinned because the
// downstream chunk-ranking aggregates by score and any drift here
// would silently flip the chunk ordering.
func TestBuildPath_ScoreInverseLength(t *testing.T) {
	t.Parallel()
	short := buildPath([]EdgeRow{{Src: 1, Dst: 2}})
	long := buildPath([]EdgeRow{{Src: 1, Dst: 2}, {Src: 2, Dst: 3}, {Src: 3, Dst: 4}})
	if short.Score <= long.Score {
		t.Errorf("short path should outscore long: short=%v long=%v", short.Score, long.Score)
	}
	if short.Score != 1.0 {
		t.Errorf("len=1 score: want 1.0, got %v", short.Score)
	}
}

// TestTotalWeight_AggregatesCorrectly: tiebreaker correctness for
// the path sort. Two paths of the same length should rank by
// total weight, heavier first.
func TestTotalWeight_AggregatesCorrectly(t *testing.T) {
	t.Parallel()
	w := totalWeight([]EdgeRow{
		{Src: 1, Dst: 2, Weight: 0.5},
		{Src: 2, Dst: 3, Weight: 2.0},
	})
	if w != 2.5 {
		t.Errorf("sum failed: want 2.5, got %v", w)
	}
}
