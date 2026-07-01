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

// AgenticChatParams configures a multi-hop agentic chat run. Mirrors
// DeepChatParams plus a MaxHops field; produces the same *ChatContext
// shape so the streaming caller doesn't need to branch on which planner
// ran.
type AgenticChatParams struct {
	KbID           string
	Query          string
	Language       string
	FileIDs        []string
	KbSystemPrompt string
	PlanningModel  string // model used for HyDE / MultiQuery / NeedsMoreInfo
	MaxHops        int    // 1..5 (clamped by ChatAgenticMaxHops range guard)
	// Plateau is the resolved Phase 1 §1.3 quality-plateau early-stop
	// configuration. When Plateau.Enabled is true the loop tracks per-hop
	// `topScore` + `chunksAdded` deltas and breaks early after two
	// consecutive low-yield rounds. Zero value (Enabled=false) is the
	// production default — the legacy zero-progress break still fires.
	Plateau PlateauConfig
	// GraphChunkIDs forwards the AP-C4 graph router's resolved
	// subgraph chunk IDs (chat.ResolveGraphChunksIfEnabled) into the
	// hop-1 SearchOptions. Subsequent hops are focused gap-fillers
	// against a smaller scope, so they intentionally don't carry the
	// graph injection — the contribution belongs to the initial pool.
	GraphChunkIDs []string
	// BridgeChunks forwards the bridge-evidence tally (chunk_id -> bridge
	// count) into the hop-1 SearchOptions for post-rerank multi-hop
	// boosting. Nil (default) leaves the boost inert.
	BridgeChunks map[string]int
	// HyPESearch enables the HyPE query-time arm on this orchestrator's
	// initial search (resolved from hype_search_enabled at dispatch).
	HyPESearch bool
	// CurrentDateLine is the localized current-date line to append to the
	// answer system prompt (empty when chat_date_awareness_enabled is off).
	// Set at dispatch via SystemPromptDateLine.
	CurrentDateLine string
}

// searchInvoker is the minimal vector.SearchService surface RunAgenticChat
// needs. Defining it here lets the tests inject a fake searcher without
// importing the full search service.
type searchInvoker interface {
	Search(ctx context.Context, kbID, query string, limit int, opts vector.SearchOptions) (*vector.SearchResult, error)
}

// critiqueFn is the signature ai.NeedsMoreInfo provides. Pulled out so
// tests can inject a deterministic stub.
type critiqueFn func(ctx context.Context, resolver *ai.ConfigResolver, kbID, question, chunkSummaries, language, modelOverride string) (sufficient bool, followUpQuery string, err error)

// RunAgenticChat is the production entry point. Mirrors RunDeepChat's
// signature so tryDeepChat can swap one for the other based on
// site_config gates. Returns the same *ChatContext shape; the caller
// streams the answer using that context.
//
// Answer-time tool calling: the final SSE-streamed completion runs in
// http_send.go's shared streaming block, not here. RunAgenticChat
// returns a ChatContext; the handler then routes through
// RunAnswerWithTools or the legacy ai.StreamCompletion based on the
// chat_answer_tools_enabled gate (see
// docs/superpowers/specs/2026-05-13-llm-answer-tool-calling-design.md).
func RunAgenticChat(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	params AgenticChatParams,
	emit func(data map[string]any),
) (*ChatContext, error) {
	return runAgenticChatTestable(ctx, aiResolver, searchSvc, ai.NeedsMoreInfo, params, emit)
}

