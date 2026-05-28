package vector

import (
	"math"
	"testing"
)

const floatTolerance = 1e-9

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < floatTolerance
}

// docByID returns the first RankedDoc with the given ID, plus a found flag.
func docByID(docs []RankedDoc, id string) (RankedDoc, bool) {
	for _, d := range docs {
		if d.ID == id {
			return d, true
		}
	}
	return RankedDoc{}, false
}

// TestFuseRRF_OverlappingLists verifies that a shared document accumulates a
// higher RRF score than documents that appear in only one list.
func TestFuseRRF_OverlappingLists(t *testing.T) {
	t.Parallel()
	list1 := []RankedDoc{
		{ID: "a", VectorScore: 0.9},
		{ID: "b", VectorScore: 0.5},
	}
	list2 := []RankedDoc{
		{ID: "a", VectorScore: 0.8},
		{ID: "c", VectorScore: 0.4},
	}

	k := 10
	result := FuseRRF(k, list1, list2)

	docA, ok := docByID(result, "a")
	if !ok {
		t.Fatal("expected doc 'a' in result")
	}
	docB, ok := docByID(result, "b")
	if !ok {
		t.Fatal("expected doc 'b' in result")
	}
	docC, ok := docByID(result, "c")
	if !ok {
		t.Fatal("expected doc 'c' in result")
	}

	// doc "a" appears at rank 0 in both lists → score = 1/(10+0+1) + 1/(10+0+1)
	expectedA := 2.0 / float64(k+1)
	if !approxEqual(docA.Score, expectedA) {
		t.Errorf("doc 'a' score: got %v, want %v", docA.Score, expectedA)
	}

	// doc "a" should have the highest VectorScore (0.9)
	if !approxEqual(docA.VectorScore, 0.9) {
		t.Errorf("doc 'a' VectorScore: got %v, want 0.9", docA.VectorScore)
	}

	// shared doc "a" must outrank "b" and "c"
	if docA.Score <= docB.Score {
		t.Errorf("doc 'a' (%.4f) should score higher than doc 'b' (%.4f)", docA.Score, docB.Score)
	}
	if docA.Score <= docC.Score {
		t.Errorf("doc 'a' (%.4f) should score higher than doc 'c' (%.4f)", docA.Score, docC.Score)
	}

	// result must be sorted descending
	if result[0].Score < result[len(result)-1].Score {
		t.Error("result is not sorted descending by score")
	}
}

// TestFuseRRF_DisjointLists verifies that all documents from disjoint lists appear
// in the result.
func TestFuseRRF_DisjointLists(t *testing.T) {
	t.Parallel()
	list1 := []RankedDoc{
		{ID: "x"},
		{ID: "y"},
	}
	list2 := []RankedDoc{
		{ID: "p"},
		{ID: "q"},
	}

	result := FuseRRF(60, list1, list2)

	if len(result) != 4 {
		t.Fatalf("expected 4 docs, got %d", len(result))
	}
	for _, id := range []string{"x", "y", "p", "q"} {
		if _, ok := docByID(result, id); !ok {
			t.Errorf("doc '%s' missing from result", id)
		}
	}
}

// TestFuseRRF_StandardK verifies exact RRF scores with k=60 (the standard value).
func TestFuseRRF_StandardK(t *testing.T) {
	t.Parallel()
	k := 60
	list := []RankedDoc{
		{ID: "first"},
		{ID: "second"},
		{ID: "third"},
	}

	result := FuseRRF(k, list)

	// rank 0 → 1/61, rank 1 → 1/62, rank 2 → 1/63
	expected := map[string]float64{
		"first":  1.0 / 61.0,
		"second": 1.0 / 62.0,
		"third":  1.0 / 63.0,
	}
	for id, want := range expected {
		doc, ok := docByID(result, id)
		if !ok {
			t.Fatalf("doc '%s' missing", id)
		}
		if !approxEqual(doc.Score, want) {
			t.Errorf("doc '%s' score: got %.10f, want %.10f", id, doc.Score, want)
		}
	}

	// Sorted descending
	for i := 1; i < len(result); i++ {
		if result[i-1].Score < result[i].Score {
			t.Errorf("result not sorted: result[%d].Score (%.4f) < result[%d].Score (%.4f)",
				i-1, result[i-1].Score, i, result[i].Score)
		}
	}
}

// TestBlendRerankScores_HalfHalfBlendNormalizesBothSides verifies that
// alpha=0.5 produces a genuine 50/50 blend: BOTH the RRF score and the
// rerank score are independently min-max-normalized to [0,1] before
// blending. Without this, rerankers that emit scores outside [0,1] (logit
// outputs, dot-product scores, …) would dominate the blend asymmetrically
// and alpha would no longer mean what it says.
func TestBlendRerankScores_HalfHalfBlendNormalizesBothSides(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", Score: 0.8},
		{ID: "b", Score: 0.4},
	}
	// Rerank scores in an arbitrary range (not [0,1]) to prove we
	// normalize. Old behavior would treat these as raw weights and skew
	// the blend.
	rerankScores := map[int]float64{
		0: 0.6, // min after normalization → 0.0
		1: 0.9, // max after normalization → 1.0
	}

	result := BlendRerankScores(docs, rerankScores, 0.5)

	if len(result) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(result))
	}

	docA, ok := docByID(result, "a")
	if !ok {
		t.Fatal("doc 'a' missing")
	}
	docB, ok := docByID(result, "b")
	if !ok {
		t.Fatal("doc 'b' missing")
	}

	// Both sides normalized → mix is symmetric.
	// a: normalizedRRF=1.0 (top), normalizedRerank=0.0 (bottom) → 0.5
	// b: normalizedRRF=0.0 (bottom), normalizedRerank=1.0 (top) → 0.5
	wantA := 0.5*0.0 + 0.5*1.0 // 0.5
	wantB := 0.5*1.0 + 0.5*0.0 // 0.5

	if !approxEqual(docA.Score, wantA) {
		t.Errorf("doc 'a' blended score: got %v, want %v", docA.Score, wantA)
	}
	if !approxEqual(docB.Score, wantB) {
		t.Errorf("doc 'b' blended score: got %v, want %v", docB.Score, wantB)
	}
	// RerankScore field still holds the RAW reranker value for telemetry.
	if !approxEqual(docA.RerankScore, 0.6) {
		t.Errorf("doc 'a' RerankScore (raw): got %v, want 0.6", docA.RerankScore)
	}
	if !approxEqual(docB.RerankScore, 0.9) {
		t.Errorf("doc 'b' RerankScore (raw): got %v, want 0.9", docB.RerankScore)
	}

	// Result must be sorted descending by blended score (stable sort on tie).
	if result[0].Score < result[1].Score {
		t.Error("result not sorted descending after blending")
	}
}

