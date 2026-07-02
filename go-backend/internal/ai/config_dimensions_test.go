package ai

import (
	"context"
	"testing"
)

// EmbeddingDimensions resolves from the embedding model's dimensions column.
func TestResolveEmbeddingDimensions(t *testing.T) {
	store := newStore()
	store.modelsByProv["prov-1"] = []AIModelInfo{
		{Name: "gpt-4o"},
		{Name: "qwen3-embedding-8b", IsEmbedding: true, Dimensions: 2048},
	}
	resolver := NewConfigResolver(store)

	cfg, err := resolver.Resolve(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EmbeddingModel != "qwen3-embedding-8b" {
		t.Fatalf("expected EmbeddingModel qwen3-embedding-8b, got %s", cfg.EmbeddingModel)
	}
	if cfg.EmbeddingDimensions != 2048 {
		t.Errorf("expected EmbeddingDimensions 2048, got %d", cfg.EmbeddingDimensions)
	}
}

// A model row without dimensions set (0 = auto/native) resolves to 0.
func TestResolveEmbeddingDimensions_ZeroWhenUnset(t *testing.T) {
	store := newStore() // defaultModels has no Dimensions set
	resolver := NewConfigResolver(store)

	cfg, err := resolver.Resolve(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EmbeddingDimensions != 0 {
		t.Errorf("expected EmbeddingDimensions 0, got %d", cfg.EmbeddingDimensions)
	}
}

// When a KB overrides the embedding model by name, EmbeddingDimensions follows
// the override model's dimensions (looked up in the provider's model list).
func TestResolveEmbeddingDimensions_KBOverrideModel(t *testing.T) {
	store := newStore()
	store.modelsByProv["prov-1"] = []AIModelInfo{
		{Name: "embed-large", IsEmbedding: true, Dimensions: 4096},
		{Name: "embed-small", IsEmbedding: true, Dimensions: 1024},
	}
	store.kbOverrides["kb-1"] = &KBModelOverrides{
		EmbeddingModel: strPtr("embed-small"),
	}
	resolver := NewConfigResolver(store)

	cfg, err := resolver.Resolve(context.Background(), "kb-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EmbeddingModel != "embed-small" {
		t.Fatalf("expected EmbeddingModel embed-small, got %s", cfg.EmbeddingModel)
	}
	if cfg.EmbeddingDimensions != 1024 {
		t.Errorf("expected EmbeddingDimensions 1024, got %d", cfg.EmbeddingDimensions)
	}
}

// A KB override naming a model unknown to the provider resolves dims to 0
// (auto) — we can't know the native size, so we must not send a guess.
func TestResolveEmbeddingDimensions_KBOverrideUnknownModel(t *testing.T) {
	store := newStore()
	store.modelsByProv["prov-1"] = []AIModelInfo{
		{Name: "embed-large", IsEmbedding: true, Dimensions: 4096},
	}
	store.kbOverrides["kb-1"] = &KBModelOverrides{
		EmbeddingModel: strPtr("some-external-model"),
	}
	resolver := NewConfigResolver(store)

	cfg, err := resolver.Resolve(context.Background(), "kb-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EmbeddingModel != "some-external-model" {
		t.Fatalf("expected EmbeddingModel some-external-model, got %s", cfg.EmbeddingModel)
	}
	if cfg.EmbeddingDimensions != 0 {
		t.Errorf("expected EmbeddingDimensions 0 for unknown override model, got %d", cfg.EmbeddingDimensions)
	}
}
