package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/aibudget"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
)

// ToolDispatcher is the chat-side surface of mcp.Registry. The chat
// package keeps this thin interface to avoid importing mcp directly
// (which would couple the chat-test fixtures to the JSON-RPC client).
// In production it is satisfied by *mcp.Registry; tests inject a stub.
type ToolDispatcher interface {
	Dispatch(ctx context.Context, kbID, name string, args json.RawMessage) (DispatchedToolResult, error)
}

// DispatchedToolResult is the chat-package-local view of mcp.ToolResult.
// It carries only the fields the orchestrators consume — full provenance
// stays in the mcp package so changes there don't ripple through chat.
//
// Structured (AP-B3): JSON payload from tools that return structured
// data (sql_query, document_outline). The plan-execute tool runner
// wraps it as a synthetic chunk's Content when no chunk-shaped output
// is present, so downstream chunk-consumers don't need a special case.
type DispatchedToolResult struct {
	Text       string
	Chunks     []vector.SearchChunk
	Meta       map[string]any
	Structured json.RawMessage
}

// PlanExecuteParams configures a Plan-and-Execute run. Mirrors
// AgenticChatParams plus the four Plan-Execute-specific knobs.
type PlanExecuteParams struct {
	KbID           string
	Query          string
	Language       string
	FileIDs        []string
	KbSystemPrompt string
	PlanningModel  string
	MaxSubQueries  int
	MaxIterations  int
	TokenBudget    int
	// Plateau is the resolved Phase 1 §1.3 quality-plateau early-stop
	// configuration. When Plateau.Enabled is true the iterate loop tracks
	// per-round `topScore` + `chunksAdded` deltas. Two consecutive plateau
	// rounds set the outcome to `early_stop_plateau` and break out
	// gracefully — strictly looser than the existing `noProgressStreak`
	// gate (which fires only on `added == 0`).
	Plateau PlateauConfig
	// Tools is the optional MCP tool dispatcher (Phase 2 §2.1). When
	// non-nil the iterate loop's "search" action routes through
	// Tools.Dispatch(kb_search) instead of calling searcher.Search
	// directly. Nil disables the MCP path entirely so existing tests
	// (and deployments with chat_use_mcp_tools off) keep their behavior.
	Tools ToolDispatcher
	// DAG enables the Phase 3 §3.1 dependency-graph plan stage. When
	// true, the Plan call returns ai.Plan{Nodes,Rationale} and the
	// executor walks the graph in topological levels (parallel within
	// each level) with deterministic parent-excerpt interpolation
	// into dependent-node queries. Cycle / depth-cap / width-cap
	// failures fall back to the flat-list path and increment
	// `rag_plan_dag_invalid_total` so the regression is observable.
	DAG         bool
	MaxDAGDepth int // [1,5], default 3; circuit breaker on chain length.
	MaxDAGNodes int // [1,12], default 6; hard cap on planner output.
	// ToolAware enables the AP-B3 tool-aware DAG planner. When true AND
	// DAG is true AND ToolCatalog is non-empty, the planner LLM call
	// includes the tool catalog in its system prompt and the executor
	// dispatches each node's chosen tool via DAGToolRunner. When false
	// (or any prerequisite is missing), the legacy DAG planner runs and
	// every node goes to kb_search via runNodeFn — full backward compat.
	ToolAware   bool
	ToolCatalog []prompts.PlanToolEntry
	ToolRunner  DAGToolRunner
	// DAGCritic is the AP-D3 inter-level critic. Wired by the
	// chat handler when chat_plan_execute_dag_iterative is on.
	// When non-nil AND DAG is on, the executor calls it after
	// each completed level to decide continue / add / prune /
	// answer_now.
	DAGCritic DAGCritic
	// GraphChunkIDs forwards the AP-C4 graph router's resolved
	// subgraph chunk IDs (chat.ResolveGraphChunksIfEnabled) into the
	// initial plan-search SearchOptions. Iterate-loop sub-query
	// searches and DAG node searches do NOT carry the injection
	// because each iterate query is a focused gap-filler and folding
	// the same graph chunks into every sub-search would dilute the
	// per-query relevance signal.
	GraphChunkIDs []string
	// HyPESearch enables the HyPE query-time arm on this orchestrator's
	// initial search (resolved from hype_search_enabled at dispatch).
	HyPESearch bool
}

