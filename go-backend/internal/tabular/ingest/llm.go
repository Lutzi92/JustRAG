package ingest

import (
	"context"

	"github.com/justrag/go-backend/internal/ai"
)

// AIProfiler adapts ai.ProfileTableRegion to profile.LLMProfiler, binding a
// resolver to the per-file KB id, prompt language, and model override. One
// instance is built per file (Ingester.WithLLM), since the shared Ingester
// itself carries no per-file state.
type AIProfiler struct {
	Resolver          *ai.ConfigResolver
	KBID, Lang, Model string
}

// ProfileTableRegion implements profile.LLMProfiler.
func (p *AIProfiler) ProfileTableRegion(ctx context.Context, req ai.SheetProfileRequest) (ai.SheetProfileProposal, error) {
	return ai.ProfileTableRegion(ctx, p.Resolver, req, p.KBID, p.Lang, p.Model)
}