// TestBlendRerankScores_NormalizesRerankerWithHighDynamicRange covers the
// real-world case the normalization fix was built for: a reranker that
// emits scores in [-5, +5] (raw logit output). Under the old blend,
// alpha=0.5 would be dominated by the high-range side regardless of the
// RRF signal; under the normalized blend, the two signals contribute
// equally as alpha promises.
func TestBlendRerankScores_NormalizesRerankerWithHighDynamicRange(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", Score: 0.016}, // high RRF
		{ID: "b", Score: 0.005}, // low RRF
	}
	rerankScores := map[int]float64{
		0: -4.2, // reranker hates doc a → min after normalization
		1: 3.8,  // reranker loves doc b → max after normalization
	}

	result := BlendRerankScores(docs, rerankScores, 0.5)

	// After normalization:
	//   a: rrf=1.0, rerank=0.0 → 0.5
	//   b: rrf=0.0, rerank=1.0 → 0.5
	// With old behavior (no rerank normalization):
	//   a: rrf=1.0, rerank=-4.2 → 0.5*(-4.2) + 0.5*1.0 = -1.6
	//   b: rrf=0.0, rerank=3.8 → 0.5*3.8 + 0.5*0.0 = 1.9
	// — the old behavior would swing hard toward the reranker's opinion.
	docA, _ := docByID(result, "a")
	docB, _ := docByID(result, "b")

	if !approxEqual(docA.Score, 0.5) || !approxEqual(docB.Score, 0.5) {
		t.Errorf("expected both docs at 0.5 after normalized blend, got a=%v b=%v", docA.Score, docB.Score)
	}
}

// TestBlendRerankScores_PureRerank verifies that alpha=1.0 uses the rerank
// score verbatim (no RRF normalization) and re-sorts accordingly. The
// reranker disagreeing with RRF order should now win.
func TestBlendRerankScores_PureRerank(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", Score: 0.8}, // top of RRF order
		{ID: "b", Score: 0.4},
	}
	rerankScores := map[int]float64{
		0: 0.2, // RRF-top doc gets a poor rerank score
		1: 0.95,
	}

	result := BlendRerankScores(docs, rerankScores, 1.0)

	if len(result) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(result))
	}

	// alpha=1.0 → final score equals rerank score.
	if !approxEqual(result[0].Score, 0.95) {
		t.Errorf("top result score: got %v, want 0.95", result[0].Score)
	}
	if result[0].ID != "b" {
		t.Errorf("expected doc 'b' to win after pure rerank, got %q", result[0].ID)
	}
	if !approxEqual(result[1].Score, 0.2) {
		t.Errorf("bottom result score: got %v, want 0.2", result[1].Score)
	}
}

// TestBlendRerankScores_PureRRF verifies alpha=0.0 ignores rerank scores
// for the final ordering, but the RerankScore field is still populated for
// observability.
func TestBlendRerankScores_PureRRF(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", Score: 0.8},
		{ID: "b", Score: 0.4},
	}
	rerankScores := map[int]float64{
		0: 0.1,
		1: 0.99,
	}

	result := BlendRerankScores(docs, rerankScores, 0.0)

	// alpha=0.0 → final score is normalized RRF only.
	// docA: normalized=1.0, docB: normalized=0.0
	if result[0].ID != "a" {
		t.Errorf("expected RRF order preserved, got %q first", result[0].ID)
	}
	if !approxEqual(result[0].Score, 1.0) {
		t.Errorf("top result normalized score: got %v, want 1.0", result[0].Score)
	}
	if !approxEqual(result[1].Score, 0.0) {
		t.Errorf("bottom result normalized score: got %v, want 0.0", result[1].Score)
	}
	// RerankScore field still set even though it didn't influence ranking.
	docA, _ := docByID(result, "a")
	if !approxEqual(docA.RerankScore, 0.1) {
		t.Errorf("doc 'a' RerankScore: got %v, want 0.1", docA.RerankScore)
	}
}

// TestApplyScoreDrop_DropsTrailingLow verifies that docs with a sharp score drop
// are removed.
func TestApplyScoreDrop_DropsTrailingLow(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", Score: 1.0},
		{ID: "b", Score: 0.9},
		{ID: "c", Score: 0.1}, // drops sharply below 0.35 * 1.0 = 0.35
	}

	result := ApplyScoreDrop(docs, 0.35)

	if len(result) != 2 {
		t.Errorf("expected 2 docs after drop, got %d", len(result))
	}
	if result[0].ID != "a" || result[1].ID != "b" {
		t.Errorf("unexpected docs in result: %v", result)
	}
}

