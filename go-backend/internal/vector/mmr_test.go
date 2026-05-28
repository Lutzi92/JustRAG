package vector

import (
	"testing"
)

func TestApplyMMR_LambdaOnePreservesOrder(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", Content: "alpha", Score: 0.9},
		{ID: "b", Content: "beta", Score: 0.7},
		{ID: "c", Content: "gamma", Score: 0.5},
	}
	got := ApplyMMR(docs, 1.0, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(got))
	}
	wantOrder := []string{"a", "b", "c"}
	for i, w := range wantOrder {
		if got[i].ID != w {
			t.Errorf("position %d: expected %s, got %s", i, w, got[i].ID)
		}
	}
}

func TestApplyMMR_LambdaZeroPicksMostDifferent(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "near1", Content: "alpha bravo charlie delta", Score: 0.95},
		{ID: "near2", Content: "alpha bravo charlie delta", Score: 0.93},
		{ID: "diff", Content: "echo foxtrot golf hotel", Score: 0.50},
	}
	got := ApplyMMR(docs, 0.0, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	if got[0].ID != "near1" {
		t.Errorf("first pick should be near1, got %s", got[0].ID)
	}
	if got[1].ID != "diff" {
		t.Errorf("second pick (λ=0) should be diff, got %s", got[1].ID)
	}
}

func TestApplyMMR_BalancedLambda(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "top", Content: "alpha", Score: 1.0},
		{ID: "near-top", Content: "alpha", Score: 0.95},
		{ID: "diverse-low", Content: "completely different vocabulary", Score: 0.40},
	}
	got := ApplyMMR(docs, 0.5, 2)
	if len(got) < 2 {
		t.Fatalf("expected at least 2 docs, got %d", len(got))
	}
	if got[1].ID != "diverse-low" {
		t.Errorf("balanced λ should pick diverse-low second, got %s", got[1].ID)
	}
}

func TestApplyMMR_KGreaterThanInputReturnsAll(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", Content: "x", Score: 0.9},
		{ID: "b", Content: "y", Score: 0.5},
	}
	got := ApplyMMR(docs, 0.7, 10)
	if len(got) != 2 {
		t.Errorf("expected 2 (all), got %d", len(got))
	}
}

func TestApplyMMR_EmptyInput(t *testing.T) {
	t.Parallel()
	got := ApplyMMR(nil, 0.7, 5)
	if got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
	got = ApplyMMR([]RankedDoc{}, 0.7, 5)
	if len(got) != 0 {
		t.Errorf("expected empty for empty input, got %d", len(got))
	}
}

func TestApplyMMR_SingleDocReturnsSame(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{{ID: "only", Content: "x", Score: 0.5}}
	got := ApplyMMR(docs, 0.7, 5)
	if len(got) != 1 || got[0].ID != "only" {
		t.Errorf("expected single doc passthrough, got %v", got)
	}
}

func TestApplyMMR_KZeroReturnsEmpty(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", Content: "x", Score: 0.9},
		{ID: "b", Content: "y", Score: 0.5},
	}
	got := ApplyMMR(docs, 0.7, 0)
	if len(got) != 0 {
		t.Errorf("expected empty for k=0, got %d", len(got))
	}
}

