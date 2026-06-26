package chat

import (
	"context"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/vector"
)

// communitySearcher is the minimal search surface the primer helper needs
// (satisfied by vector.Searcher / *vector.SearchService).
type communitySearcher interface {
	Search(ctx context.Context, kbID, query string, limit int, opts vector.SearchOptions) (*vector.SearchResult, error)
}

// retrieveCommunityPrimers returns the top-K KG community summaries most
// relevant to the query (a node_kind='community_summary'-filtered search),
// WITH their content. Best-effort: a nil searcher, topK<1, a search error,
// or an empty result yields nil.
func retrieveCommunityPrimers(ctx context.Context, searchSvc communitySearcher, kbID, query string, topK int) []vector.SearchChunk {
	if searchSvc == nil || topK < 1 {
		return nil
	}
	res, err := searchSvc.Search(ctx, kbID, query, topK, vector.SearchOptions{NodeKindFilter: "community_summary"})
	if err != nil {
		logctx.From(ctx).Warn("community primer: search failed; skipping", "kb_id", kbID, "err", err)
		return nil
	}
	if res == nil || len(res.Chunks) == 0 {
		return nil
	}
	return res.Chunks
}

// retrieveCommunityPrimerIDs returns the chunk ids of the community summaries
// (the P2-B injection lane). Delegates to retrieveCommunityPrimers; unchanged
// behaviour — same chunks, same id order, empty ids skipped. Returns nil when
// the delegate returns nil (error / no results / guards) to preserve the
// pre-refactor nil contract.
func retrieveCommunityPrimerIDs(ctx context.Context, searchSvc communitySearcher, kbID, query string, topK int) []string {
	chunks := retrieveCommunityPrimers(ctx, searchSvc, kbID, query, topK)
	if chunks == nil {
		return nil
	}
	ids := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if c.ID != "" {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// shouldInjectCommunityPrimer reports whether community-primed global search
// should fire for this turn: gated + a global-synthesis complex query.
func shouldInjectCommunityPrimer(ctx context.Context, reader SiteConfigReader, queryType, query string) bool {
	return ChatCommunitySearchEnabled(ctx, reader) &&
		queryType == "complex_reasoning" &&
		IsGlobalSynthesisQuery(query)
}