// TestApplyScoreDrop_NoCutWhenClose verifies that no truncation occurs when all
// scores are within the threshold of the top score.
func TestApplyScoreDrop_NoCutWhenClose(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", Score: 1.0},
		{ID: "b", Score: 0.8},
		{ID: "c", Score: 0.7},
	}

	// threshold = 0.35: 0.35 * 1.0 = 0.35; all scores are above 0.35
	result := ApplyScoreDrop(docs, 0.35)

	if len(result) != 3 {
		t.Errorf("expected all 3 docs retained, got %d", len(result))
	}
}

// TestApplyMinThreshold verifies that docs with scores below the minimum are removed.
func TestApplyMinThreshold(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", Score: 0.9},
		{ID: "b", Score: 0.5},
		{ID: "c", Score: 0.2},
		{ID: "d", Score: 0.0},
	}

	result := ApplyMinThreshold(docs, 0.5)

	if len(result) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(result))
	}
	for _, doc := range result {
		if doc.Score < 0.5 {
			t.Errorf("doc '%s' with score %.2f should have been filtered", doc.ID, doc.Score)
		}
	}
}

// ---------------------------------------------------------------------------
// ApplyBM25Floor
// ---------------------------------------------------------------------------

// TestApplyBM25Floor_ReinsertsMissingFiles is the scenario the floor was
// built for: BM25 found chunks in 4 distinct files (exact-keyword evidence),
// the reranker/MMR discarded 2 of them. The floor must reinsert the missing
// ones, displacing the lowest-scored tail entries of final.
func TestApplyBM25Floor_ReinsertsMissingFiles(t *testing.T) {
	// Final has 5 chunks from files F1, F2, F6, F7, F8. MMR picked these
	// after the reranker devalued the team-list chunks from F3 and F4.
	t.Parallel()

	final := []RankedDoc{
		{ID: "c1", FileID: "F1", Score: 0.9},
		{ID: "c2", FileID: "F2", Score: 0.8},
		{ID: "c6", FileID: "F6", Score: 0.6},
		{ID: "c7", FileID: "F7", Score: 0.5},
		{ID: "c8", FileID: "F8", Score: 0.3},
	}
	// BM25 ordered these 4 files on top: F1, F2, F3, F4.
	keywordDocs := []RankedDoc{
		{ID: "c1", FileID: "F1", Score: 10},
		{ID: "c2", FileID: "F2", Score: 9},
		{ID: "c3", FileID: "F3", Score: 8},
		{ID: "c4", FileID: "F4", Score: 7},
	}
	// Snapshot has post-dedup blended scores for every chunk the search
	// pipeline considered. c3 and c4 are present with moderate scores.
	snapshot := map[string]RankedDoc{
		"c1": {ID: "c1", FileID: "F1", Score: 0.9},
		"c2": {ID: "c2", FileID: "F2", Score: 0.8},
		"c3": {ID: "c3", FileID: "F3", Score: 0.4},
		"c4": {ID: "c4", FileID: "F4", Score: 0.35},
	}

	result, reinserted := ApplyBM25Floor(final, keywordDocs, snapshot, 5, 4)

	if reinserted != 2 {
		t.Fatalf("expected 2 reinsertions for F3/F4, got %d", reinserted)
	}
	if len(result) != 5 {
		t.Fatalf("expected length to stay at limit=5, got %d", len(result))
	}
	// All four BM25 top files must be present.
	seen := map[string]bool{}
	for _, d := range result {
		seen[d.FileID] = true
	}
	for _, want := range []string{"F1", "F2", "F3", "F4"} {
		if !seen[want] {
			t.Errorf("expected file %s in final, got files %v", want, seen)
		}
	}
	// Result must be score-sorted descending after reinsertion.
	for i := 1; i < len(result); i++ {
		if result[i].Score > result[i-1].Score {
			t.Errorf("result not score-sorted at index %d: %.3f > %.3f", i, result[i].Score, result[i-1].Score)
		}
	}
}

// TestApplyBM25Floor_NoOpWhenAllPresent: when every BM25 top file is already
// in final, the floor must not mutate anything or report reinsertions.
func TestApplyBM25Floor_NoOpWhenAllPresent(t *testing.T) {
	t.Parallel()
	final := []RankedDoc{
		{ID: "c1", FileID: "F1", Score: 0.9},
		{ID: "c2", FileID: "F2", Score: 0.8},
	}
	keywordDocs := []RankedDoc{
		{ID: "c1", FileID: "F1", Score: 10},
		{ID: "c2", FileID: "F2", Score: 9},
	}
	snapshot := map[string]RankedDoc{
		"c1": final[0],
		"c2": final[1],
	}
	result, reinserted := ApplyBM25Floor(final, keywordDocs, snapshot, 5, 4)

	if reinserted != 0 {
		t.Errorf("expected 0 reinsertions, got %d", reinserted)
	}
	if len(result) != 2 {
		t.Errorf("expected length 2, got %d", len(result))
	}
}

// TestApplyBM25Floor_AppendsWhenUnderLimit: when final is under limit, the
// reinsertions are appended rather than replacing the tail.
func TestApplyBM25Floor_AppendsWhenUnderLimit(t *testing.T) {
	t.Parallel()
	final := []RankedDoc{
		{ID: "c1", FileID: "F1", Score: 0.9},
	}
	keywordDocs := []RankedDoc{
		{ID: "c1", FileID: "F1", Score: 10},
		{ID: "c2", FileID: "F2", Score: 9},
	}
	snapshot := map[string]RankedDoc{
		"c1": final[0],
		"c2": {ID: "c2", FileID: "F2", Score: 0.5},
	}
	result, reinserted := ApplyBM25Floor(final, keywordDocs, snapshot, 10, 4)

	if reinserted != 1 {
		t.Fatalf("expected 1 reinsertion, got %d", reinserted)
	}
	if len(result) != 2 {
		t.Fatalf("expected length 2 after append, got %d", len(result))
	}
}

