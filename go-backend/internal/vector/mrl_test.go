package vector

import (
	"math"
	"testing"
)

func TestL2NormalizeFirst256_TruncatesAndNormalizes(t *testing.T) {
	// 2560-dim input, all 1.0
	t.Parallel()

	in := make([]float64, 2560)
	for i := range in {
		in[i] = 1.0
	}
	out := L2NormalizeFirst256(in)
	if len(out) != 256 {
		t.Fatalf("length: want 256, got %d", len(out))
	}
	// L2 norm of out should be 1.0
	var sumsq float64
	for _, v := range out {
		sumsq += v * v
	}
	norm := math.Sqrt(sumsq)
	if math.Abs(norm-1.0) > 1e-9 {
		t.Errorf("L2 norm: want 1.0, got %f", norm)
	}
	// Each component should equal 1/sqrt(256) ≈ 0.0625
	want := 1.0 / math.Sqrt(256)
	for i, v := range out {
		if math.Abs(v-want) > 1e-9 {
			t.Errorf("component %d: want %f, got %f", i, want, v)
			break
		}
	}
}

func TestL2NormalizeFirst256_ShortInput(t *testing.T) {
	// Input shorter than 256 should return nil (defensive — should never
	// happen in production, but a panic on short input would be a bad
	// failure mode).
	t.Parallel()

	in := make([]float64, 100)
	out := L2NormalizeFirst256(in)
	if out != nil {
		t.Errorf("short input: want nil, got %v", out)
	}
}

func TestL2NormalizeFirst256_ZeroVector(t *testing.T) {
	// A zero input has zero norm; division would NaN. Helper should return
	// nil so the caller can decide what to do (production embeddings are
	// effectively never zero, so this is a defensive guard).
	t.Parallel()

	in := make([]float64, 2560)
	out := L2NormalizeFirst256(in)
	if out != nil {
		t.Errorf("zero input: want nil, got %v", out)
	}
}

func TestL2NormalizeFirst256_PreservesDirection(t *testing.T) {
	// Non-uniform input: the first 256 components after normalization
	// should equal the raw first-256 components scaled by 1/||first256||.
	t.Parallel()

	in := make([]float64, 2560)
	in[0] = 3.0
	in[1] = 4.0
	out := L2NormalizeFirst256(in)
	expectedNorm := 5.0 // sqrt(9+16) over the first 256, the rest are zero
	if math.Abs(out[0]-3.0/expectedNorm) > 1e-9 {
		t.Errorf("out[0]: want %f, got %f", 3.0/expectedNorm, out[0])
	}
	if math.Abs(out[1]-4.0/expectedNorm) > 1e-9 {
		t.Errorf("out[1]: want %f, got %f", 4.0/expectedNorm, out[1])
	}
}
