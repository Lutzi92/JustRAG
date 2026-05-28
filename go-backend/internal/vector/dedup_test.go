package vector

import (
	"testing"
)

// helper to build a RankedDoc slice quickly
func docs(pairs ...interface{}) []RankedDoc {
	result := make([]RankedDoc, 0, len(pairs)/3)
	for i := 0; i+2 < len(pairs); i += 3 {
		result = append(result, RankedDoc{
			ID:      pairs[i].(string),
			Content: pairs[i+1].(string),
			Score:   pairs[i+2].(float64),
		})
	}
	return result
}

func ids(ds []RankedDoc) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.ID
	}
	return out
}

// Test 1: No duplicates — all docs retained.
func TestDeduplicate_NoDuplicates(t *testing.T) {
	t.Parallel()
	input := docs(
		"a", "the quick brown fox", 1.0,
		"b", "lorem ipsum dolor sit amet", 0.9,
		"c", "golang is a statically typed language", 0.8,
	)
	got := Deduplicate(input, 0.85)
	if len(got) != 3 {
		t.Errorf("expected 3 docs, got %d: %v", len(got), ids(got))
	}
}

// Test 2: Near-duplicate pair — lower-scored doc removed.
func TestDeduplicate_NearDuplicate(t *testing.T) {
	// "b" shares most words with "a" but has one different word; score is lower.
	t.Parallel()

	input := docs(
		"a", "the quick brown fox jumps over the lazy dog", 1.0,
		"b", "the quick brown fox jumps over the lazy cat", 0.7,
	)
	// Jaccard: intersection = {the,quick,brown,fox,jumps,over,lazy} = 7 unique shared
	// setA = {the,quick,brown,fox,jumps,over,lazy,dog} = 8
	// setB = {the,quick,brown,fox,jumps,over,lazy,cat} = 8
	// union = 8+8-7 = 9, similarity = 7/9 ≈ 0.78 — below 0.85.
	// Use a lower threshold to trigger dedup.
	got := Deduplicate(input, 0.75)
	if len(got) != 1 {
		t.Errorf("expected 1 doc after dedup, got %d: %v", len(got), ids(got))
	}
	if got[0].ID != "a" {
		t.Errorf("expected higher-scored doc 'a' to be kept, got %q", got[0].ID)
	}
}

// Test 3: Exact duplicate — only the higher-scored copy kept.
func TestDeduplicate_ExactDuplicate(t *testing.T) {
	t.Parallel()
	content := "machine learning is a subset of artificial intelligence"
	input := docs(
		"a", content, 0.95,
		"b", content, 0.60,
	)
	got := Deduplicate(input, 0.85)
	if len(got) != 1 {
		t.Errorf("expected 1 doc, got %d: %v", len(got), ids(got))
	}
	if got[0].ID != "a" {
		t.Errorf("expected doc 'a' (higher score) to be kept, got %q", got[0].ID)
	}
}

// Test 4: Empty input — empty output.
func TestDeduplicate_Empty(t *testing.T) {
	t.Parallel()
	got := Deduplicate([]RankedDoc{}, 0.85)
	if len(got) != 0 {
		t.Errorf("expected empty output, got %d docs", len(got))
	}
}

// Test 5: Single doc — returned as-is.
func TestDeduplicate_SingleDoc(t *testing.T) {
	t.Parallel()
	input := docs("a", "only one document here", 1.0)
	got := Deduplicate(input, 0.85)
	if len(got) != 1 {
		t.Errorf("expected 1 doc, got %d", len(got))
	}
	if got[0].ID != "a" {
		t.Errorf("expected doc 'a', got %q", got[0].ID)
	}
}

// Test 6: Threshold 0.0 — every doc after the first is considered a duplicate.
func TestDeduplicate_ThresholdZero(t *testing.T) {
	// With threshold=0.0, any non-empty overlap triggers dedup.
	// Use docs with at least one word in common so similarity > 0.
	t.Parallel()

	input := docs(
		"a", "apple banana cherry", 1.0,
		"b", "apple mango kiwi", 0.9,
		"c", "banana grape peach", 0.8,
	)
	got := Deduplicate(input, 0.0)
	// "b" shares "apple" with "a": sim > 0 → removed.
	// "c" shares "banana" with "a": sim > 0 → removed.
	if len(got) != 1 {
		t.Errorf("expected 1 doc with threshold=0.0, got %d: %v", len(got), ids(got))
	}
	if got[0].ID != "a" {
		t.Errorf("expected doc 'a' to be kept, got %q", got[0].ID)
	}
}

