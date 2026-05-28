package ai

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var _ ConfigStore = (*mockStore)(nil)

type mockStore struct {
	activeProvider  *AIProviderInfo
	activeErr       error
	providersByID   map[string]*AIProviderInfo
	providerByIDErr error
	modelsByProv    map[string][]AIModelInfo
	modelsByProvErr error
	kbOverrides     map[string]*KBModelOverrides
	kbOverridesErr  error

	callsGetActiveAIProvider   int
	callsGetAIProviderByID     int
	callsGetAIModelsByProvider int
	callsGetKBModelOverrides   int
}

func (m *mockStore) GetActiveAIProvider(_ context.Context) (*AIProviderInfo, error) {
	m.callsGetActiveAIProvider++
	return m.activeProvider, m.activeErr
}

func (m *mockStore) GetAIProviderByID(_ context.Context, id string) (*AIProviderInfo, error) {
	m.callsGetAIProviderByID++
	if m.providerByIDErr != nil {
		return nil, m.providerByIDErr
	}
	p, ok := m.providersByID[id]
	if !ok {
		return nil, nil
	}
	return p, nil
}

func (m *mockStore) GetAIModelsByProvider(_ context.Context, providerID string) ([]AIModelInfo, error) {
	m.callsGetAIModelsByProvider++
	if m.modelsByProvErr != nil {
		return nil, m.modelsByProvErr
	}
	return m.modelsByProv[providerID], nil
}