// TestApplyBM25Floor_RespectsMaxFiles caps the reinsertions at maxFiles even
// when more BM25 top files are missing.
func TestApplyBM25Floor_RespectsMaxFiles(t *testing.T) {
	t.Parallel()
	final := []RankedDoc{
		{ID: "other", FileID: "Z", Score: 0.9},
	}
	// BM25 has 6 distinct files; budget is 2 → only top 2 get considered.
	keywordDocs := []RankedDoc{
		{ID: "c1", FileID: "F1", Score: 10},
		{ID: "c2", FileID: "F2", Score: 9},
		{ID: "c3", FileID: "F3", Score: 8},
		{ID: "c4", FileID: "F4", Score: 7},
		{ID: "c5", FileID: "F5", Score: 6},
		{ID: "c6", FileID: "F6", Score: 5},
	}
	snapshot := map[string]RankedDoc{
		"c1": {ID: "c1", FileID: "F1", Score: 0.8},
		"c2": {ID: "c2", FileID: "F2", Score: 0.7},
		"c3": {ID: "c3", FileID: "F3", Score: 0.6},
		"c4": {ID: "c4", FileID: "F4", Score: 0.5},
		"c5": {ID: "c5", FileID: "F5", Score: 0.4},
		"c6": {ID: "c6", FileID: "F6", Score: 0.3},
	}
	result, reinserted := ApplyBM25Floor(final, keywordDocs, snapshot, 10, 2)

	if reinserted != 2 {
		t.Fatalf("expected 2 reinsertions (bounded by maxFiles), got %d", reinserted)
	}
	seen := map[string]bool{}
	for _, d := range result {
		seen[d.FileID] = true
	}
	if !seen["F1"] || !seen["F2"] {
		t.Errorf("expected F1 and F2 (top 2 by BM25) reinserted, got files %v", seen)
	}
	if seen["F3"] || seen["F4"] || seen["F5"] || seen["F6"] {
		t.Errorf("budget exceeded — only F1/F2 should have been reinserted, got files %v", seen)
	}
}

// TestApplyBM25Floor_ReinsertsWhenFilePresentButWrongChunk: the subtle case
// that motivated chunk-level (vs file-level) checking. A project file has
// a description chunk AND a team-list chunk. Reranker keeps the
// description chunk (good Q/A answer-shape), discards the team-list chunk
// (structural, rerank-devalued). File is "represented" in final but the
// actual named-entity mention — the team-list chunk — is gone. The floor
// must reinsert the team-list chunk because its chunk ID is missing, not
// skip because the file is nominally present.
func TestApplyBM25Floor_ReinsertsWhenFilePresentButWrongChunk(t *testing.T) {
	// Final keeps the project-description chunk from each of 3 files;
	// the team-list chunks (c1_team, c2_team, c3_team) got dropped.
	t.Parallel()

	final := []RankedDoc{
		{ID: "c1_desc", FileID: "F1", Score: 0.9},
		{ID: "c2_desc", FileID: "F2", Score: 0.8},
		{ID: "c3_desc", FileID: "F3", Score: 0.7},
	}
	// BM25 confidently ranked the team-list chunks first because they
	// exact-match the person's name.
	keywordDocs := []RankedDoc{
		{ID: "c1_team", FileID: "F1", Score: 10},
		{ID: "c2_team", FileID: "F2", Score: 9.5},
		{ID: "c3_team", FileID: "F3", Score: 9},
	}
	snapshot := map[string]RankedDoc{
		"c1_team": {ID: "c1_team", FileID: "F1", Score: 0.5},
		"c2_team": {ID: "c2_team", FileID: "F2", Score: 0.45},
		"c3_team": {ID: "c3_team", FileID: "F3", Score: 0.4},
		"c1_desc": final[0],
		"c2_desc": final[1],
		"c3_desc": final[2],
	}

	result, reinserted := ApplyBM25Floor(final, keywordDocs, snapshot, 5, 4)

	if reinserted != 3 {
		t.Fatalf("expected 3 reinsertions (team-list chunks missing even though files are present), got %d", reinserted)
	}
	// The team-list chunk IDs must all be in the result.
	ids := map[string]bool{}
	for _, d := range result {
		ids[d.ID] = true
	}
	for _, want := range []string{"c1_team", "c2_team", "c3_team"} {
		if !ids[want] {
			t.Errorf("expected team-list chunk %s to be reinserted, got IDs %v", want, ids)
		}
	}
}

// TestBM25FloorMaxFilesFor: the adaptive cap must scale with the caller's
// result-set limit so a person in many projects isn't truncated to 4, while
// never exceeding half of `limit` (so the reranker stays in the game).
func TestBM25FloorMaxFilesFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		limit, want int
		reason      string
	}{
		{0, 4, "degenerate limit still gets the minimum floor"},
		{4, 4, "min floor applies when limit/2 < 4"},
		{7, 4, "min floor still applies at limit=7 (7/2=3)"},
		{8, 4, "exactly at boundary"},
		{10, 5, "above boundary — scale with limit"},
		{30, 15, "default limit — 15 slots protected, 15 for reranker"},
		{60, 30, "large limit — scales proportionally"},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			t.Parallel()
			got := BM25FloorMaxFilesFor(c.limit)
			if got != c.want {
				t.Errorf("BM25FloorMaxFilesFor(%d) = %d, want %d", c.limit, got, c.want)
			}
		})
	}
}