// Test 7: Threshold 1.0 — only exact duplicates (100% Jaccard) are removed.
func TestDeduplicate_ThresholdOne(t *testing.T) {
	t.Parallel()
	exact := "neural networks deep learning"
	input := docs(
		"a", exact, 1.0,
		"b", exact, 0.9, // exact duplicate
		"c", "neural networks deep learning models", 0.8, // one extra word — not exact
	)
	got := Deduplicate(input, 1.0)
	// "b" is exact duplicate of "a": sim=1.0, NOT > 1.0 so it's kept? No — > vs >=.
	// Spec says "> threshold", so sim=1.0 > 1.0 is false → "b" kept too.
	// Only sim strictly greater than 1.0 would remove — impossible.
	// So with threshold=1.0 nothing is removed.
	if len(got) != 3 {
		t.Errorf("expected 3 docs with threshold=1.0 (strict >), got %d: %v", len(got), ids(got))
	}
}

// Test 7b: Verify exact duplicates ARE removed at threshold just below 1.0.
func TestDeduplicate_ExactDuplicateRemovedBelowOne(t *testing.T) {
	t.Parallel()
	exact := "neural networks deep learning"
	input := docs(
		"a", exact, 1.0,
		"b", exact, 0.9,
		"c", "neural networks deep learning models", 0.8,
	)
	got := Deduplicate(input, 0.99)
	// "b" sim with "a" = 1.0 > 0.99 → removed
	// "c" sim with "a": intersection={neural,networks,deep,learning}=4, union=5 → 0.8 < 0.99 → kept
	if len(got) != 2 {
		t.Errorf("expected 2 docs with threshold=0.99, got %d: %v", len(got), ids(got))
	}
	resultIDs := ids(got)
	if resultIDs[0] != "a" || resultIDs[1] != "c" {
		t.Errorf("expected [a, c], got %v", resultIDs)
	}
}

// Regression: near-duplicate content from DIFFERENT files must not be
// collapsed. This is the "templated synthetic section across many project
// pages" case — each chunk is independent evidence for its own file.
func TestDeduplicate_DifferentFileIDsAreNeverDuplicates(t *testing.T) {
	t.Parallel()
	shared := "mentioned persons alice smith bob jones project overview team roster"
	input := []RankedDoc{
		{ID: "a", FileID: "f1", Content: shared + " alpha", Score: 0.95},
		{ID: "b", FileID: "f2", Content: shared + " beta", Score: 0.94},
		{ID: "c", FileID: "f3", Content: shared + " gamma", Score: 0.93},
	}
	// Jaccard is very high between any two. With content-only dedup, only
	// "a" would survive. With source-aware dedup, all three must survive
	// because each is from a different file.
	got := Deduplicate(input, 0.85)
	if len(got) != 3 {
		t.Fatalf("expected 3 cross-file docs preserved, got %d: %v", len(got), ids(got))
	}
}

// Same-file near-duplicates should still be removed — within-document
// redundancy is what Deduplicate exists to handle.
func TestDeduplicate_SameFileNearDuplicatesRemoved(t *testing.T) {
	t.Parallel()
	shared := "the quick brown fox jumps over the lazy dog"
	input := []RankedDoc{
		{ID: "a", FileID: "f1", Content: shared, Score: 0.95},
		{ID: "b", FileID: "f1", Content: shared, Score: 0.60},
	}
	got := Deduplicate(input, 0.85)
	if len(got) != 1 {
		t.Fatalf("expected 1 doc (same-file dedup), got %d: %v", len(got), ids(got))
	}
	if got[0].ID != "a" {
		t.Errorf("expected higher-scored 'a' to survive, got %q", got[0].ID)
	}
}

// Test: case-insensitive matching.
func TestDeduplicate_CaseInsensitive(t *testing.T) {
	t.Parallel()
	input := docs(
		"a", "The Quick Brown Fox", 1.0,
		"b", "the quick brown fox", 0.8,
	)
	got := Deduplicate(input, 0.99)
	if len(got) != 1 {
		t.Errorf("expected 1 doc (case-insensitive exact match), got %d: %v", len(got), ids(got))
	}
}
