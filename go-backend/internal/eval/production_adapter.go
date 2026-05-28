package eval

import (
	"cmp"
	"context"
	"slices"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/vector"
)

// ProductionContextAdapter invokes the full chat.PrepareChatContext pipeline
// (CRAG, enumeration pre-pass, contextual prefix, sandwich order,
// abstain/low-confidence notices) so eval measures the same code path that
// serves users. Used by cmd/eval (--production-context) and the in-app eval
// runner's worker.
type ProductionContextAdapter struct {
	aiResolver       *ai.ConfigResolver
	searchService    vector.Searcher
	siteConfigReader chat.SiteConfigReader
	flags            EvalFlags

	// cache stores the final ChatContext per question so judge-mode can
	// reuse the assembled system prompt and context text without a second
	// retrieval pass.
	cache map[string]*chat.ChatContext
}

// EvalFlags carries the flag values through the adapter without a global.
type EvalFlags struct {
	Enhance    string
	HyDE       bool
	MultiQuery bool
	// StepBack force-enables step-back retrieval for this eval run regardless
	// of the live `step_back_enabled` site_config — the production adapter
	// passes it into ChatContextParams.StepBack which is OR-combined with the
	// site_config inside PrepareChatContext.
	StepBack                bool
	ForceEnumerationPrepass *bool
}

// NewProductionContextAdapter constructs a ProductionContextAdapter with the
// given dependencies. All fields are required (siteConfigReader may be wrapped
// to override CRAG config).
func NewProductionContextAdapter(
	aiResolver *ai.ConfigResolver,
	searchService vector.Searcher,
	siteConfigReader chat.SiteConfigReader,
	flags EvalFlags,
) *ProductionContextAdapter {
	return &ProductionContextAdapter{
		aiResolver:       aiResolver,
		searchService:    searchService,
		siteConfigReader: siteConfigReader,
		flags:            flags,
	}
}

// Search runs chat.PrepareChatContext for the question and caches the
// resulting ChatContext so judge-mode can reuse it via ChatContextForQuestion.
func (a *ProductionContextAdapter) Search(ctx context.Context, q Question, k int) ([]RetrievedChunk, error) {
	params := chat.ChatContextParams{
		KbID:                    q.KbID,
		SearchQuery:             q.Question,
		Language:                q.Language,
		Enhance:                 a.flags.Enhance,
		HyDE:                    a.flags.HyDE,
		MultiQuery:              a.flags.MultiQuery,
		StepBack:                a.flags.StepBack,
		ForceEnumerationPrepass: a.flags.ForceEnumerationPrepass,
	}
	chatCtx, err := chat.PrepareChatContext(ctx, a.aiResolver, a.searchService, a.siteConfigReader, params)
	if err != nil {
		return nil, err
	}
	if a.cache == nil {
		a.cache = make(map[string]*chat.ChatContext)
	}
	a.cache[q.ID] = chatCtx

	// Project FinalChunks to the evaluator's view. FinalChunks is in
	// SANDWICH order — highest-scored chunks at positions 0 and N-1, next at
	// 1 and N-2, etc. — so the LLM's lost-in-the-middle attention pattern is
	// mitigated. That layout is wrong for retrieval-quality metrics (recall,
	// precision, MRR, nDCG) which all need top-k by score: a chunks[:k] cut
	// against sandwich order silently drops every other top-ranked chunk
	// (rank-2/4/6/8/10 land at sandwich positions 14/13/12/11/10, all of
	// which fall outside k=10 when len=15). Re-sort by score descending
	// here, so the eval measures retrieval quality, not LLM-context layout.
	// The cached ChatContext (used by ContentsForQuestion → judge mode)
	// remains in sandwich order — that's the right layout for grading the
	// LLM's actual answer.
	chunks := append([]vector.SearchChunk(nil), chatCtx.FinalChunks...)
	slices.SortStableFunc(chunks, func(a, b vector.SearchChunk) int {
		return cmp.Compare(b.Score, a.Score)
	})
	if k > 0 && k < len(chunks) {
		chunks = chunks[:k]
	}
	out := make([]RetrievedChunk, 0, len(chunks))
	for _, c := range chunks {
		idx, total := ChunkPositionFromMetadata(c.Metadata)
		out = append(out, RetrievedChunk{
			FileID:      c.FileID,
			FileName:    c.FileName,
			Score:       c.Score,
			ChunkIndex:  idx,
			TotalChunks: total,
		})
	}
	return out, nil
}

// ChatContextForQuestion returns the cached ChatContext for judge-mode answer
// generation. Returns (nil, false) if no retrieval was run for questionID
// (e.g. it errored).
func (a *ProductionContextAdapter) ChatContextForQuestion(questionID string) (*chat.ChatContext, bool) {
	if a.cache == nil {
		return nil, false
	}
	c, ok := a.cache[questionID]
	return c, ok
}

// ContentsForQuestion mirrors the legacy adapter's method so judge-mode code
// can call it uniformly. Returns the chunk content and file-name slices from
// the cached ChatContext, trimmed to at most k entries.
func (a *ProductionContextAdapter) ContentsForQuestion(questionID string, k int) (contents []string, fileNames []string, ok bool) {
	c, found := a.ChatContextForQuestion(questionID)
	if !found {
		return nil, nil, false
	}
	chunks := c.FinalChunks
	if k > 0 && k < len(chunks) {
		chunks = chunks[:k]
	}
	contents = make([]string, len(chunks))
	fileNames = make([]string, len(chunks))
	for i, ch := range chunks {
		contents[i] = ch.Content
		fileNames[i] = ch.FileName
	}
	return contents, fileNames, true
}