// runAgenticChatTestable accepts the search invoker and critique function
// as injectable arguments so unit tests can drive the loop without a real
// SearchService or LLM. Production callers go through RunAgenticChat.
func runAgenticChatTestable(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searcher searchInvoker,
	critique critiqueFn,
	params AgenticChatParams,
	emit func(data map[string]any),
) (*ChatContext, error) {
	const maxTokens = 120_000
	const chunkSummaryBudget = 3000

	maxHops := params.MaxHops
	if maxHops < 1 || maxHops > 5 {
		maxHops = 3
	}

	emitTrajectory(emit,
		TrajectoryEvent{Stage: "hop", Step: 1, Query: params.Query},
		map[string]any{"agenticHop": 1, "agenticQuery": params.Query},
	)

	// Hop 1: standard search with HyDE + MultiQuery (matches RunDeepChat
	// step 1). Subsequent hops use plain search since the follow-up query
	// is already a focused gap-filler — re-running HyDE on it would
	// introduce noise.
	opts1 := vector.SearchOptions{
		FileIDs:       params.FileIDs,
		HyDE:          true,
		MultiQuery:    true,
		ModelOverride: params.PlanningModel,
		QueryType:     vector.QueryTypeComplexReasoning,
		GraphChunkIDs: params.GraphChunkIDs,
		BridgeChunks:  params.BridgeChunks,
		HyPESearch:    params.HyPESearch,
	}
	result1, err := searcher.Search(ctx, params.KbID, params.Query, 0, opts1)
	if err != nil {
		return nil, fmt.Errorf("agentic chat: hop 1 search: %w", err)
	}
	accumulated := result1.Chunks
	if len(accumulated) == 0 {
		return nil, fmt.Errorf("agentic chat: no results in hop 1")
	}

	// Loop: hops 2..maxHops, each gated by a critique call. The outcome
	// records WHY the loop stopped — observed alongside the final hop
	// count for trend analysis.
	outcome := "max_hops_reached"
	// finalHopCount counts attempts (incremented at the top of each loop
	// iteration) rather than successes — so judge_error / search_error /
	// early_stop_no_query outcomes record the hop number where the failure
	// actually happened, not the previous successful one. The histogram
	// reads as "the agent attempted up to N hops before stopping".
	finalHopCount := 1
	// Plateau bookkeeping (Phase 1 §1.3). prevTopScore starts at the hop-1
	// peak so a hop-2 score that fails to improve counts toward the streak
	// from the very first comparison.
	prevTopScore := topChunkScore(accumulated)
	plateauStreak := 0
	budget := TurnBudgetFromContext(ctx)
	for hop := 2; hop <= maxHops; hop++ {
		// AP-A3: budget check at the top of each hop. Time / tokens /
		// tool calls all stop the loop with `budget_exceeded`. The
		// chunks already gathered are still used for the answer.
		if kind := budget.CheckExceeded(ctx, "agentic"); kind != "" {
			outcome = "budget_exceeded"
			emitTrajectory(emit,
				TrajectoryEvent{Stage: "decision", Step: hop, Decision: "budget_exceeded", Reason: kind},
				nil,
			)
			break
		}
		finalHopCount = hop
		summaries := summarizeChunksForCritique(accumulated, chunkSummaryBudget)
		sufficient, followUp, cerr := critique(ctx, aiResolver, params.KbID, params.Query, summaries, params.Language, params.PlanningModel)
		if cerr != nil {
			outcome = "judge_error"
			logctx.From(ctx).Warn("agentic_chat: judge call failed; using accumulated chunks",
				"hop", hop, "error", cerr)
			break
		}
		logctx.From(ctx).Info("rag.agentic_chat.critique",
			"hop", hop,
			"sufficient", sufficient,
			"follow_up_query", strings.TrimSpace(followUp),
			"chunks_accumulated", len(accumulated),
		)
		if sufficient {
			outcome = "early_stop_sufficient"
			break
		}
		followUp = strings.TrimSpace(followUp)
		if followUp == "" {
			outcome = "early_stop_no_query"
			break
		}

		emitTrajectory(emit,
			TrajectoryEvent{Stage: "hop", Step: hop, Query: followUp, Reason: "judge_followup"},
			map[string]any{"agenticHop": hop, "agenticQuery": followUp},
		)
		opts := vector.SearchOptions{
			FileIDs:       params.FileIDs,
			HyDE:          false, // focused gap-filler doesn't need HyDE
			MultiQuery:    false,
			ModelOverride: params.PlanningModel,
			QueryType:     vector.QueryTypeComplexReasoning,
		}
		nextResult, serr := searcher.Search(ctx, params.KbID, followUp, 0, opts)
		if serr != nil {
			outcome = "search_error"
			logctx.From(ctx).Warn("agentic_chat: hop search failed; using accumulated chunks",
				"hop", hop, "query", followUp, "error", serr)
			break
		}

		before := len(accumulated)
		accumulated = deduplicateChunks(accumulated, nextResult.Chunks)
		added := len(accumulated) - before
		emitTrajectory(emit,
			TrajectoryEvent{
				Stage:    "hop",
				Step:     hop,
				Query:    followUp,
				Findings: added,
				Chunks:   chunkRefs(nextResult.Chunks),
			},
			map[string]any{"agenticHopFindings": added, "agenticHop": hop},
		)

		if added == 0 {
			outcome = "early_stop_no_progress"
			break
		}

		// Plateau check (Phase 1 §1.3). Runs after the zero-progress
		// branch so `early_stop_no_progress` keeps its existing
		// semantics; the plateau case is strictly the looser superset.
		if params.Plateau.Enabled {
			currentTopScore := topChunkScore(accumulated)
			scoreDelta := currentTopScore - prevTopScore
			if added <= params.Plateau.Chunks && scoreDelta < params.Plateau.ScoreDelta {
				plateauStreak++
				logctx.From(ctx).Info("rag.agentic_chat.plateau",
					"hop", hop,
					"added", added,
					"chunks_threshold", params.Plateau.Chunks,
					"score_delta", scoreDelta,
					"score_delta_threshold", params.Plateau.ScoreDelta,
					"streak", plateauStreak,
				)
				if plateauStreak >= PlateauStreakLimit {
					outcome = "early_stop_plateau"
					break
				}
			} else {
				plateauStreak = 0
			}
			prevTopScore = currentTopScore
		}
	}

	observability.RecordAgenticChatDecision(outcome, finalHopCount)
	emitTrajectory(emit,
		TrajectoryEvent{Stage: "answer", Step: finalHopCount, Decision: outcome},
		map[string]any{"agenticDecision": outcome, "agenticTotalHops": finalHopCount},
	)
	logctx.From(ctx).Info("rag.agentic_chat.completed",
		"outcome", outcome,
		"hops_attempted", finalHopCount,
		"chunks_accumulated", len(accumulated),
	)

	// Build ChatContext from accumulated — same tail as RunDeepChat:
	// truncate to token budget, sandwich-order, build context string +
	// sources slice.
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
		EnhancedQuery: result1.EnhancedQuery,
		SystemPrompt:  sb.String(),
		Sources:       sources,
		Context:       contextText,
		FinalChunks:   accumulated,
	}, nil
}

// summarizeChunksForCritique builds a compact rendering of the
// accumulated chunks for the NeedsMoreInfo critique prompt. Hard-caps
// the total length to budget characters so the critique call stays
// cheap regardless of how many chunks the agent has gathered.
func summarizeChunksForCritique(chunks []vector.SearchChunk, budget int) string {
	var sb strings.Builder
	for i, c := range chunks {
		entry := fmt.Sprintf("[%d] [%s] %s\n", i+1, c.FileName, c.Content)
		if sb.Len()+len(entry) > budget {
			// Stop adding chunks rather than truncate mid-content; the
			// critique LLM works better with whole chunks than with
			// partial ones.
			break
		}
		sb.WriteString(entry)
	}
	return sb.String()
}
