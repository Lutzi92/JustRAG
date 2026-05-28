package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

const cacheTTL = 30 * time.Second

// ErrNoActiveProvider is returned by Resolve when no AI provider is active in
// the database — typically a fresh install before the admin has configured a
// provider. Callers (e.g. the ingestion enrichment loop) use errors.Is to
// distinguish "AI not configured yet" from a real provider/network failure and
// degrade gracefully with a single info log instead of flooding warnings.
var ErrNoActiveProvider = errors.New("ai: no active AI provider configured")

// ---------------------------------------------------------------------------
// Types exposed by this package
// ---------------------------------------------------------------------------

// ResolvedConfig holds a fully resolved AI provider configuration for a
// given KB (or for the global active provider when no KB is specified).
type ResolvedConfig struct {
	ProviderID      string
	ProviderName    string
	APIKey          string
	BaseURL         string // always has trailing slash; defaults to defaultBaseURL
	ChatModel       string
	ChatModels      []string // all chat-capable model names for the resolved provider
	EmbeddingModel  string
	RerankModel     string   // empty if none configured
	TtsModel        string   // empty if none configured
	SttModel        string   // empty if none configured
	ReasoningModels []string // names of models where is_reasoning = true
}

// AIProviderInfo holds the raw provider fields needed for config resolution.
type AIProviderInfo struct {
	ID      string
	Name    string
	APIKey  string
	BaseURL string
}

// AIModelInfo holds the raw model fields needed for config resolution.
type AIModelInfo struct {
	Name        string
	IsReasoning bool
	IsEmbedding bool
	IsRerank    bool
	IsTts       bool
	IsStt       bool
}

// KBModelOverrides holds the optional model-name overrides stored on a KB row.
type KBModelOverrides struct {
	AIConfigID     *string
	ChatModel      *string
	EmbeddingModel *string
	RerankModel    *string
	TtsModel       *string
	SttModel       *string
}

// ---------------------------------------------------------------------------
// ConfigStore interface
// ---------------------------------------------------------------------------

// ConfigStore is the subset of the DB store that ConfigResolver needs.
type ConfigStore interface {
	// GetActiveAIProvider returns the provider whose is_active flag is true.
	// Returns nil, nil when no active provider exists.
	GetActiveAIProvider(ctx context.Context) (*AIProviderInfo, error)

	// GetAIProviderByID returns the provider with the given ID.
	// Returns nil, nil when the provider does not exist.
	GetAIProviderByID(ctx context.Context, id string) (*AIProviderInfo, error)

	// GetAIModelsByProvider returns all models belonging to providerID.
	GetAIModelsByProvider(ctx context.Context, providerID string) ([]AIModelInfo, error)

	// GetKBModelOverrides returns the AI-related override columns for the KB with kbID.
	// Returns nil, nil when the KB does not exist.
	GetKBModelOverrides(ctx context.Context, kbID string) (*KBModelOverrides, error)
}

// ---------------------------------------------------------------------------
// Cache
// ---------------------------------------------------------------------------

type cacheEntry struct {
	config    *ResolvedConfig
	expiresAt time.Time
}

// ---------------------------------------------------------------------------
// ConfigResolver
// ---------------------------------------------------------------------------

// ConfigResolver resolves AI provider configuration for a given (optional) KB,
// with a short-lived in-memory cache keyed by KB ID (or "global").
type ConfigResolver struct {
	store ConfigStore
	mu    sync.RWMutex
	cache map[string]*cacheEntry
	group singleflight.Group

	// completionHook is an optional per-instance override for the LLM-call path
	// used by NeedsMoreInfo, VerifyFactuality, DecideNextAction, PlanQueries,
	// etc. Production code leaves it nil and falls through to
	// GenerateCompletionWithModel. Tests construct a hook-bearing resolver via
	// newTestResolverWithCompletion (see completion_hook_test.go) to stub the
	// LLM round-trip without spinning up a real provider. Held in an
	// atomic.Pointer so a test that swaps it mid-flight stays race-free with
	// concurrent callers and the race detector.
	completionHook atomic.Pointer[completionFunc]
}

// NewConfigResolver creates a ConfigResolver backed by store.
func NewConfigResolver(store ConfigStore) *ConfigResolver {
	return &ConfigResolver{
		store: store,
		cache: make(map[string]*cacheEntry),
	}
}

// Resolve returns a ResolvedConfig for kbID. When kbID is empty the global
// active provider is used. Results are cached for cacheTTL. Concurrent
// requests for the same key are coalesced via singleflight to prevent
// cache stampedes on expiry.
func (r *ConfigResolver) Resolve(ctx context.Context, kbID string) (*ResolvedConfig, error) {
	key := cacheKey(kbID)

	// Fast path: cache hit.
	r.mu.RLock()
	entry, ok := r.cache[key]
	r.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.config, nil
	}

	// Slow path: resolve from DB, coalescing concurrent requests.
	// DoChan lets each caller select on their own context while the shared
	// DB lookup uses a detached context — if the winning caller disconnects,
	// the lookup continues for the remaining waiters, but any individual
	// caller can still bail early when their own context is cancelled.
	ch := r.group.DoChan(key, func() (any, error) {
		// Double-check cache inside singleflight — another goroutine may have filled it.
		r.mu.RLock()
		entry, ok := r.cache[key]
		r.mu.RUnlock()
		if ok && time.Now().Before(entry.expiresAt) {
			return entry.config, nil
		}

		// Detach cancellation (singleflight winner may outlive the originating
		// request) but preserve tracing/request-id via context.WithoutCancel.
		resolveCtx, resolveCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer resolveCancel()

		cfg, err := r.resolve(resolveCtx, kbID)
		if err != nil {
			return nil, err
		}

		r.mu.Lock()
		r.cache[key] = &cacheEntry{config: cfg, expiresAt: time.Now().Add(cacheTTL)}
		r.mu.Unlock()

		return cfg, nil
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(*ResolvedConfig), nil
	}
}

