package adkbridge

import (
	"context"
	"fmt"
	"slices"

	"github.com/justrag/go-backend/internal/ai"
)

// ConfigResolver is satisfied by *ai.ConfigResolver.
type ConfigResolver interface {
	Resolve(ctx context.Context, kbID string) (*ai.ResolvedConfig, error)
}

// ModelFactory builds a Model for one KB, using the same provider/model
// resolution as every other completion call (admin providers, per-KB
// overrides, ai.CachedClient so the concurrency limiter is shared).
type ModelFactory struct{ r ConfigResolver }

// NewModelFactory returns a factory over r.
func NewModelFactory(r ConfigResolver) *ModelFactory { return &ModelFactory{r: r} }

// For returns the model for kbID. modelOverride is honoured only when the
// provider offers it. reasoningEffort is applied only when the effective
// model is flagged is_reasoning — sending enable_thinking to a non-reasoning
// model is at best ignored and at worst rejected.
func (f *ModelFactory) For(ctx context.Context, kbID, modelOverride, reasoningEffort string) (*Model, error) {
	cfg, err := f.r.Resolve(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("adkbridge: resolve model for kb %q: %w", kbID, err)
	}
	name := cfg.EffectiveChatModel(modelOverride)
	var opts []Option
	if reasoningEffort != "" && slices.Contains(cfg.ReasoningModels, name) {
		opts = append(opts, WithReasoning(reasoningEffort))
	}
	return NewModel(ai.CachedClient(cfg.BaseURL, cfg.APIKey), name, opts...), nil
}
