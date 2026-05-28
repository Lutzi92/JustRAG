package chat

import (
	"context"
	"errors"
	"math"
	"testing"
)

// fakeEmbedder is a deterministic stand-in for ai.GenerateEmbedding so the
// semantic fallback can be exercised in unit tests without network I/O.
// It returns the vector for an exact text match in `vecs`; for unknown
// inputs it returns errMissing so the caller observes a fail-open path.
type fakeEmbedder struct {
	vecs       map[string][]float64
	errMissing error
	calls      int
}

func (f *fakeEmbedder) embed(_ context.Context, text string) ([]float64, error) {
	f.calls++
	if v, ok := f.vecs[text]; ok {
		return v, nil
	}
	if f.errMissing != nil {
		return nil, f.errMissing
	}
	return nil, errors.New("fakeEmbedder: missing")
}

// unitVec scales v to unit length for clean cosine arithmetic in tests.
func unitVec(v []float64) []float64 {
	var n float64
	for _, x := range v {
		n += x * x
	}
	n = math.Sqrt(n)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

func TestRunCitationValidation_NoSemanticConfigPreservesNgram(t *testing.T) {
	t.Parallel()
	answer := "Foo bar baz qux. The notice period is six weeks [1]."
	sources := []ChatSource{
		{Index: 1, Content: "the notice period is six weeks for permanent staff"},
	}
	got := RunCitationValidation(context.Background(), answer, sources, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 status, got %d", len(got))
	}
	if !got[0].Verified || got[0].Method != "ngram" {
		t.Errorf("expected ngram-verified, got %+v", got[0])
	}
}

func TestRunCitationValidation_SemanticFlipsNoOverlap(t *testing.T) {
	t.Parallel()
	// Answer paraphrases the source — n-gram fails. Semantic vectors
	// constructed to be highly similar (cosine == 1.0).
	answer := "Foo bar baz qux. Tenants must give a quarter year of warning before leaving [1]."
	sources := []ChatSource{
		{Index: 1, Content: "the notice period is three months"},
	}
	// Pre-confirm the n-gram path fails on this input.
	ngramOnly := ValidateCitations(answer, sources)
	if len(ngramOnly) != 1 || ngramOnly[0].Verified {
		t.Fatalf("test setup invalid: n-gram should fail; got %+v", ngramOnly)
	}

	embedder := &fakeEmbedder{
		vecs: map[string][]float64{
			"the notice period is three months": unitVec([]float64{1, 0, 0}),
		},
	}
	for _, w := range citationWindows(answer) {
		embedder.vecs[w] = unitVec([]float64{1, 0, 0})
	}
	cfg := &SemanticConfig{Embed: embedder.embed, Threshold: 0.85}
	got := RunCitationValidation(context.Background(), answer, sources, cfg)
	if len(got) != 1 {
		t.Fatalf("want 1 status, got %d", len(got))
	}
	if !got[0].Verified {
		t.Errorf("semantic fallback should have flipped to verified, got %+v", got[0])
	}
	if got[0].Method != "semantic" {
		t.Errorf("Method: got %q, want \"semantic\"", got[0].Method)
	}
}

func TestRunCitationValidation_SemanticBelowThreshold(t *testing.T) {
	t.Parallel()
	answer := "Tenants must give a quarter year of warning before leaving [1]."
	sources := []ChatSource{
		{Index: 1, Content: "the notice period is three months"},
	}
	// Vectors with cosine ~0.6 — below the 0.85 threshold.
	embedder := &fakeEmbedder{
		vecs: map[string][]float64{
			"the notice period is three months": unitVec([]float64{0.6, 0.8, 0}),
		},
	}
	for _, w := range citationWindows(answer) {
		embedder.vecs[w] = unitVec([]float64{1, 0, 0})
	}
	cfg := &SemanticConfig{Embed: embedder.embed, Threshold: 0.85}
	got := RunCitationValidation(context.Background(), answer, sources, cfg)
	if len(got) != 1 {
		t.Fatalf("want 1 status, got %d", len(got))
	}
	if got[0].Verified {
		t.Errorf("below threshold should NOT verify, got %+v", got[0])
	}
	if got[0].Reason != "no_overlap" {
		t.Errorf("Reason: got %q, want \"no_overlap\"", got[0].Reason)
	}
}

func TestRunCitationValidation_FailOpenOnEmbedError(t *testing.T) {
	t.Parallel()
	answer := "Tenants give a quarter year warning [1]."
	sources := []ChatSource{
		{Index: 1, Content: "notice period is three months"},
	}
	embedder := &fakeEmbedder{
		vecs:       map[string][]float64{},
		errMissing: errors.New("simulated network error"),
	}
	cfg := &SemanticConfig{Embed: embedder.embed, Threshold: 0.85}
	got := RunCitationValidation(context.Background(), answer, sources, cfg)
	if len(got) != 1 {
		t.Fatalf("want 1 status, got %d", len(got))
	}
	if got[0].Verified {
		t.Errorf("embed error should keep n-gram verdict (unverified), got %+v", got[0])
	}
	if got[0].Reason != "no_overlap" {
		t.Errorf("Reason: got %q, want \"no_overlap\"", got[0].Reason)
	}
}

func TestRunCitationValidation_OutOfRangeNotRetried(t *testing.T) {
	t.Parallel()
	answer := "There is no source three [3]."
	sources := []ChatSource{
		{Index: 1, Content: "first"},
		{Index: 2, Content: "second"},
	}
	embedder := &fakeEmbedder{}
	cfg := &SemanticConfig{Embed: embedder.embed, Threshold: 0.85}
	got := RunCitationValidation(context.Background(), answer, sources, cfg)
	if len(got) != 1 {
		t.Fatalf("want 1 status, got %d", len(got))
	}
	if got[0].Reason != "out_of_range" {
		t.Errorf("Reason: got %q, want \"out_of_range\"", got[0].Reason)
	}
	if embedder.calls != 0 {
		t.Errorf("out_of_range should not call embedder; got %d calls", embedder.calls)
	}
}

func TestRunCitationValidation_WindowCachedAcrossNs(t *testing.T) {
	t.Parallel()
	// Two distinct Ns appearing in the same sentence → same window string.
	// Without the per-call window cache, getWinVec would embed the window
	// twice. With the cache, it's embedded once.
	answer := "Both A [1] and B [2] live in the shared window."
	sources := []ChatSource{
		{Index: 1, Content: "source one content"},
		{Index: 2, Content: "source two content"},
	}
	// Pre-confirm the n-gram path fails on both citations (no overlap with
	// the synthetic answer text).
	ngramOnly := ValidateCitations(answer, sources)
	if len(ngramOnly) != 2 {
		t.Fatalf("test setup invalid: want 2 statuses from n-gram pass, got %d", len(ngramOnly))
	}
	for _, s := range ngramOnly {
		if s.Verified {
			t.Fatalf("test setup invalid: n-gram should fail; got %+v", s)
		}
	}

	embedder := &fakeEmbedder{
		vecs: map[string][]float64{
			"source one content": unitVec([]float64{1, 0, 0}),
			"source two content": unitVec([]float64{1, 0, 0}),
		},
	}
	for _, w := range citationWindows(answer) {
		embedder.vecs[w] = unitVec([]float64{1, 0, 0})
	}
	cfg := &SemanticConfig{Embed: embedder.embed, Threshold: 0.85}
	got := RunCitationValidation(context.Background(), answer, sources, cfg)
	if len(got) != 2 {
		t.Fatalf("want 2 statuses, got %d", len(got))
	}

	// Both citations are in one sentence → one shared window.
	// Embedder is called for: 2 sources + 1 window = 3 total. If the
	// window cache is broken, the count rises to 4.
	if embedder.calls != 3 {
		t.Errorf("expected exactly 3 embed calls (2 sources + 1 shared window), got %d", embedder.calls)
	}
}