// TestApplyBM25Floor_DisplacementPreservesOtherBM25Files covers the bug
// observed in production: naive tail-replace dropped the SOLE
// representative of another BM25 top file that happened to be at MMR's
// tail, so net effect was "we reinserted F3 but lost F7" and
// pre_seeded_candidates came back 8 for keyword_files=9. The fix
// preferentially drops non-BM25-file entries before touching BM25 ones.
func TestApplyBM25Floor_DisplacementPreservesOtherBM25Files(t *testing.T) {
	// Final at limit=5. F1 (BM25-top) and F6..F8 (non-BM25) are in
	// final; the tail entry is F2 (also BM25), which a naive tail-drop
	// would remove when reinserting F3.
	t.Parallel()

	final := []RankedDoc{
		{ID: "c1_top", FileID: "F1", Score: 0.9}, // BM25-top chunk of F1
		{ID: "c6", FileID: "F6", Score: 0.8},
		{ID: "c7", FileID: "F7", Score: 0.6},
		{ID: "c8", FileID: "F8", Score: 0.5},
		{ID: "c2_tail", FileID: "F2", Score: 0.3}, // F2's non-top chunk — the only F2 representative
	}
	// BM25 has F1, F2, F3 as top files. F3 is missing from final.
	keywordDocs := []RankedDoc{
		{ID: "c1_top", FileID: "F1", Score: 10},
		{ID: "c2_top", FileID: "F2", Score: 9}, // F2's BM25-top chunk — NOT in final (c2_tail is)
		{ID: "c3_top", FileID: "F3", Score: 8},
	}
	snapshot := map[string]RankedDoc{
		"c1_top":  final[0],
		"c2_top":  {ID: "c2_top", FileID: "F2", Score: 0.4},
		"c2_tail": final[4],
		"c3_top":  {ID: "c3_top", FileID: "F3", Score: 0.35},
	}

	result, reinserted := ApplyBM25Floor(final, keywordDocs, snapshot, 5, 4)

	// Before the fix, F2 would have been dropped (c2_tail at tail of
	// final is displaced by c3_top, leaving F2 unrepresented entirely
	// while c2_top isn't reinserted because c2_tail was "already present").
	// After the fix, the displacement skips F2 (it's a BM25-file) and
	// drops a non-BM25 entry instead.
	seen := map[string]bool{}
	for _, d := range result {
		seen[d.FileID] = true
	}
	for _, want := range []string{"F1", "F2", "F3"} {
		if !seen[want] {
			t.Errorf("BM25-top file %s missing from final after displacement; files=%v, reinserted=%d", want, seen, reinserted)
		}
	}
}

// TestApplyBM25Floor_BoostsInPlaceWhenAlreadyPresent: when a BM25-top chunk
// is already in final (so not reinserted) it still gets its score boosted
// to the top of the set. Reason: the floor's job is "guarantee this chunk
// gets LLM attention", and a demoted-but-present chunk suffers the same
// lost-in-the-middle fate as a missing one. Without this boost, the
// scenario where 3 of 4 BM25-top files are reinserted and 1 is "already
// present with a low score" leaves the 4th project as the one the LLM
// ignores — the exact pattern observed in production logs.
func TestApplyBM25Floor_BoostsInPlaceWhenAlreadyPresent(t *testing.T) {
	t.Parallel()
	final := []RankedDoc{
		{ID: "c_top", FileID: "F_top", Score: 0.95},
		{ID: "c_mid", FileID: "F_mid", Score: 0.55},
		// c_bm25_top is already in final but with a low reranker score —
		// exactly the "already present but demoted" case.
		{ID: "c_bm25_present", FileID: "F_bm25_present", Score: 0.15},
	}
	keywordDocs := []RankedDoc{
		{ID: "c_bm25_present", FileID: "F_bm25_present", Score: 10},
	}
	snapshot := map[string]RankedDoc{
		"c_bm25_present": final[2],
	}

	result, reinserted := ApplyBM25Floor(final, keywordDocs, snapshot, 5, 4)

	if reinserted != 0 {
		t.Fatalf("expected 0 reinsertions (chunk already present), got %d", reinserted)
	}
	var got *RankedDoc
	for i := range result {
		if result[i].ID == "c_bm25_present" {
			got = &result[i]
			break
		}
	}
	if got == nil {
		t.Fatal("c_bm25_present must remain in result")
	}
	if got.Score < 0.95 {
		t.Errorf("expected in-place score boost to 0.95 (top of set), got %.3f — chunk would still land in sandwich-middle and be ignored", got.Score)
	}
	// After boost + re-sort, the boosted chunk should be tied with c_top
	// at the front of the array.
	if result[0].Score != 0.95 || result[1].Score != 0.95 {
		t.Errorf("expected top two entries both at 0.95 after boost, got scores %.3f, %.3f", result[0].Score, result[1].Score)
	}
}

// TestApplyBM25Floor_BoostsReinsertedScores: reinserted chunks get their
// score boosted to the top of the final set so downstream score-sort +
// SandwichOrder place them at the LLM-attention-heavy boundaries of the
// context. Without the boost, reranker-demoted chunks (which is
// precisely why they fell out) land in the middle of the sandwich and
// LLM attention skips them even though the prompt correctly labels them.
func TestApplyBM25Floor_BoostsReinsertedScores(t *testing.T) {
	t.Parallel()
	final := []RankedDoc{
		{ID: "c1", FileID: "F1", Score: 0.9},
		{ID: "c2", FileID: "F2", Score: 0.8},
		{ID: "c3", FileID: "F3", Score: 0.7},
	}
	keywordDocs := []RankedDoc{
		{ID: "c_team1", FileID: "F4", Score: 10},
	}
	// Snapshot has the team-list chunk with a LOW score (what the
	// reranker assigned — the reason it fell out).
	snapshot := map[string]RankedDoc{
		"c_team1": {ID: "c_team1", FileID: "F4", Score: 0.12},
	}

	result, reinserted := ApplyBM25Floor(final, keywordDocs, snapshot, 5, 4)

	if reinserted != 1 {
		t.Fatalf("expected 1 reinsertion, got %d", reinserted)
	}
	// Find the reinserted chunk and verify its score was boosted to the
	// max (0.9), not left at the snapshot's 0.12.
	var got *RankedDoc
	for i := range result {
		if result[i].ID == "c_team1" {
			got = &result[i]
			break
		}
	}
	if got == nil {
		t.Fatal("reinserted chunk c_team1 not found in result")
	}
	if got.Score < 0.9 {
		t.Errorf("expected reinserted chunk to be boosted to top score (0.9), got %.3f", got.Score)
	}
}

