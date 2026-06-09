package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// DeepChatParams
// ---------------------------------------------------------------------------

// DeepChatParams holds the parameters for a deep chat research run.
type DeepChatParams struct {
	KbID           string
	ChatID         string
	UserMsgID      string
	Message        string   // original user message
	Query          string   // condensed search query
	Language       string   // "de" or "en"
	FileIDs        []string // optional file scope
	KbSystemPrompt string
	ReasoningLevel string
	// PlanningModel names an alternative chat model used for deep-chat
	// planning LLM calls (RewriteQuery, ExpandQuery, HyDE, MultiQuery).
	// Empty = use the resolver's ChatModel. The final streamed answer
	// always uses the main ChatModel, regardless of this override.
	PlanningModel string
	// QueryType is the upstream query-type classification (see
	// vector.QueryType* constants). Plumbed through to SearchOptions so
	// the search pipeline can surface it on the rag.search.stages event.
	QueryType string
	// GraphChunkIDs forwards the AP-C4 graph router's resolved
	// subgraph chunk IDs (chat.ResolveGraphChunksIfEnabled) into both
	// search passes' SearchOptions. Empty (default) preserves legacy
	// behaviour. Same field on every orchestrator's params struct so
	// the http_send.go dispatcher can compute it once and forward.
	GraphChunkIDs []string
	// HyPESearch enables the HyPE query-time arm on this orchestrator's
	// initial search (resolved from hype_search_enabled at dispatch).
	HyPESearch bool
}

// ---------------------------------------------------------------------------
// RunDeepChat
// ---------------------------------------------------------------------------

