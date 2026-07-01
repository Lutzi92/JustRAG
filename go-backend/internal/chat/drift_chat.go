package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
)

// driftPrimerCap bounds the concatenated community-summary text (in runes)
// fed to the follow-up generator — keeps the fast-tier call cheap.
const driftPrimerCap = 6000

// DriftChatParams configures a full iterative DRIFT run. Produces the same
// *ChatContext shape as the other orchestrators so the streaming caller
// doesn't branch on which planner ran.
type DriftChatParams struct {
	KbID           string
	Query          string
	Language       string
	FileIDs        []string
	KbSystemPrompt string
	PlanningModel  string // resolved follow-up-gen model (fast tier)
	MaxFollowups   int    // 1..8
	PrimerTopK     int    // 1..20
	SearchTopK     int    // 1..30
	GraphChunkIDs  []string
	BridgeChunks   map[string]int
	HyPESearch     bool
	// CurrentDateLine is the localized current-date line to append to the
	// answer system prompt (empty when chat_date_awareness_enabled is off).
	// Set at dispatch via SystemPromptDateLine.
	CurrentDateLine string
}

// driftFollowupFn is the injectable seam for ai.GenerateDriftFollowups.
type driftFollowupFn func(ctx context.Context, resolver *ai.ConfigResolver, kbID, query, primerText, language, modelOverride string) ([]string, error)

// RunDriftChat is the production entry point. Mirrors RunAgenticChat's
// signature; the caller streams the answer from the returned ChatContext.
func RunDriftChat(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	params DriftChatParams,
	emit func(data map[string]any),
) (*ChatContext, error) {
	return runDriftChatTestable(ctx, aiResolver, searchSvc, ai.GenerateDriftFollowups, params, emit)
}

func runDriftChatTestable(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searcher searchInvoker,
	genFollowups driftFollowupFn,
	params DriftChatParams,
	emit func(data map[string]any),
) (*ChatContext, error) {
	const maxTokens = 120_000

	maxFollowups := params.MaxFollowups
	if maxFollowups < 1 || maxFollowups > 8 {
		maxFollowups = 4
	}
	primerTopK := params.PrimerTopK
	if primerTopK < 1 || primerTopK > 20 {
		primerTopK = 6
	}
	searchTopK := params.SearchTopK
	if searchTopK < 1 || searchTopK > 30 {
		searchTopK = 8
	}

	// Stage 1: community primer (best-effort).
	primerChunks := retrieveCommunityPrimers(ctx, searcher, params.KbID, params.Query, primerTopK)
	primerText := buildPrimerText(primerChunks, driftPrimerCap)
	emitTrajectory(emit, TrajectoryEvent{Stage: "primer", Findings: len(primerChunks)}, nil)

	outcome := "primed"
	if len(primerChunks) == 0 {
		outcome = "primerless"
	}

	// Stage 2: follow-up sub-questions (primer-conditioned).
	followups, ferr := genFollowups(ctx, aiResolver, params.KbID, params.Query, primerText, params.Language, params.PlanningModel)
	if ferr != nil || len(followups) == 0 {
		if ferr != nil {
			logctx.From(ctx).Warn("drift_chat: follow-up generation failed; falling back to original query", "error", ferr)
		}
		followups = []string{params.Query}
		outcome = "fallback"
	}
	if len(followups) > maxFollowups {
		followups = followups[:maxFollowups]
	}
	emitTrajectory(emit, TrajectoryEvent{Stage: "drift_followups", Queries: followups}, nil)

	// Stage 3: one light search per follow-up, seeded with the primer chunks.
	accumulated := append([]vector.SearchChunk(nil), primerChunks...)
	for i, q := range followups {
		opts := vector.SearchOptions{
			FileIDs:       params.FileIDs,
			HyDE:          false,
			MultiQuery:    false,
			ModelOverride: params.PlanningModel,
			QueryType:     vector.QueryTypeComplexReasoning,
			GraphChunkIDs: params.GraphChunkIDs,
			BridgeChunks:  params.BridgeChunks,
			HyPESearch:    params.HyPESearch,
		}
		res, serr := searcher.Search(ctx, params.KbID, q, searchTopK, opts)
		if serr != nil {
			logctx.From(ctx).Warn("drift_chat: follow-up search failed; skipping", "query", q, "error", serr)
			emitTrajectory(emit, TrajectoryEvent{Stage: "search", Step: i + 1, Query: q, Findings: 0}, nil)
			continue
		}
		before := len(accumulated)
		accumulated = deduplicateChunks(accumulated, res.Chunks)
		emitTrajectory(emit, TrajectoryEvent{
			Stage:    "search",
			Step:     i + 1,
			Query:    q,
			Findings: len(accumulated) - before,
			Chunks:   chunkRefs(res.Chunks),
		}, nil)
	}

	if len(accumulated) == 0 {
		// Nothing to answer from — return an error so the dispatcher falls
		// through to the standard path (mirrors RunAgenticChat's empty hop-1).
		return nil, fmt.Errorf("drift chat: no results")
	}

	observability.RecordDriftRun(outcome)
	emitTrajectory(emit, TrajectoryEvent{Stage: "answer", Decision: outcome}, nil)

	// Stage 4: assemble — same tail as RunAgenticChat.
	accumulated = TruncateChunksToFit(accumulated, maxTokens)
	accumulated = SandwichOrder(accumulated)
	sources, contextText := buildChatSourcesAndContext(accumulated)

	var sb strings.Builder
	if params.KbSystemPrompt != "" {
		sb.WriteString(params.KbSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompts.ChatSystemPromptWithDate(params.Language, params.CurrentDateLine))
	if IsLowConfidence(accumulated) {
		sb.WriteString(prompts.ChatLowConfidenceNotice(params.Language))
	}
	sb.WriteString("\n\nCONTEXT:\n")
	sb.WriteString(contextText)

	return &ChatContext{
		SystemPrompt: sb.String(),
		Sources:      sources,
		Context:      contextText,
		FinalChunks:  accumulated,
	}, nil
}

// buildPrimerText concatenates community-summary chunk contents (separated by
// a rule) up to maxRunes, for feeding the follow-up generator.
func buildPrimerText(chunks []vector.SearchChunk, maxRunes int) string {
	parts := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if t := strings.TrimSpace(c.Content); t != "" {
			parts = append(parts, t)
		}
	}
	joined := strings.Join(parts, "\n\n---\n\n")
	r := []rune(joined)
	if len(r) > maxRunes {
		return string(r[:maxRunes])
	}
	return joined
}
