package chat

import (
	"context"
	"fmt"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/vector"
)

// RunLongContextChat is the OrchLongContext production entry point (W3-R5).
//
// Until Wave 3 the long-context route lived only inside PrepareChatContext,
// which the streaming chat never reaches for complex_reasoning turns — every
// such turn goes through tryDeepChat. Promoting it to an orchestrator is what
// makes the route reachable in production streaming chat at all.
//
// Shape: one wide retrieval pass (LongContextMode + LongContextTopK, i.e. no
// MMR / score-drop / parent-child swap) → consumeLongContext, which either
// hands the pool to the answer LLM raw (flat) or maps it to per-group findings
// first (map_reduce). Mirrors RunDriftChat's signature; the caller streams the
// answer from the returned ChatContext.
func RunLongContextChat(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	cfg SiteConfigReader,
	p LongContextParams,
) (*ChatContext, error) {
	return runLongContextChatTestable(ctx, aiResolver, searchSvc, ai.ExtractLongContextFindings, cfg, p)
}

func runLongContextChatTestable(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searcher searchInvoker,
	extract extractFindingsFn,
	cfg SiteConfigReader,
	p LongContextParams,
) (*ChatContext, error) {
	p = resolveLongContextParams(ctx, cfg, p)

	emitTrajectory(p.Emit, TrajectoryEvent{
		Stage: "longcontext_route",
		Mode:  p.Mode,
		Query: p.Query,
	}, map[string]any{
		// Legacy shape kept for one release — PrepareChatContext emitted this
		// raw map before the event moved onto the unified envelope.
		"type":       "longcontext_route",
		"query":      p.Query,
		"max_tokens": p.MaxTokens,
		"top_k":      p.TopK,
	})

	opts := vector.SearchOptions{
		LongContextMode: true,
		LongContextTopK: p.TopK,
		QueryType:       vector.QueryTypeComplexReasoning,
		FileIDs:         p.FileIDs,
		RawQuery:        p.RawQuery,
		GraphChunkIDs:   p.GraphChunkIDs,
		BridgeChunks:    p.BridgeChunks,
		HyPESearch:      p.HyPESearch,
	}
	res, err := searcher.Search(ctx, p.KbID, p.Query, 0, opts)
	if err != nil {
		return nil, fmt.Errorf("longcontext chat: search: %w", err)
	}
	if res == nil || len(res.Chunks) == 0 {
		return nil, fmt.Errorf("longcontext chat: no results")
	}

	observability.RecordLongContextRoute("fired", p.Mode)
	logctx.From(ctx).Info("rag.longcontext.fired",
		"query", p.Query,
		"mode", p.Mode,
		"max_tokens", p.MaxTokens,
		"top_k", p.TopK,
		"chunks", len(res.Chunks),
	)

	return consumeLongContextWith(ctx, aiResolver, extract, p, res.Chunks)
}