// TestApplyBM25Floor_SkipsChunksMissingFromSnapshot: if the best BM25 chunk
// for a missing file didn't survive dedup (no snapshot entry), the floor
// must not synthesize a low-quality RankedDoc — skip silently.
func TestApplyBM25Floor_SkipsChunksMissingFromSnapshot(t *testing.T) {
	t.Parallel()
	final := []RankedDoc{
		{ID: "c1", FileID: "F1", Score: 0.9},
	}
	keywordDocs := []RankedDoc{
		{ID: "c2", FileID: "F2", Score: 9},
	}
	// F2's best chunk (c2) is not in snapshot → dedup dropped it.
	snapshot := map[string]RankedDoc{
		"c1": final[0],
	}
	result, reinserted := ApplyBM25Floor(final, keywordDocs, snapshot, 5, 4)

	if reinserted != 0 {
		t.Errorf("expected 0 reinsertions when snapshot misses the chunk, got %d", reinserted)
	}
	if len(result) != 1 {
		t.Errorf("expected length 1, got %d", len(result))
	}
}

func TestFuseRRFWeighted_DefaultWeightsMatchUnweighted(t *testing.T) {
	t.Parallel()
	list1 := []RankedDoc{{ID: "a"}, {ID: "b"}}
	list2 := []RankedDoc{{ID: "a"}, {ID: "c"}}

	weighted := FuseRRFWeighted(10, []float64{1.0, 1.0}, list1, list2)
	plain := FuseRRF(10, list1, list2)

	if len(weighted) != len(plain) {
		t.Fatalf("length mismatch: weighted=%d plain=%d", len(weighted), len(plain))
	}
	// Compare per-doc scores by ID rather than position: map iteration order
	// makes two independent calls produce the same scores but potentially
	// different orderings for tied docs ("b" and "c" both score 1/12).
	for _, id := range []string{"a", "b", "c"} {
		dw, okw := docByID(weighted, id)
		dp, okp := docByID(plain, id)
		if okw != okp {
			t.Errorf("doc %q presence mismatch: weighted=%v plain=%v", id, okw, okp)
			continue
		}
		if !approxEqual(dw.Score, dp.Score) {
			t.Errorf("score mismatch for %q: weighted=%v plain=%v", id, dw.Score, dp.Score)
		}
	}
	// The highest-scored doc ("a") must be first in both slices.
	if weighted[0].ID != plain[0].ID {
		t.Errorf("top-doc mismatch: weighted=%q plain=%q", weighted[0].ID, plain[0].ID)
	}
}

func TestFuseRRFWeighted_HigherWeightDominates(t *testing.T) {
	t.Parallel()
	// list1 (vector) has docs in one order; list2 (BM25) has them reversed.
	// With heavy BM25 weight the BM25 order should win.
	list1 := []RankedDoc{{ID: "a"}, {ID: "b"}}
	list2 := []RankedDoc{{ID: "b"}, {ID: "a"}}

	bm25Heavy := FuseRRFWeighted(10, []float64{0.5, 2.0}, list1, list2)
	if bm25Heavy[0].ID != "b" {
		t.Errorf("BM25-heavy fusion: expected b first, got %q", bm25Heavy[0].ID)
	}

	vectorHeavy := FuseRRFWeighted(10, []float64{2.0, 0.5}, list1, list2)
	if vectorHeavy[0].ID != "a" {
		t.Errorf("vector-heavy fusion: expected a first, got %q", vectorHeavy[0].ID)
	}
}

func TestFuseRRFWeighted_MissingWeightsDefaultToOne(t *testing.T) {
	t.Parallel()
	list1 := []RankedDoc{{ID: "a"}}
	list2 := []RankedDoc{{ID: "b"}}
	list3 := []RankedDoc{{ID: "c"}}

	// Two weights provided; the third list should be treated as weight 1.0.
	got := FuseRRFWeighted(10, []float64{1.0, 1.0}, list1, list2, list3)
	plain := FuseRRF(10, list1, list2, list3)

	if len(got) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(got))
	}
	for _, doc := range []string{"a", "b", "c"} {
		gw, _ := docByID(got, doc)
		gp, _ := docByID(plain, doc)
		if !approxEqual(gw.Score, gp.Score) {
			t.Errorf("doc %q score mismatch: weighted=%v plain=%v", doc, gw.Score, gp.Score)
		}
	}
}