func (m *mockStore) GetKBModelOverrides(_ context.Context, kbID string) (*KBModelOverrides, error) {
	m.callsGetKBModelOverrides++
	if m.kbOverridesErr != nil {
		return nil, m.kbOverridesErr
	}
	ov, ok := m.kbOverrides[kbID]
	if !ok {
		return nil, nil
	}
	return ov, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func strPtr(s string) *string { return &s }

func defaultModels() []AIModelInfo {
	return []AIModelInfo{
		{Name: "gpt-4o", IsReasoning: false},
		{Name: "o1", IsReasoning: true},
		{Name: "text-embedding-3-small", IsEmbedding: true},
		{Name: "rerank-english-v3.0", IsRerank: true},
		{Name: "tts-1", IsTts: true},
		{Name: "whisper-1", IsStt: true},
	}
}

func newStore() *mockStore {
	return &mockStore{
		activeProvider: &AIProviderInfo{
			ID:      "prov-1",
			Name:    "OpenAI",
			APIKey:  "sk-test",
			BaseURL: "https://api.openai.com/v1",
		},
		providersByID: map[string]*AIProviderInfo{
			"prov-1": {
				ID:      "prov-1",
				Name:    "OpenAI",
				APIKey:  "sk-test",
				BaseURL: "https://api.openai.com/v1",
			},
			"prov-2": {
				ID:      "prov-2",
				Name:    "Azure",
				APIKey:  "azure-key",
				BaseURL: "https://my-azure.openai.azure.com/openai",
			},
		},
		modelsByProv: map[string][]AIModelInfo{
			"prov-1": defaultModels(),
			"prov-2": {
				{Name: "azure-gpt-4", IsReasoning: false},
				{Name: "azure-embed", IsEmbedding: true},
			},
		},
		kbOverrides: map[string]*KBModelOverrides{},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// Test 1: Resolve global config (no KB) returns active provider + first models.
func TestResolveGlobal(t *testing.T) {
	store := newStore()
	resolver := NewConfigResolver(store)
	ctx := context.Background()

	cfg, err := resolver.Resolve(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ProviderID != "prov-1" {
		t.Errorf("expected ProviderID prov-1, got %s", cfg.ProviderID)
	}
	if cfg.ProviderName != "OpenAI" {
		t.Errorf("expected ProviderName OpenAI, got %s", cfg.ProviderName)
	}
	if cfg.APIKey != "sk-test" {
		t.Errorf("expected APIKey sk-test, got %s", cfg.APIKey)
	}
	if cfg.BaseURL != "https://api.openai.com/v1/" {
		t.Errorf("expected trailing slash in BaseURL, got %s", cfg.BaseURL)
	}
	if cfg.ChatModel != "gpt-4o" {
		t.Errorf("expected ChatModel gpt-4o, got %s", cfg.ChatModel)
	}
	if cfg.EmbeddingModel != "text-embedding-3-small" {
		t.Errorf("expected EmbeddingModel text-embedding-3-small, got %s", cfg.EmbeddingModel)
	}
	if cfg.RerankModel != "rerank-english-v3.0" {
		t.Errorf("expected RerankModel rerank-english-v3.0, got %s", cfg.RerankModel)
	}
	if cfg.TtsModel != "tts-1" {
		t.Errorf("expected TtsModel tts-1, got %s", cfg.TtsModel)
	}
	if cfg.SttModel != "whisper-1" {
		t.Errorf("expected SttModel whisper-1, got %s", cfg.SttModel)
	}
	if len(cfg.ReasoningModels) != 1 || cfg.ReasoningModels[0] != "o1" {
		t.Errorf("expected ReasoningModels [o1], got %v", cfg.ReasoningModels)
	}

	// GetActiveAIProvider must be called (no KB).
	if store.callsGetActiveAIProvider != 1 {
		t.Errorf("expected 1 call to GetActiveAIProvider, got %d", store.callsGetActiveAIProvider)
	}
	if store.callsGetKBModelOverrides != 0 {
		t.Errorf("expected 0 calls to GetKBModelOverrides, got %d", store.callsGetKBModelOverrides)
	}
}

// Test 2: Resolve with KB overrides — KB model names override provider defaults.
func TestResolveWithKBOverrides(t *testing.T) {
	store := newStore()
	store.kbOverrides["kb-abc"] = &KBModelOverrides{
		ChatModel:      strPtr("gpt-4-turbo"),
		EmbeddingModel: strPtr("text-embedding-ada-002"),
		RerankModel:    nil, // no override — keep provider default
	}

	resolver := NewConfigResolver(store)
	ctx := context.Background()

	cfg, err := resolver.Resolve(ctx, "kb-abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ChatModel != "gpt-4-turbo" {
		t.Errorf("expected overridden ChatModel gpt-4-turbo, got %s", cfg.ChatModel)
	}
	if cfg.EmbeddingModel != "text-embedding-ada-002" {
		t.Errorf("expected overridden EmbeddingModel text-embedding-ada-002, got %s", cfg.EmbeddingModel)
	}
	// RerankModel not overridden — should be provider default.
	if cfg.RerankModel != "rerank-english-v3.0" {
		t.Errorf("expected provider default RerankModel rerank-english-v3.0, got %s", cfg.RerankModel)
	}
	// Provider should still be the global active one (no AIConfigID on KB).
	if cfg.ProviderID != "prov-1" {
		t.Errorf("expected ProviderID prov-1, got %s", cfg.ProviderID)
	}
}

// Test 3: Resolve with KB aiConfigId — uses specific provider.
func TestResolveWithKBAIConfigID(t *testing.T) {
	store := newStore()
	store.kbOverrides["kb-azure"] = &KBModelOverrides{
		AIConfigID: strPtr("prov-2"),
	}

	resolver := NewConfigResolver(store)
	ctx := context.Background()

	cfg, err := resolver.Resolve(ctx, "kb-azure")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ProviderID != "prov-2" {
		t.Errorf("expected ProviderID prov-2, got %s", cfg.ProviderID)
	}
	if cfg.ProviderName != "Azure" {
		t.Errorf("expected ProviderName Azure, got %s", cfg.ProviderName)
	}
	if cfg.ChatModel != "azure-gpt-4" {
		t.Errorf("expected ChatModel azure-gpt-4, got %s", cfg.ChatModel)
	}
	if cfg.EmbeddingModel != "azure-embed" {
		t.Errorf("expected EmbeddingModel azure-embed, got %s", cfg.EmbeddingModel)
	}
	// No rerank model in prov-2.
	if cfg.RerankModel != "" {
		t.Errorf("expected empty RerankModel, got %s", cfg.RerankModel)
	}
	// GetAIProviderByID should have been called; GetActiveAIProvider should NOT.
	if store.callsGetAIProviderByID != 1 {
		t.Errorf("expected 1 call to GetAIProviderByID, got %d", store.callsGetAIProviderByID)
	}
	if store.callsGetActiveAIProvider != 0 {
		t.Errorf("expected 0 calls to GetActiveAIProvider, got %d", store.callsGetActiveAIProvider)
	}
}

// Test 4: Cache hit — second call doesn't hit store.
func TestCacheHit(t *testing.T) {
	store := newStore()
	resolver := NewConfigResolver(store)
	ctx := context.Background()

	_, err := resolver.Resolve(ctx, "")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	_, err = resolver.Resolve(ctx, "")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}

	if store.callsGetActiveAIProvider != 1 {
		t.Errorf("expected exactly 1 DB call, got %d", store.callsGetActiveAIProvider)
	}
	if store.callsGetAIModelsByProvider != 1 {
		t.Errorf("expected exactly 1 models DB call, got %d", store.callsGetAIModelsByProvider)
	}
}

// Test 5: No active config — returns error.
func TestNoActiveConfig(t *testing.T) {
	store := newStore()
	store.activeProvider = nil // simulate no active provider

	resolver := NewConfigResolver(store)
	ctx := context.Background()

	_, err := resolver.Resolve(ctx, "")
	if err == nil {
		t.Fatal("expected error when no active provider, got nil")
	}
}

// Test 6: InvalidateAll clears cache.
func TestInvalidateAll(t *testing.T) {
	store := newStore()
	resolver := NewConfigResolver(store)
	ctx := context.Background()

	// Populate cache.
	_, err := resolver.Resolve(ctx, "")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	resolver.InvalidateAll()

	// Second resolve should hit the store again.
	_, err = resolver.Resolve(ctx, "")
	if err != nil {
		t.Fatalf("second resolve after InvalidateAll: %v", err)
	}

	if store.callsGetActiveAIProvider != 2 {
		t.Errorf("expected 2 calls to GetActiveAIProvider after invalidation, got %d", store.callsGetActiveAIProvider)
	}
}

// Test 7: Invalidate (single key) clears only that entry.
func TestInvalidateSingleKey(t *testing.T) {
	store := newStore()
	// Use a KB with an explicit AIConfigID so its resolution does NOT call
	// GetActiveAIProvider, making the call counts easier to reason about.
	store.kbOverrides["kb-1"] = &KBModelOverrides{
		AIConfigID: strPtr("prov-1"),
	}

	resolver := NewConfigResolver(store)
	ctx := context.Background()

	// Populate two cache entries.
	if _, err := resolver.Resolve(ctx, ""); err != nil {
		t.Fatalf("global resolve: %v", err)
	}
	if _, err := resolver.Resolve(ctx, "kb-1"); err != nil {
		t.Fatalf("kb-1 resolve: %v", err)
	}

	// Invalidate only the global entry.
	resolver.Invalidate("")

	// Resolve global again — should hit store.
	if _, err := resolver.Resolve(ctx, ""); err != nil {
		t.Fatalf("global re-resolve: %v", err)
	}
	// Resolve kb-1 again — should be served from cache (no extra store call).
	if _, err := resolver.Resolve(ctx, "kb-1"); err != nil {
		t.Fatalf("kb-1 re-resolve: %v", err)
	}

	// GetActiveAIProvider: 1 (initial global) + 1 (after invalidation) = 2.
	// kb-1 uses AIConfigID so it never calls GetActiveAIProvider.
	if store.callsGetActiveAIProvider != 2 {
		t.Errorf("expected 2 calls to GetActiveAIProvider, got %d", store.callsGetActiveAIProvider)
	}
	// GetKBModelOverrides: 1 (initial kb-1 only; second is served from cache).
	if store.callsGetKBModelOverrides != 1 {
		t.Errorf("expected 1 call to GetKBModelOverrides, got %d", store.callsGetKBModelOverrides)
	}
	// GetAIProviderByID: 1 (initial kb-1 lookup of prov-1; second is cached).
	if store.callsGetAIProviderByID != 1 {
		t.Errorf("expected 1 call to GetAIProviderByID, got %d", store.callsGetAIProviderByID)
	}
}

// Test 8: BaseURL defaults to OpenAI when empty.
func TestBaseURLDefault(t *testing.T) {
	store := newStore()
	store.activeProvider.BaseURL = ""

	resolver := NewConfigResolver(store)
	cfg, err := resolver.Resolve(context.Background(), "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.BaseURL != defaultBaseURL {
		t.Errorf("expected default BaseURL %s, got %s", defaultBaseURL, cfg.BaseURL)
	}
}

// Test 9: Store error propagates.
func TestStoreErrorPropagates(t *testing.T) {
	store := newStore()
	store.activeErr = errors.New("db down")

	resolver := NewConfigResolver(store)
	_, err := resolver.Resolve(context.Background(), "")
	if err == nil {
		t.Fatal("expected error to propagate, got nil")
	}
}

// Test 10: Cache expires after TTL (unit-testable via time mock — verified via
// direct cache manipulation since we can't mock time without interface injection).
func TestCacheExpiry(t *testing.T) {
	store := newStore()
	resolver := NewConfigResolver(store)
	ctx := context.Background()

	// Populate cache with an already-expired entry.
	if _, err := resolver.Resolve(ctx, ""); err != nil {
		t.Fatalf("initial resolve: %v", err)
	}

	// Manually expire the cache entry.
	resolver.mu.Lock()
	resolver.cache["global"].expiresAt = time.Now().Add(-1 * time.Second)
	resolver.mu.Unlock()

	// Next resolve should bypass cache and call store again.
	if _, err := resolver.Resolve(ctx, ""); err != nil {
		t.Fatalf("post-expiry resolve: %v", err)
	}

	if store.callsGetActiveAIProvider != 2 {
		t.Errorf("expected 2 calls to GetActiveAIProvider after expiry, got %d", store.callsGetActiveAIProvider)
	}
}