// planFn / iterateFn are injectable function shapes for the LLM calls.
// Production wires them to ai.PlanQueries and ai.DecideNextAction; tests
// inject deterministic stubs.
type planFn func(ctx context.Context, resolver *ai.ConfigResolver, kbID, question, language, modelOverride string) ([]string, string, error)

type iterateFn func(ctx context.Context, resolver *ai.ConfigResolver, kbID, question, chunkSummaries string, round int, language, modelOverride string) (action string, queries []string, reason string, err error)

// planDAGFn is the injectable LLM-call shape for Phase 3 §3.1 DAG-shaped
// planning. Production wires it to ai.PlanQueriesDAG; tests inject
// deterministic stubs.
type planDAGFn func(ctx context.Context, resolver *ai.ConfigResolver, kbID, question, language, modelOverride string) (ai.Plan, error)

// RunPlanExecuteChat is the production entry point. Mirrors RunAgenticChat's
// signature so http_send.go can swap one for the other based on the
// chat_plan_execute_enabled gate.
func RunPlanExecuteChat(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	params PlanExecuteParams,
	emit func(data map[string]any),
) (*ChatContext, error) {
	return runPlanExecuteChatTestable(ctx, aiResolver, searchSvc, ai.PlanQueries, ai.PlanQueriesDAG, ai.DecideNextAction, params, emit)
}