func TestFuseRRFWeighted_NegativeOrZeroWeightTreatedAsZero(t *testing.T) {
	t.Parallel()
	// A zero-weight list contributes nothing; only the other list's order matters.
	list1 := []RankedDoc{{ID: "a"}, {ID: "b"}}
	list2 := []RankedDoc{{ID: "b"}, {ID: "a"}}

	zero := FuseRRFWeighted(10, []float64{1.0, 0.0}, list1, list2)
	if zero[0].ID != "a" {
		t.Errorf("zero-weighted second list: expected a first (from list1 only), got %q", zero[0].ID)
	}

	// Negative weights are clamped to 0, so this must produce the same fused
	// output as the zero-weight call. A regression that removes the clamp
	// would either subtract from the score or panic; either way the assertion
	// below catches it.
	negative := FuseRRFWeighted(10, []float64{1.0, -1.0}, list1, list2)
	if len(negative) != len(zero) {
		t.Fatalf("negative-weight result length differs from zero-weight: %d vs %d", len(negative), len(zero))
	}
	for _, want := range zero {
		got, ok := docByID(negative, want.ID)
		if !ok {
			t.Errorf("doc %q present under zero weight but missing under negative weight", want.ID)
			continue
		}
		if !approxEqual(got.Score, want.Score) {
			t.Errorf("doc %q score differs: negative=%v zero=%v", want.ID, got.Score, want.Score)
		}
	}

	// Contract: a zero-weighted list contributes NOTHING. Verify by
	// comparing against a single-list FuseRRFWeighted call: with list2
	// skipped entirely, the result must equal fusing list1 alone.
	list1Only := FuseRRFWeighted(10, []float64{1.0}, list1)
	if len(zero) != len(list1Only) {
		t.Fatalf("zero-weight list2 should be equivalent to list1-only: got len=%d want=%d", len(zero), len(list1Only))
	}
	for _, want := range list1Only {
		got, ok := docByID(zero, want.ID)
		if !ok {
			t.Errorf("doc %q present under list1-only but missing when list2 weight=0", want.ID)
			continue
		}
		if !approxEqual(got.Score, want.Score) {
			t.Errorf("doc %q score: list1-only=%v zero-weighted=%v", want.ID, want.Score, got.Score)
		}
	}
}

func TestComputeRerankScoreStddev_Empty(t *testing.T) {
	t.Parallel()
	got := ComputeRerankScoreStddev(map[int]float64{})
	if got != 0 {
		t.Errorf("empty map: got %v, want 0", got)
	}
}

func TestComputeRerankScoreStddev_SingleScore(t *testing.T) {
	t.Parallel()
	got := ComputeRerankScoreStddev(map[int]float64{0: 0.7})
	if got != 0 {
		t.Errorf("single score: got %v, want 0 (stddev undefined for n<2)", got)
	}
}

func TestComputeRerankScoreStddev_IdenticalScores(t *testing.T) {
	t.Parallel()
	got := ComputeRerankScoreStddev(map[int]float64{0: 0.5, 1: 0.5, 2: 0.5})
	if got != 0 {
		t.Errorf("identical scores: got %v, want 0", got)
	}
}

func TestComputeRerankScoreStddev_DegenerateBand(t *testing.T) {
	t.Parallel()
	// Mimics the documented Qwen3-Reranker collapse: all scores in 0.49..0.51 band.
	scores := map[int]float64{0: 0.49, 1: 0.50, 2: 0.51, 3: 0.50, 4: 0.49}
	got := ComputeRerankScoreStddev(scores)
	if got >= 0.02 {
		t.Errorf("degenerate band stddev: got %v, want < 0.02", got)
	}
}

func TestComputeRerankScoreStddev_HealthyDistribution(t *testing.T) {
	t.Parallel()
	// A healthy calibrated reranker (bge-v2-m3 on a real query) shows
	// scores spanning ~0.01..0.99 with the literal-answer chunk at the top.
	scores := map[int]float64{0: 0.99, 1: 0.10, 2: 0.05, 3: 0.40, 4: 0.02}
	got := ComputeRerankScoreStddev(scores)
	if got <= 0.2 {
		t.Errorf("healthy distribution stddev: got %v, want > 0.2", got)
	}
}

func TestEffectiveRerankAlpha_FallsBackToGlobal(t *testing.T) {
	t.Parallel()
	cfg := KBVectorConfig{
		RerankBlendAlpha:                 0.5,
		RerankBlendAlphaLookup:           -1, // sentinel: inherit
		RerankBlendAlphaEnumeration:      -1,
		RerankBlendAlphaComplexReasoning: -1,
	}
	for _, qt := range []string{QueryTypeLookup, QueryTypeEnumeration, QueryTypeComplexReasoning, QueryTypeUnknown, ""} {
		name := qt
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := EffectiveRerankAlpha(cfg, qt)
			if got != 0.5 {
				t.Errorf("query_type=%q with all sentinels: got %v, want 0.5", qt, got)
			}
		})
	}
}

func TestEffectiveRerankAlpha_LookupOverride(t *testing.T) {
	t.Parallel()
	cfg := KBVectorConfig{
		RerankBlendAlpha:                 0.5,
		RerankBlendAlphaLookup:           0.2,
		RerankBlendAlphaEnumeration:      -1, // sentinel: inherit
		RerankBlendAlphaComplexReasoning: -1,
	}
	if got := EffectiveRerankAlpha(cfg, QueryTypeLookup); got != 0.2 {
		t.Errorf("lookup override: got %v, want 0.2", got)
	}
	if got := EffectiveRerankAlpha(cfg, QueryTypeEnumeration); got != 0.5 {
		t.Errorf("enumeration with no override: got %v, want global 0.5", got)
	}
}

func TestEffectiveRerankAlpha_AllOverridesSet(t *testing.T) {
	t.Parallel()
	cfg := KBVectorConfig{
		RerankBlendAlpha:                 0.5,
		RerankBlendAlphaLookup:           0.2,
		RerankBlendAlphaEnumeration:      0.4,
		RerankBlendAlphaComplexReasoning: 0.8,
	}
	cases := map[string]float64{
		QueryTypeLookup:           0.2,
		QueryTypeEnumeration:      0.4,
		QueryTypeComplexReasoning: 0.8,
		QueryTypeUnknown:          0.5,
		"":                        0.5,
	}
	for qt, want := range cases {
		if got := EffectiveRerankAlpha(cfg, qt); got != want {
			t.Errorf("query_type=%q: got %v, want %v", qt, got, want)
		}
	}
}

