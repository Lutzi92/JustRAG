package vector

import (
	"testing"
)

// TestAssembleRRFWeights_NoExtras pins the legacy weight ordering:
// the original [vector, keyword] pair, no extra lists. Behaviour
// must match the pre-P4 inline construction so toggling RAG-Fusion
// off (or never enabling it) produces byte-identical RRF output.
func TestAssembleRRFWeights_NoExtras(t *testing.T) {
	t.Parallel()
	cfg := KBVectorConfig{RRFWeightVector: 1.5, RRFWeightBM25: 0.8}
	got := assembleRRFWeights(cfg, 0, 0)
	want := []float64{1.5, 0.8}
	if len(got) != len(want) {
		t.Fatalf("length: got %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %f, want %f", i, got[i], want[i])
		}
	}
}

// TestAssembleRRFWeights_VectorExtrasOnly: pre-P4 behaviour for
// multi-query / step-back / sub-queries. Each extra list inherits
// the vector weight (as the original inline code does).
func TestAssembleRRFWeights_VectorExtrasOnly(t *testing.T) {
	t.Parallel()
	cfg := KBVectorConfig{RRFWeightVector: 1.5, RRFWeightBM25: 0.8}
	got := assembleRRFWeights(cfg, 3, 0)
	want := []float64{1.5, 0.8, 1.5, 1.5, 1.5}
	if len(got) != len(want) {
		t.Fatalf("length: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %f, want %f", i, got[i], want[i])
		}
	}
}

// TestAssembleRRFWeights_BothExtras: P4 RAG-Fusion happy path.
// Vector extras get the vector weight; keyword extras get the
// BM25 weight. The order must be [vector, keyword, vec_extras...,
// kw_extras...] so the caller's lists slice can be assembled in
// the same order.
func TestAssembleRRFWeights_BothExtras(t *testing.T) {
	t.Parallel()
	cfg := KBVectorConfig{RRFWeightVector: 1.5, RRFWeightBM25: 0.8}
	got := assembleRRFWeights(cfg, 2, 3)
	want := []float64{1.5, 0.8, 1.5, 1.5, 0.8, 0.8, 0.8}
	if len(got) != len(want) {
		t.Fatalf("length: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]: got %f, want %f", i, got[i], want[i])
		}
	}
}

// TestAssembleRRFWeights_NegativeCountsTreatedAsZero: defensive —
// a programming error feeding negative counts must not panic or
// allocate negatively. Graceful 0-extras fallback.
func TestAssembleRRFWeights_NegativeCountsTreatedAsZero(t *testing.T) {
	t.Parallel()
	cfg := KBVectorConfig{RRFWeightVector: 1.0, RRFWeightBM25: 1.0}
	got := assembleRRFWeights(cfg, -3, -1)
	if len(got) != 2 {
		t.Errorf("negative counts must yield length-2 baseline, got %d (%v)", len(got), got)
	}
}
