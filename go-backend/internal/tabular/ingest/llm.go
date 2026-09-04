package ingest

import (
	"context"
	"errors"

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

// ProfileTableRegion implements profile.LLMProfiler. A nil Resolver is a
// caller wiring bug (Task 6/7 must not build an AIProfiler without one), but
// Ingest treats every LLMProfiler failure as soft (logged, heuristic kept —
// see ingester.go), so this must return an error rather than let
// ai.ProfileTableRegion panic dereferencing a nil resolver.
func (p *AIProfiler) ProfileTableRegion(ctx context.Context, req ai.SheetProfileRequest) (ai.SheetProfileProposal, error) {
	if p.Resolver == nil {
		return ai.SheetProfileProposal{}, errors.New("tabular/ingest: nil ai resolver")
	}
	return ai.ProfileTableRegion(ctx, p.Resolver, req, p.KBID, p.Lang, p.Model)
}