func TestEffectiveRerankAlpha_ZeroIsValidOverride(t *testing.T) {
	t.Parallel()
	// Per the spec, alpha=0.0 is a legitimate per-type override
	// ("ignore the reranker entirely for this query type"), distinct
	// from the negative sentinel that means "inherit". The global
	// rerank_blend_alpha parser already allows 0.0; per-type overrides
	// must too.
	cfg := KBVectorConfig{
		RerankBlendAlpha:                 0.5,
		RerankBlendAlphaLookup:           0.0,
		RerankBlendAlphaEnumeration:      -1, // sentinel: inherit
		RerankBlendAlphaComplexReasoning: -1,
	}
	if got := EffectiveRerankAlpha(cfg, QueryTypeLookup); got != 0.0 {
		t.Errorf("alpha=0.0 should be a valid lookup override, not interpreted as inherit: got %v, want 0.0", got)
	}
	if got := EffectiveRerankAlpha(cfg, QueryTypeEnumeration); got != 0.5 {
		t.Errorf("enumeration with no override should still inherit global: got %v, want 0.5", got)
	}
}

func TestEffectiveRerankAlpha_NaNFallsBackToGlobal(t *testing.T) {
	t.Parallel()
	// NaN compares false against everything, so a guard like
	// `override < 0 || override > 1` would silently let NaN through.
	// Verify the inclusive `0 <= x <= 1` form rejects NaN.
	cfg := KBVectorConfig{
		RerankBlendAlpha:       0.5,
		RerankBlendAlphaLookup: math.NaN(),
	}
	if got := EffectiveRerankAlpha(cfg, QueryTypeLookup); got != 0.5 {
		t.Errorf("NaN lookup override should fall back to global: got %v, want 0.5", got)
	}
}

func TestEffectiveRerankAlphaForQuery_EntityWinsOverRoute(t *testing.T) {
	t.Parallel()
	// Entity-asking override beats the per-route override when both
	// are set. Rationale: a query can be both `lookup` (route) and
	// entity-asking (shape); the entity-asking failure mode (reranker
	// saturation on lookalike role/function chunks) is the more
	// specific signal.
	cfg := KBVectorConfig{
		RerankBlendAlpha:       0.8,
		RerankBlendAlphaLookup: 0.6,
		RerankBlendAlphaEntity: 0.3,
	}
	if got := EffectiveRerankAlphaForQuery(cfg, QueryTypeLookup, true); got != 0.3 {
		t.Errorf("entity override should win over lookup override: got %v, want 0.3", got)
	}
	if got := EffectiveRerankAlphaForQuery(cfg, QueryTypeLookup, false); got != 0.6 {
		t.Errorf("non-entity lookup should use lookup override: got %v, want 0.6", got)
	}
}

func TestEffectiveRerankAlphaForQuery_EntitySentinelInherits(t *testing.T) {
	t.Parallel()
	// When the entity-asking override is at its sentinel value, fall
	// through to the existing per-route logic — same semantics as the
	// per-route sentinels.
	cfg := KBVectorConfig{
		RerankBlendAlpha:                 0.8,
		RerankBlendAlphaLookup:           0.5,
		RerankBlendAlphaEnumeration:      -1,
		RerankBlendAlphaComplexReasoning: -1,
		RerankBlendAlphaEntity:           -1,
	}
	if got := EffectiveRerankAlphaForQuery(cfg, QueryTypeLookup, true); got != 0.5 {
		t.Errorf("entity sentinel should inherit per-route alpha: got %v, want 0.5", got)
	}
	if got := EffectiveRerankAlphaForQuery(cfg, QueryTypeEnumeration, true); got != 0.8 {
		t.Errorf("entity sentinel + no enum override should inherit global: got %v, want 0.8", got)
	}
}

func TestEffectiveRerankAlphaForQuery_EntityZeroIsValid(t *testing.T) {
	t.Parallel()
	// 0.0 is a legitimate "ignore reranker entirely" value for entity
	// queries — same convention as the per-route 0.0 overrides.
	cfg := KBVectorConfig{
		RerankBlendAlpha:       0.8,
		RerankBlendAlphaEntity: 0.0,
	}
	if got := EffectiveRerankAlphaForQuery(cfg, QueryTypeLookup, true); got != 0.0 {
		t.Errorf("entity alpha=0.0 should be honored, not interpreted as inherit: got %v, want 0.0", got)
	}
}

func TestEffectiveRerankAlphaForQuery_GatedToLookupRoute(t *testing.T) {
	t.Parallel()
	// Entity override only applies to lookup-route queries. The proper-
	// noun extractor over-fires on German complex_reasoning queries
	// ("Best Practices", "Künstliche Intelligenz" look like entities),
	// and the un-gated override cost complex_reasoning MRR −2.7pp in
	// AB_both_v2 (2026-05-11). Gating to lookup preserves the BM25
	// phrase-evidence benefit on entity-role queries without touching
	// complex_reasoning's reranker-dominant default.
	cfg := KBVectorConfig{
		RerankBlendAlpha:                 0.8,
		RerankBlendAlphaLookup:           -1,
		RerankBlendAlphaEnumeration:      -1,
		RerankBlendAlphaComplexReasoning: -1,
		RerankBlendAlphaEntity:           0.3,
	}
	if got := EffectiveRerankAlphaForQuery(cfg, QueryTypeLookup, true); got != 0.3 {
		t.Errorf("entity override should apply on lookup route: got %v, want 0.3", got)
	}
	if got := EffectiveRerankAlphaForQuery(cfg, QueryTypeComplexReasoning, true); got != 0.8 {
		t.Errorf("entity override must NOT apply on complex_reasoning route: got %v, want 0.8 (inherit global)", got)
	}
	if got := EffectiveRerankAlphaForQuery(cfg, QueryTypeEnumeration, true); got != 0.8 {
		t.Errorf("entity override must NOT apply on enumeration route: got %v, want 0.8 (inherit global)", got)
	}
	if got := EffectiveRerankAlphaForQuery(cfg, "", true); got != 0.8 {
		t.Errorf("entity override must NOT apply when route is unknown: got %v, want 0.8 (inherit global)", got)
	}
}
