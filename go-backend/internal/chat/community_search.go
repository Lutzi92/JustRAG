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

// retrieveCommunityPrimerIDs returns the chunk ids of the top-K KG community
// summaries most relevant to the query (a node_kind='community_summary'-filtered
// search). Best-effort: a search error or empty result yields nil — the caller
// proceeds with normal retrieval.
func retrieveCommunityPrimerIDs(ctx context.Context, searchSvc communitySearcher, kbID, query string, topK int) []string {
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
	ids := make([]string, 0, len(res.Chunks))
	for _, c := range res.Chunks {
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
