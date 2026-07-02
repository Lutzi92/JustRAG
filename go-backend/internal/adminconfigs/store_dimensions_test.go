package adminconfigs

import "testing"

// Embedding dims semantics changed with migration 0062: 0 = "auto" (backend
// omits the dimensions request parameter). The save path must persist 0
// verbatim — the old 1536 fallback would activate MRL truncation to 1536 on
// the next resolve and silently point queries at the wrong vector table.
func TestCollectModelInserts_PreservesZeroEmbeddingDims(t *testing.T) {
	input := CreateAIConfigInput{
		EmbeddingModels: []AIModelInput{
			{Name: "qwen3-embedding-8b", Dimensions: 0},
			{Name: "embed-truncated", Dimensions: 2048},
		},
	}

	out := collectModelInserts(input)
	if len(out) != 2 {
		t.Fatalf("expected 2 inserts, got %d", len(out))
	}
	if out[0].dims != 0 {
		t.Errorf("expected dims 0 (auto) preserved for %s, got %d", out[0].name, out[0].dims)
	}
	if out[1].dims != 2048 {
		t.Errorf("expected dims 2048 preserved for %s, got %d", out[1].name, out[1].dims)
	}
}

// Negative values are clamped to 0 (auto) rather than persisted.
func TestCollectModelInserts_ClampsNegativeEmbeddingDims(t *testing.T) {
	input := CreateAIConfigInput{
		EmbeddingModels: []AIModelInput{
			{Name: "embed-weird", Dimensions: -7},
		},
	}

	out := collectModelInserts(input)
	if len(out) != 1 {
		t.Fatalf("expected 1 insert, got %d", len(out))
	}
	if out[0].dims != 0 {
		t.Errorf("expected negative dims clamped to 0, got %d", out[0].dims)
	}
}