// runPlanExecuteChatTestable accepts the searcher + plan + iterate funcs
// as injectable arguments so unit tests can drive the loop without a
// real SearchService or LLM.
func runPlanExecuteChatTestable(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searcher searchInvoker,
	plan planFn,
	planDAG planDAGFn,
	iterate iterateFn,
	params PlanExecuteParams,
	emit func(data map[string]any),
) (*ChatContext, error) {
	const maxTokens = 120_000
	const chunkSummaryBudget = 3000

	if params.MaxSubQueries < 1 || params.MaxSubQueries > 5 {
		params.MaxSubQueries = 3
	}
	if params.MaxIterations < 1 || params.MaxIterations > 5 {
		params.MaxIterations = 3
	}
	if params.TokenBudget <= 0 {
		params.TokenBudget = 8000
	}

	ctx = aibudget.New(ctx, params.TokenBudget)

	emitTrajectory(emit,
		TrajectoryEvent{Stage: "plan", Reason: "starting"},
		map[string]any{"planExecuteStage": "plan", "planExecuteQueries": []string{}, "planExecuteReason": "starting"},
	)

	// ---------- Stage 1: PLAN ----------
	// Phase 3 §3.1: try the DAG planner first when params.DAG is on AND
	// a DAG planner func was supplied. On any DAG-side failure (LLM
	// error, cycle, depth/width-cap blown) fall through to the legacy
	// flat-list planner so the user still gets an answer; the
	// rag_plan_dag_invalid_total counter records why.
	planStart := time.Now()
	var (
		subQueries     []string
		planRationale  string
		planErr        error
		dagAccumulated []vector.SearchChunk // chunks already gathered by the DAG executor
		dagUsed        bool
	)

	if params.DAG && planDAG != nil {
		// AP-B3: when tool-aware mode is on and a catalog is wired,
		// switch to PlanQueriesDAGToolAware. This is a separate LLM
		// call shape (catalog goes into the system prompt) so we
		// can't just substitute the planDAG closure — we call the
		// tool-aware variant directly. Falls through to the legacy
		// planner when ToolAware is off, the catalog is empty, or
		// the variant returns an error.
		var (
			dagPlan ai.Plan
			derr    error
		)
		if params.ToolAware && len(params.ToolCatalog) > 0 {
			dagPlan, derr = ai.PlanQueriesDAGToolAware(ctx, aiResolver, params.KbID, params.Query, params.Language, params.PlanningModel, params.ToolCatalog)
			if derr != nil {
				logctx.From(ctx).Warn("plan_execute: tool-aware planner failed; falling back to legacy DAG planner",
					"error", derr)
				dagPlan, derr = planDAG(ctx, aiResolver, params.KbID, params.Query, params.Language, params.PlanningModel)
			}
		} else {
			dagPlan, derr = planDAG(ctx, aiResolver, params.KbID, params.Query, params.Language, params.PlanningModel)
		}
		if derr != nil {
			logctx.From(ctx).Warn("plan_execute: DAG plan stage failed; falling back to flat planner", "error", derr)
		} else if len(dagPlan.Nodes) == 0 {
			logctx.From(ctx).Info("plan_execute: DAG plan stage returned no nodes; falling back to flat planner")
		} else {
			runNode := func(rctx context.Context, query string, fileIDs []string, planningModel string) ([]vector.SearchChunk, error) {
				opts := vector.SearchOptions{
					FileIDs:       fileIDs,
					ModelOverride: planningModel,
					QueryType:     vector.QueryTypeComplexReasoning,
				}
				res, serr := searcher.Search(rctx, params.KbID, query, 0, opts)
				if serr != nil {
					return nil, serr
				}
				return res.Chunks, nil
			}
			dagRes, eerr := ExecuteDAG(ctx, DAGExecuteParams{
				Plan:          dagPlan,
				KbID:          params.KbID,
				FileIDs:       params.FileIDs,
				PlanningModel: params.PlanningModel,
				MaxNodes:      params.MaxDAGNodes,
				MaxDepth:      params.MaxDAGDepth,
				Question:      params.Query,
				// AP-B3: ToolRunner is consulted only for nodes
				// whose Tool field is non-empty AND not "kb_search".
				// Legacy plans (Tool="") still go through runNode.
				ToolRunner: params.ToolRunner,
				// AP-D3: Critic is consulted between levels when
				// chat_plan_execute_dag_iterative is on. Nil when
				// the gate is off — the executor short-circuits
				// the critic branch entirely.
				Critic: params.DAGCritic,
			}, runNode)
			switch {
			case errors.Is(eerr, ErrPlanCycle):
				observability.RecordPlanDAGInvalid("cycle")
				logctx.From(ctx).Warn("plan_execute: DAG plan has a cycle; falling back to flat planner")
			case errors.Is(eerr, ErrPlanTooDeep):
				observability.RecordPlanDAGInvalid("too_deep")
				logctx.From(ctx).Warn("plan_execute: DAG plan exceeds depth cap; falling back to flat planner")
			case errors.Is(eerr, ErrPlanTooWide):
				observability.RecordPlanDAGInvalid("too_wide")
				logctx.From(ctx).Warn("plan_execute: DAG plan exceeds node cap; falling back to flat planner")
			case eerr != nil:
				logctx.From(ctx).Warn("plan_execute: DAG executor errored; using whatever chunks were gathered", "error", eerr)
				// Partial results are surfaced regardless: the executor
				// returned per-node chunks for nodes that did finish.
				dagUsed = true
				for _, n := range dagRes.Nodes {
					dagAccumulated = deduplicateChunks(dagAccumulated, n.Chunks)
				}
			default:
				dagUsed = true
				for _, n := range dagRes.Nodes {
					dagAccumulated = deduplicateChunks(dagAccumulated, n.Chunks)
				}
				observability.ObservePlanDAGShape(dagRes.MaxDepth, dagRes.ParallelNodes)
				// Surface the DAG shape on the trajectory `plan` event so
				// the frontend can render an ASCII tree without an extra
				// round-trip.
				dagQueries := make([]string, 0, len(dagRes.Nodes))
				for _, n := range dagRes.Nodes {
					dagQueries = append(dagQueries, n.Query)
				}
				planRationale = dagPlan.Rationale
				subQueries = dagQueries
			}
		}
	}

	if !dagUsed {
		subQueries, planRationale, planErr = plan(ctx, aiResolver, params.KbID, params.Query, params.Language, params.PlanningModel)
	}
	observability.ObservePlanExecuteLLMSeconds("plan", time.Since(planStart).Seconds())

	subqueryCounts := []int{len(subQueries)}

	if len(subQueries) > params.MaxSubQueries {
		subQueries = subQueries[:params.MaxSubQueries]
		subqueryCounts[0] = params.MaxSubQueries
	}

	emitTrajectory(emit,
		TrajectoryEvent{Stage: "plan", Queries: subQueries, Reason: planRationale},
		map[string]any{
			"planExecuteStage":   "plan",
			"planExecuteQueries": subQueries,
			"planExecuteReason":  planRationale,
		},
	)

	planFailed := false
	planSearchOpts := vector.SearchOptions{
		FileIDs:       params.FileIDs,
		ModelOverride: params.PlanningModel,
		QueryType:     vector.QueryTypeComplexReasoning,
		GraphChunkIDs: params.GraphChunkIDs,
		HyPESearch:    params.HyPESearch,
	}
	if !dagUsed && (planErr != nil || len(subQueries) == 0) {
		planFailed = true
		if planErr != nil {
			logctx.From(ctx).Warn("plan_execute: plan stage failed; falling back to single-query baseline",
				"error", planErr)
		} else {
			logctx.From(ctx).Info("plan_execute: plan stage returned empty queries; falling back to single-query baseline",
				"rationale", planRationale)
		}
	} else if !dagUsed {
		planSearchOpts.SubQueries = subQueries
	}

	logctx.From(ctx).Info("rag.plan_execute.plan",
		"duration_ms", time.Since(planStart).Milliseconds(),
		"queries_count", len(subQueries),
		"queries", subQueries,
		"rationale", planExecuteTruncate(planRationale, 300),
		"dag_used", dagUsed,
		"error", planErr,
	)

	var accumulated []vector.SearchChunk
	enhancedQuery := ""
	if dagUsed {
		// Skip the redundant single-query baseline search — the DAG
		// executor already gathered chunks across every node.
		accumulated = dagAccumulated
	} else {
		planResult, err := searcher.Search(ctx, params.KbID, params.Query, 0, planSearchOpts)
		if err != nil {
			return nil, fmt.Errorf("plan_execute: plan search: %w", err)
		}
		accumulated = planResult.Chunks
		enhancedQuery = planResult.EnhancedQuery
	}
	if len(accumulated) == 0 {
		return nil, fmt.Errorf("plan_execute: no results in plan stage")
	}

	// ---------- Stage 2: ITERATE ----------
	totalRounds := 0
	noProgressStreak := 0
	lastAction := ""
	iterErrored := false
	noProgressBreak := false
	plateauBreak := false
	budgetExhausted := false
	// Plateau bookkeeping (Phase 1 §1.3). Seeded from the Plan-stage top
	// score so the first iterate round's score is compared against a real
	// baseline rather than 0.
	prevTopScore := topChunkScore(accumulated)
	plateauStreak := 0

	budget := TurnBudgetFromContext(ctx)
	for round := 1; round <= params.MaxIterations; round++ {
		// AP-A3: unified budget check (time / tokens / tool calls).
		// CheckExceeded delegates to aibudget for the tokens branch,
		// so the existing plan_execute_token_budget knob still wires
		// through the same `budget_exceeded` outcome — no separate
		// path remains.
		if kind := budget.CheckExceeded(ctx, "plan_execute"); kind != "" {
			budgetExhausted = true
			emitTrajectory(emit,
				TrajectoryEvent{Stage: "decision", Step: round, Decision: "budget_exceeded", Reason: kind},
				nil,
			)
			break
		}

		summaries := summarizeChunksForCritique(accumulated, chunkSummaryBudget)
		iterStart := time.Now()
		action, queries, reason, ierr := iterate(ctx, aiResolver, params.KbID, params.Query, summaries, round, params.Language, params.PlanningModel)
		observability.ObservePlanExecuteLLMSeconds("iterate", time.Since(iterStart).Seconds())
		subqueryCounts = append(subqueryCounts, len(queries))

		if ierr != nil {
			iterErrored = true
			logctx.From(ctx).Warn("plan_execute: iterate call failed; bailing with accumulated chunks",
				"round", round, "error", ierr)
			break
		}

		logctx.From(ctx).Info("rag.plan_execute.iteration",
			"round", round,
			"action", action,
			"queries_count", len(queries),
			"queries", queries,
			"reason", planExecuteTruncate(reason, 200),
			"chunks_total_before", len(accumulated),
			"duration_ms", time.Since(iterStart).Milliseconds(),
		)

		emitTrajectory(emit,
			TrajectoryEvent{
				Stage:   "iterate",
				Step:    round,
				Queries: queries,
				Reason:  reason,
			},
			map[string]any{
				"planExecuteStage":   "iterate",
				"planExecuteRound":   round,
				"planExecuteQueries": queries,
				"planExecuteReason":  reason,
			},
		)

		if action == "answer" {
			lastAction = "answer"
			break
		}

		// action == "search"; queries guaranteed non-empty by DecideNextAction
		// normalization (empty queries → answer).
		primaryQuery := queries[0]

		var nextChunks []vector.SearchChunk
		var serr error
		if params.Tools != nil {
			// MCP path (Phase 2 §2.1). The legacy "search" action maps to
			// kb_search with the primary query; sub-queries are not yet
			// surfaced through the tool args (the plan-execute orchestrator
			// only ever issued one tool call per round even pre-MCP).
			argsJSON, _ := json.Marshal(map[string]any{
				"query":    primaryQuery,
				"file_ids": params.FileIDs,
				"kb_id":    params.KbID,
			})
			res, terr := params.Tools.Dispatch(ctx, params.KbID, "kb_search", argsJSON)
			if terr != nil {
				serr = terr
			} else {
				nextChunks = res.Chunks
				emitTrajectory(emit,
					TrajectoryEvent{Stage: "decision", Decision: "agent_tool_call", Reason: "kb_search", Findings: len(res.Chunks)},
					map[string]any{"agentToolCall": map[string]any{"tool": "kb_search", "result_chunks": len(res.Chunks)}},
				)
			}
		} else {
			searchOpts := vector.SearchOptions{
				FileIDs:       params.FileIDs,
				ModelOverride: params.PlanningModel,
				QueryType:     vector.QueryTypeComplexReasoning,
			}
			if len(queries) > 1 {
				searchOpts.SubQueries = queries[1:]
			}
			nextResult, perr := searcher.Search(ctx, params.KbID, primaryQuery, 0, searchOpts)
			if perr != nil {
				serr = perr
			} else {
				nextChunks = nextResult.Chunks
			}
		}
		if serr != nil {
			iterErrored = true
			logctx.From(ctx).Warn("plan_execute: iterate search failed; bailing with accumulated chunks",
				"round", round, "error", serr)
			break
		}

		before := len(accumulated)
		accumulated = deduplicateChunks(accumulated, nextChunks)
		added := len(accumulated) - before

		emitTrajectory(emit,
			TrajectoryEvent{
				Stage:    "iterate",
				Step:     round,
				Query:    primaryQuery,
				Findings: added,
				Chunks:   chunkRefs(nextChunks),
			},
			map[string]any{
				"planExecuteStage":    "iterate",
				"planExecuteRound":    round,
				"planExecuteFindings": added,
			},
		)

		totalRounds = round

		if added == 0 {
			noProgressStreak++
			if noProgressStreak >= 2 {
				noProgressBreak = true
				break
			}
			continue
		}
		noProgressStreak = 0

		// Plateau check (Phase 1 §1.3). Runs after the no-progress branch
		// so the existing semantics (added==0 → noProgressBreak) are
		// preserved; the plateau case strictly extends them with a
		// score-gradient signal.
		if params.Plateau.Enabled {
			currentTopScore := topChunkScore(accumulated)
			scoreDelta := currentTopScore - prevTopScore
			if added <= params.Plateau.Chunks && scoreDelta < params.Plateau.ScoreDelta {
				plateauStreak++
				logctx.From(ctx).Info("rag.plan_execute.plateau",
					"round", round,
					"added", added,
					"chunks_threshold", params.Plateau.Chunks,
					"score_delta", scoreDelta,
					"score_delta_threshold", params.Plateau.ScoreDelta,
					"streak", plateauStreak,
				)
				if plateauStreak >= PlateauStreakLimit {
					plateauBreak = true
					break
				}
			} else {
				plateauStreak = 0
			}
			prevTopScore = currentTopScore
		}
	}

	// Outcome resolution. Order matters: more-specific wins.
	outcome := ""
	switch {
	case planFailed:
		outcome = "plan_error"
	case budgetExhausted:
		outcome = "budget_exhausted"
	case iterErrored:
		outcome = "iter_error"
	case plateauBreak:
		outcome = "early_stop_plateau"
	case noProgressBreak:
		outcome = "no_progress"
	case lastAction == "answer" && totalRounds == 0:
		outcome = "answered_after_plan"
	case lastAction == "answer":
		outcome = "answered_after_iter"
	case totalRounds == params.MaxIterations:
		outcome = "max_iter_reached"
	default:
		// Loop didn't enter (MaxIterations < 1) or some unexpected state.
		outcome = "answered_after_plan"
	}

	observability.RecordPlanExecuteDecision(outcome, totalRounds, subqueryCounts)

	emitTrajectory(emit,
		TrajectoryEvent{Stage: "answer", Step: totalRounds, Decision: outcome},
		map[string]any{
			"planExecuteStage":       "answer",
			"planExecuteOutcome":     outcome,
			"planExecuteTotalRounds": totalRounds,
		},
	)
	logctx.From(ctx).Info("rag.plan_execute.completed",
		"outcome", outcome,
		"total_rounds", totalRounds,
		"chunks_accumulated", len(accumulated),
		"total_llm_tokens", aibudget.Used(ctx),
	)

	// ---------- Stage 3: GENERATE (same tail as RunAgenticChat) ----------
	accumulated = TruncateChunksToFit(accumulated, maxTokens)
	accumulated = SandwichOrder(accumulated)

	var ctxParts []string
	sources := make([]ChatSource, len(accumulated))
	for i, c := range accumulated {
		idx := i + 1
		pages := pagesFromMetadata(c.Metadata)
		pageAnnotation := ""
		if len(pages) > 0 {
			if len(pages) == 1 {
				pageAnnotation = fmt.Sprintf(", p. %d", pages[0])
			} else {
				pageAnnotation = fmt.Sprintf(", p. %d-%d", pages[0], pages[len(pages)-1])
			}
		}
		annotation := renderSourceHeader(idx, c.FileName, pageAnnotation, c.NodeKind, c.TreeLevel)
		ctxParts = append(ctxParts, annotation+"\n"+c.Content)
		sources[i] = ChatSource{
			Index:     idx,
			FileName:  c.FileName,
			FileID:    c.FileID,
			Content:   c.Content,
			Score:     c.Score,
			Pages:     pages,
			ChunkID:   c.ID,
			NodeKind:  c.NodeKind,
			TreeLevel: c.TreeLevel,
		}
	}
	contextText := strings.Join(ctxParts, "\n\n---\n\n")

	var sb strings.Builder
	if params.KbSystemPrompt != "" {
		sb.WriteString(params.KbSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompts.ChatSystemPrompt(params.Language))
	if IsLowConfidence(accumulated) {
		sb.WriteString(prompts.ChatLowConfidenceNotice(params.Language))
	}
	sb.WriteString("\n\nCONTEXT:\n")
	sb.WriteString(contextText)

	return &ChatContext{
		EnhancedQuery: enhancedQuery,
		SystemPrompt:  sb.String(),
		Sources:       sources,
		Context:       contextText,
	}, nil
}

// planExecuteTruncate truncates s to n bytes, appending "…" if needed.
// Named to avoid collision with deep_chat.go's truncateDisplay helper.
func planExecuteTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
