package vector

import "math"

// MRLLowDim is the truncation length used for the Matryoshka two-pass
// retrieval index. 256 is the standard MRL setpoint for the Qwen3-Embedding
// family — the first 256 dimensions of a 2560-dim Qwen3-Embedding-4B vector
// (or any 1536/4096 sibling) are themselves a usable embedding after
// L2-normalization.
const MRLLowDim = 256

// L2NormalizeFirst256 truncates a Matryoshka-trained embedding to its first
// MRLLowDim (256) components and L2-normalizes the result. Returns nil for
// input shorter than 256 dims or for the all-zero vector (defensive: zero
// would NaN under normalization, and production embeddings are effectively
// never zero).
//
// This is the deterministic projection used both at ingestion (writing
// embedding_low alongside embedding) and at query time (deriving the low-dim
// query vector from the full-dim query embedding) — keep them in sync.
func L2NormalizeFirst256(emb []float64) []float64 {
	if len(emb) < MRLLowDim {
		return nil
	}
	out := make([]float64, MRLLowDim)
	var sumsq float64
	for i := 0; i < MRLLowDim; i++ {
		out[i] = emb[i]
		sumsq += emb[i] * emb[i]
	}
	if sumsq == 0 {
		return nil
	}
	norm := math.Sqrt(sumsq)
	for i := range out {
		out[i] /= norm
	}
	return out
}