// RunDeepChat executes a lightweight 2-step research agent that iteratively
// searches the knowledge base, aggregates findings, then returns a ChatContext
// ready for streaming.
//
// Steps:
//  1. Search with original query (HyDE + multi-query enabled).
//  2. Ask the AI to rewrite the query, then search again.
//  3. Deduplicate, truncate, sandwich-order, and build context.
//
// The emit callback streams progress events to the SSE response. If no
// results are found across both steps, an error is returned so the caller
// can fall back to the standard chat path.
func RunDeepChat(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	params DeepChatParams,
	emit func(data map[string]any),
) (*ChatContext, error) {
	const maxTokens = 120_000

	emit(map[string]any{"deepSearch": true})

	// ------------------------------------------------------------------
	// Step 1: Search with original query
	// ------------------------------------------------------------------
	emit(map[string]any{"deepSearchStatus": fmt.Sprintf("Searching: %s", truncateDisplay(params.Query, 80))})

	opts1 := vector.SearchOptions{
		FileIDs:       params.FileIDs,
		HyDE:          true,
		MultiQuery:    true,
		ModelOverride: params.PlanningModel,
		QueryType:     params.QueryType,
		GraphChunkIDs: params.GraphChunkIDs,
		HyPESearch:    params.HyPESearch,
	}
	result1, err := searchSvc.Search(ctx, params.KbID, params.Query, 0, opts1)
	if err != nil {
		logctx.From(ctx).Warn("deep chat: step 1 search failed", "error", err)
		return nil, fmt.Errorf("deep chat: step 1 search: %w", err)
	}

	// Expand neighbors if configured.
	chunks1 := result1.Chunks
	if result1.ContextWindowSize > 0 && result1.TableName != "" {
		chunks1 = searchSvc.ExpandNeighbors(
			ctx, chunks1, result1.ContextWindowSize,
			result1.TableName, params.KbID,
		)
	}

	// ------------------------------------------------------------------
	// Step 2: Rewrite query and search again
	// ------------------------------------------------------------------
	rewrittenQuery, err := ai.RewriteQueryWithModel(ctx, aiResolver, params.Query, params.KbID, params.Language, params.PlanningModel)
	if err != nil || rewrittenQuery == "" {
		// Fall back to expanding the query instead.
		rewrittenQuery, _ = ai.ExpandQueryWithModel(ctx, aiResolver, params.Query, params.KbID, params.Language, params.PlanningModel)
	}
	if rewrittenQuery == "" {
		rewrittenQuery = params.Query
	}

	emit(map[string]any{"deepSearchStatus": fmt.Sprintf("Searching: %s", truncateDisplay(rewrittenQuery, 80))})

	opts2 := vector.SearchOptions{
		FileIDs:       params.FileIDs,
		HyDE:          true,
		MultiQuery:    false, // single query variant is enough for step 2
		ModelOverride: params.PlanningModel,
		QueryType:     params.QueryType,
		GraphChunkIDs: params.GraphChunkIDs,
		HyPESearch:    params.HyPESearch,
	}
	result2, err := searchSvc.Search(ctx, params.KbID, rewrittenQuery, 0, opts2)
	if err != nil {
		logctx.From(ctx).Warn("deep chat: step 2 search failed", "error", err)
		// Non-fatal: continue with step 1 results only.
	}

	var chunks2 []vector.SearchChunk
	if err == nil && result2 != nil {
		chunks2 = result2.Chunks
		if result2.ContextWindowSize > 0 && result2.TableName != "" {
			chunks2 = searchSvc.ExpandNeighbors(
				ctx, chunks2, result2.ContextWindowSize,
				result2.TableName, params.KbID,
			)
		}
	}

	// ------------------------------------------------------------------
	// Merge and deduplicate
	// ------------------------------------------------------------------
	merged := deduplicateChunks(chunks1, chunks2)

	if len(merged) == 0 {
		return nil, fmt.Errorf("deep chat: no results found across both search steps")
	}

	// ------------------------------------------------------------------
	// Truncate, sandwich-order, build context
	// ------------------------------------------------------------------
	emit(map[string]any{"deepSearchStatus": "Preparing answer..."})

	merged = TruncateChunksToFit(merged, maxTokens)
	merged = SandwichOrder(merged)

	// Build context string with annotations (same format as PrepareChatContext).
	sources, contextText := buildChatSourcesAndContext(merged)

	// Build system prompt.
	var sb strings.Builder
	if params.KbSystemPrompt != "" {
		sb.WriteString(params.KbSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompts.ChatSystemPrompt(params.Language))
	if IsLowConfidence(merged) {
		sb.WriteString(prompts.ChatLowConfidenceNotice(params.Language))
	}
	sb.WriteString("\n\nCONTEXT:\n")
	sb.WriteString(contextText)

	// Use the enhanced query from step 1 if available.
	enhancedQuery := result1.EnhancedQuery

	return &ChatContext{
		EnhancedQuery: enhancedQuery,
		SystemPrompt:  sb.String(),
		Sources:       sources,
		Context:       contextText,
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// deduplicateChunks merges two chunk slices, removing duplicates by
// (FileID, chunkIndex). When chunkIndex is not available, deduplication
// falls back to matching by chunk ID.
func deduplicateChunks(a, b []vector.SearchChunk) []vector.SearchChunk {
	type key struct {
		fileID     string
		chunkIndex int
		hasIndex   bool
	}

	seen := make(map[key]struct{}, len(a)+len(b))
	// seenIDs is the fallback bucket for chunks missing a chunkIndex. Most
	// production chunks carry one, so allocate lazily on the first miss to
	// avoid the second map's overhead on the common path.
	var seenIDs map[string]struct{}
	result := make([]vector.SearchChunk, 0, len(a)+len(b))

	add := func(c vector.SearchChunk) {
		ci, hasCI := extractChunkIndex(c.Metadata)
		if hasCI {
			k := key{fileID: c.FileID, chunkIndex: ci, hasIndex: true}
			if _, dup := seen[k]; dup {
				return
			}
			seen[k] = struct{}{}
		} else {
			if seenIDs == nil {
				seenIDs = make(map[string]struct{}, 8)
			}
			if _, dup := seenIDs[c.ID]; dup {
				return
			}
			seenIDs[c.ID] = struct{}{}
		}
		result = append(result, c)
	}

	for _, c := range a {
		add(c)
	}
	for _, c := range b {
		add(c)
	}
	return result
}

// extractChunkIndex reads the "chunkIndex" field from metadata.
func extractChunkIndex(meta map[string]any) (int, bool) {
	if meta == nil {
		return 0, false
	}
	v, ok := meta["chunkIndex"]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return int(i), true
		}
	}
	return 0, false
}

// truncateDisplay truncates a string for display in status messages.
func truncateDisplay(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
