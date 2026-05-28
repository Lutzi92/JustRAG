package vector

// assembleRRFWeights returns the RRF weight slice in the canonical
// list order: [vector, keyword, vec_extras..., kw_extras...].
// Caller must assemble the lists slice in the same order so each
// list's index matches its weight.
//
// The function exists to make the P4 RAG-Fusion path (multiple
// vector AND keyword extra lists per chat turn) testable as a
// pure function — the inline construction at the search.go RRF
// call site grew too dense to verify by reading.
//
// Negative counts are treated as zero so a programming error
// upstream never panics or allocates absurdly.
func assembleRRFWeights(cfg KBVectorConfig, vectorExtras, keywordExtras int) []float64 {
	if vectorExtras < 0 {
		vectorExtras = 0
	}
	if keywordExtras < 0 {
		keywordExtras = 0
	}
	weights := make([]float64, 0, 2+vectorExtras+keywordExtras)
	weights = append(weights, cfg.RRFWeightVector, cfg.RRFWeightBM25)
	for i := 0; i < vectorExtras; i++ {
		weights = append(weights, cfg.RRFWeightVector)
	}
	for i := 0; i < keywordExtras; i++ {
		weights = append(weights, cfg.RRFWeightBM25)
	}
	return weights
}