// Invalidate removes the cached entry for kbID (or the global entry when kbID
// is empty).
func (r *ConfigResolver) Invalidate(kbID string) {
	r.mu.Lock()
	delete(r.cache, cacheKey(kbID))
	r.mu.Unlock()
}

// InvalidateAll clears the entire cache.
func (r *ConfigResolver) InvalidateAll() {
	r.mu.Lock()
	r.cache = make(map[string]*cacheEntry)
	r.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Internal resolution logic
// ---------------------------------------------------------------------------

func (r *ConfigResolver) resolve(ctx context.Context, kbID string) (*ResolvedConfig, error) {
	var overrides *KBModelOverrides

	// Step 1: if a KB ID was provided, fetch its model overrides.
	if kbID != "" {
		var err error
		overrides, err = r.store.GetKBModelOverrides(ctx, kbID)
		if err != nil {
			return nil, fmt.Errorf("ai: get KB model overrides: %w", err)
		}
		// overrides may be nil if the KB doesn't exist; we fall through to global.
	}

	// Step 2: determine the provider to use.
	var provider *AIProviderInfo
	if overrides != nil && overrides.AIConfigID != nil {
		var err error
		provider, err = r.store.GetAIProviderByID(ctx, *overrides.AIConfigID)
		if err != nil {
			return nil, fmt.Errorf("ai: get provider by id: %w", err)
		}
	}
	if provider == nil {
		var err error
		provider, err = r.store.GetActiveAIProvider(ctx)
		if err != nil {
			return nil, fmt.Errorf("ai: get active provider: %w", err)
		}
	}
	if provider == nil {
		return nil, ErrNoActiveProvider
	}

	// Step 3: load models for the resolved provider.
	models, err := r.store.GetAIModelsByProvider(ctx, provider.ID)
	if err != nil {
		return nil, fmt.Errorf("ai: get models for provider: %w", err)
	}

	// Step 4: classify models.
	var chatModels, embeddingModels, rerankModels, ttsModels, sttModels []AIModelInfo
	for _, m := range models {
		switch {
		case m.IsEmbedding:
			embeddingModels = append(embeddingModels, m)
		case m.IsRerank:
			rerankModels = append(rerankModels, m)
		case m.IsTts:
			ttsModels = append(ttsModels, m)
		case m.IsStt:
			sttModels = append(sttModels, m)
		default:
			// All remaining models (including reasoning) are chat models.
			chatModels = append(chatModels, m)
		}
	}

	var reasoningNames []string
	for _, m := range chatModels {
		if m.IsReasoning {
			reasoningNames = append(reasoningNames, m.Name)
		}
	}

	// Step 5: pick defaults from provider model lists.
	cfg := &ResolvedConfig{
		ProviderID:      provider.ID,
		ProviderName:    provider.Name,
		APIKey:          provider.APIKey,
		BaseURL:         normalizeBaseURL(provider.BaseURL),
		ReasoningModels: reasoningNames,
	}

	if len(chatModels) > 0 {
		cfg.ChatModel = chatModels[0].Name
		names := make([]string, len(chatModels))
		for i, m := range chatModels {
			names[i] = m.Name
		}
		cfg.ChatModels = names
	}
	if len(embeddingModels) > 0 {
		cfg.EmbeddingModel = embeddingModels[0].Name
	}
	if len(rerankModels) > 0 {
		cfg.RerankModel = rerankModels[0].Name
	}
	if len(ttsModels) > 0 {
		cfg.TtsModel = ttsModels[0].Name
	}
	if len(sttModels) > 0 {
		cfg.SttModel = sttModels[0].Name
	}

	// Step 6: apply KB overrides where set.
	if overrides != nil {
		if overrides.ChatModel != nil && *overrides.ChatModel != "" {
			cfg.ChatModel = *overrides.ChatModel
		}
		if overrides.EmbeddingModel != nil && *overrides.EmbeddingModel != "" {
			cfg.EmbeddingModel = *overrides.EmbeddingModel
		}
		if overrides.RerankModel != nil && *overrides.RerankModel != "" {
			cfg.RerankModel = *overrides.RerankModel
		}
		if overrides.TtsModel != nil && *overrides.TtsModel != "" {
			cfg.TtsModel = *overrides.TtsModel
		}
		if overrides.SttModel != nil && *overrides.SttModel != "" {
			cfg.SttModel = *overrides.SttModel
		}
	}

	return cfg, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func cacheKey(kbID string) string {
	if kbID == "" {
		return "global"
	}
	return "kb:" + kbID
}

// normalizeBaseURL ensures the base URL has a trailing slash and falls back to
// the OpenAI default when empty.
func normalizeBaseURL(u string) string {
	if u == "" {
		return defaultBaseURL
	}
	if !strings.HasSuffix(u, "/") {
		return u + "/"
	}
	return u
}