func TestApplyMMR_SimilarContentFromDifferentFilesIsKept(t *testing.T) {
	// Regression guard for the person-to-projects query case.
	//
	// Five project pages each carry a synthetic "persons mentioned" chunk
	// that uses the same bilingual template — only the page title and the
	// list of names differ. Content Jaccard between any two is ~0.8 because
	// the template dominates word counts. Five unrelated chunks from five
	// unrelated files compete for the same k slots with slightly lower
	// relevance but much lower content overlap.
	//
	// With the old (content-only) MMR, the diversity penalty crushes the
	// synthetic chunks — MMR picks one of them plus four low-relevance but
	// visually diverse chunks, which is the exact failure mode seen in
	// the chatbot ("missing projects").
	//
	// The expected behavior: most of the top-k picks should be synthetic
	// chunks because they're each from DIFFERENT files (true sources), and
	// source-aware MMR should recognize that.
	t.Parallel()

	synthetic := func(person, page string) string {
		return "projektbeteiligte mentioned persons seite " + page +
			" auf dieser seite werden folgende personen erwaehnt " + person +
			" bob jones this page mentions the following persons " + person + " bob jones"
	}
	unrelated := func(seed string) string {
		return seed + " " + seed + " description overview goals deliverables milestones budget"
	}

	docs := []RankedDoc{
		// Five high-relevance cross-file synthetic chunks naming "alice".
		{ID: "sA", FileID: "f1", Content: synthetic("alice smith", "alpha"), Score: 0.90},
		{ID: "sB", FileID: "f2", Content: synthetic("alice smith", "beta"), Score: 0.89},
		{ID: "sC", FileID: "f3", Content: synthetic("alice smith", "gamma"), Score: 0.88},
		{ID: "sD", FileID: "f4", Content: synthetic("alice smith", "delta"), Score: 0.87},
		{ID: "sE", FileID: "f5", Content: synthetic("alice smith", "epsilon"), Score: 0.86},
		// Five lower-relevance unrelated chunks from unrelated files.
		{ID: "u1", FileID: "g1", Content: unrelated("infrastructure"), Score: 0.75},
		{ID: "u2", FileID: "g2", Content: unrelated("procurement"), Score: 0.74},
		{ID: "u3", FileID: "g3", Content: unrelated("training"), Score: 0.73},
		{ID: "u4", FileID: "g4", Content: unrelated("governance"), Score: 0.72},
		{ID: "u5", FileID: "g5", Content: unrelated("archive"), Score: 0.71},
	}

	got := ApplyMMR(docs, 0.7, 5)
	if len(got) != 5 {
		t.Fatalf("expected 5 picks, got %d", len(got))
	}

	syntheticCount := 0
	for _, d := range got {
		if len(d.ID) >= 1 && d.ID[0] == 's' {
			syntheticCount++
		}
	}

	t.Logf("picks: %v (synthetic=%d)", idsOf(got), syntheticCount)

	// With source-aware MMR, all 5 picks should be synthetic chunks —
	// one per project page — because they're the highest-relevance docs
	// and each comes from a DIFFERENT file. The current content-only
	// MMR instead picks a lower-relevance "unrelated" chunk just
	// because its words don't overlap the synthetic template.
	if syntheticCount < 5 {
		t.Fatalf("source-aware MMR should surface all 5 cross-file synthetic chunks, got %d (ids: %v)",
			syntheticCount, idsOf(got))
	}
}

func idsOf(docs []RankedDoc) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.ID
	}
	return out
}

func TestApplyMMR_SameFileSimilarChunksStillPruned(t *testing.T) {
	// Inside a single document, near-duplicate chunks should still drop out.
	// This guards against over-weakening the diversity penalty.
	t.Parallel()

	docs := []RankedDoc{
		{ID: "a1", FileID: "f", Content: "alpha bravo charlie delta echo foxtrot", Score: 0.95},
		{ID: "a2", FileID: "f", Content: "alpha bravo charlie delta echo foxtrot", Score: 0.90},
		{ID: "c", FileID: "f", Content: "totally different vocabulary terms here", Score: 0.50},
	}
	got := ApplyMMR(docs, 0.3, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 picks, got %d", len(got))
	}
	if got[0].ID != "a1" {
		t.Errorf("first pick should be a1, got %s", got[0].ID)
	}
	if got[1].ID != "c" {
		t.Errorf("second pick at strong-diversity λ should be the distinct chunk c, got %s", got[1].ID)
	}
}

func TestApplyMMR_LambdaClampedToValidRange(t *testing.T) {
	t.Parallel()
	docs := []RankedDoc{
		{ID: "a", Content: "x", Score: 0.9},
		{ID: "b", Content: "y", Score: 0.5},
	}
	a := ApplyMMR(docs, -0.5, 2)
	b := ApplyMMR(docs, 0.0, 2)
	if len(a) != len(b) || a[0].ID != b[0].ID || a[1].ID != b[1].ID {
		t.Errorf("λ=-0.5 should behave like λ=0, got %+v vs %+v", a, b)
	}

	c := ApplyMMR(docs, 2.0, 2)
	d := ApplyMMR(docs, 1.0, 2)
	if len(c) != len(d) || c[0].ID != d[0].ID || c[1].ID != d[1].ID {
		t.Errorf("λ=2.0 should behave like λ=1.0, got %+v vs %+v", c, d)
	}

}
