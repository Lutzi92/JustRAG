package eval

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/chatpolicy"
	"github.com/justrag/go-backend/internal/vector"
)

// PlanShapeInputs carries plan attributes known at dispatch time (DAG /
// Iterative / ToolAware are dispatcher params, not events). MaxDepth is
// the dispatcher-configured cap, only meaningful when DAG is true.
type PlanShapeInputs struct {
	DAG       bool
	MaxDepth  int
	Iterative bool
	ToolAware bool
}

// ClassifyQueryTypeForEval mirrors the inline query-type classifier in
// internal/chat/http_send.go (around line 256-308) — runs the syntactic
// heuristic, defers to ai.ClassifyQueryComplexity on non-simple verdicts,
// then resolves to the production three-way label. Used by
// OrchestratorDispatchAdapter so eval routes the same way production
// would.
//
// resolver may be nil; if non-simple but resolver is nil, the function
// behaves as if the LLM classifier returned false (downgrade to lookup /
// enumeration).
func ClassifyQueryTypeForEval(ctx context.Context, resolver *ai.ConfigResolver, query, kbID, lang string) string {
	complexFromLLM := false
	if hv := ai.HeuristicComplexity(query); hv != ai.ComplexitySimple {
		if resolver != nil {
			if isComplex, err := ai.ClassifyQueryComplexity(ctx, resolver, query, kbID, lang); err == nil && isComplex {
				complexFromLLM = true
			}
		}
	}
	isEnum := chat.IsEnumerationQuery(query, lang)
	switch {
	case complexFromLLM:
		return vector.QueryTypeComplexReasoning
	case isEnum:
		return vector.QueryTypeEnumeration
	default:
		return vector.QueryTypeLookup
	}
}

// SelectOrchestrator replicates the dispatch predicate from tryDeepChat
// (internal/chat/http_send.go). Returns the orchestrator label, a short
// human-readable dispatch reason, and the chat_orchestrator_policy decision
// that produced it. Eval has no Enhance parameter so the `body.Enhance == ""`
// clause is implicitly true.
//
// query is the question text: the long-context gate keys on the
// global-synthesis keyword classifier, not on the query type alone.
//
// W6-R6: the policy table is evaluated FIRST here — before even the
// query-type gate — because production evaluates it above the whole flag
// ladder, and the ladder's complex_reasoning precondition is part of that
// ladder. A "force lookup → supervisor" rule that the mirror refused because
// the question is a lookup would measure a route production does not take.
// Production's three always-first arms (comparison / team / corpus-table)
// have no mirror at all (they are explicit-intent routes, not complex-lane
// dispatch), so there is nothing here for the policy to jump ahead of.
//
// The two ladders are pinned to agree on these cases (Wave-6 fix round 1;
// production's half lives in internal/chat, this half in
// TestSelectOrchestrator_Policy* below):
//
//   - force supervisor on a LOOKUP turn        → supervisor on both sides.
//     Production reaches it because SendMessage's entry gate now widens for a
//     forced non-standard route (chat.shouldTryDeepChat); before that fix the
//     mirror said supervisor and production said standard.
//   - prefer supervisor (flag ON) on a LOOKUP turn → supervisor on both sides,
//     same mechanism.
//   - empty policy on a COMPLEX turn           → the flag ladder on both
//     sides, byte-identically (W6-R10).
//
// Two differences remain and are deliberate, documented for the acceptance
// record rather than hidden: a forced "standard" rule runs RunDeepChat in
// production (inside tryDeepChat) but PrepareChatContext here, and the
// mirror's signal bag cannot carry HasFileSelection / HistoryTurns (see
// PolicySignalsForQuestion).
func SelectOrchestrator(ctx context.Context, siteCfg chat.SiteConfigReader, queryType, query string, sig chatpolicy.Signals) (string, string, chatpolicy.Decision) {
	pol := chat.ChatOrchestratorPolicy(ctx, siteCfg)
	dec := chatpolicy.Decide(pol, sig, policyEnabledMap(ctx, siteCfg))
	if dec.Applied {
		name := orchestratorForPolicyName(dec.Orchestrator)
		// Q1 / DAG parity with production: http_send.go's OrchPlanExecute case
		// passes `DAG: ChatPlanExecuteDAG(...) || policyDec.ForceDAG`, so on a
		// deployment with chat_plan_execute_dag on, a forced "plan_execute"
		// rule runs the DAG planner. The mirror has no separate DAG flag — the
		// orchestrator LABEL is what its dispatch switch reads — so the OR is
		// reproduced by promoting the label here. Without this, a forced
		// plan_execute rule would measure the flat planner under eval and the
		// DAG planner in production.
		if name == OrchestratorPlanExecute && chat.ChatPlanExecuteDAG(ctx, siteCfg) {
			name = OrchestratorPlanExecuteDAG
		}
		return name,
			fmt.Sprintf("policy_rule_%d_%s", dec.RuleIndex, dec.Mode),
			dec
	}

	if queryType != vector.QueryTypeComplexReasoning {
		return OrchestratorStandard, "fallback_query_type_" + queryType, dec
	}
	// W4-R3: mirrors the chat ladder's OrchDrift arm, at the same position
	// as production (directly above long-context — DRIFT needs KG
	// community summaries and is the more specific global-synthesis
	// answer). Comparison/Team/CorpusTable stay un-mirrored: none of them
	// are complex-lane dispatch, so they have no place on this ladder.
	if chat.ChatDriftEnabled(ctx, siteCfg) && chat.IsGlobalSynthesisQuery(query) {
		return OrchestratorDrift, "complex_reasoning_drift_gate", dec
	}
	// W3-R5: mirrors the chat ladder's OrchLongContext arm.
	if chat.ChatLongContextEnabled(ctx, siteCfg) && chat.IsGlobalSynthesisQuery(query) {
		return OrchestratorLongContext, "complex_reasoning_longcontext_gate", dec
	}
	if chat.ChatSupervisorEnabled(ctx, siteCfg) {
		return OrchestratorSupervisor, "complex_reasoning_supervisor_gate", dec
	}
	if chat.ChatPlanExecuteEnabled(ctx, siteCfg) {
		if chat.ChatPlanExecuteDAG(ctx, siteCfg) {
			return OrchestratorPlanExecuteDAG, "complex_reasoning_plan_execute_dag_gate", dec
		}
		return OrchestratorPlanExecute, "complex_reasoning_plan_execute_gate", dec
	}
	if chat.ChatAgenticEnabled(ctx, siteCfg) {
		return OrchestratorAgentic, "complex_reasoning_agentic_gate", dec
	}
	return OrchestratorStandard, "fallback_no_orchestrator_enabled", dec
}

// orchestratorForPolicyName maps a chatpolicy orchestrator name onto this
// package's orchestrator label. The six flag-ladder names are byte-identical
// to the eval constants (pinned in chat by
// TestChatPolicyOrchestratorNamesMatchChatConstants), and "plan_execute_dag"
// maps 1:1 onto OrchestratorPlanExecuteDAG — which is what makes the dispatch
// switch below turn the DAG on, mirroring production's ForceDAG. An unknown
// name cannot reach here (the parser rejects it), so the fallback is the
// standard route rather than a panic.
func orchestratorForPolicyName(name string) string {
	switch name {
	case "drift":
		return OrchestratorDrift
	case "longcontext":
		return OrchestratorLongContext
	case "supervisor":
		return OrchestratorSupervisor
	case "plan_execute":
		return OrchestratorPlanExecute
	case "plan_execute_dag":
		return OrchestratorPlanExecuteDAG
	case "agentic":
		return OrchestratorAgentic
	default:
		return OrchestratorStandard
	}
}

// policyEnabledMap is the enabled map chatpolicy.Decide takes, read from
// site_config. Shared by the dispatch mirror above and by RunTrajectory's
// informational policy_rule, so the two cannot drift.
//
// It mirrors chat.OrchestratorInputs.policyEnabled: "plan_execute_dag" carries
// plan-execute's flag (the DAG is a shape of that orchestrator, not a separate
// one) and "standard" is deliberately absent (chatpolicy treats it as always
// enabled — it has no flag).
func policyEnabledMap(ctx context.Context, siteCfg chat.SiteConfigReader) map[string]bool {
	planExecute := chat.ChatPlanExecuteEnabled(ctx, siteCfg)
	return map[string]bool{
		"drift":            chat.ChatDriftEnabled(ctx, siteCfg),
		"longcontext":      chat.ChatLongContextEnabled(ctx, siteCfg),
		"supervisor":       chat.ChatSupervisorEnabled(ctx, siteCfg),
		"plan_execute":     planExecute,
		"plan_execute_dag": planExecute,
		"agentic":          chat.ChatAgenticEnabled(ctx, siteCfg),
	}
}

// BuildAgentTrace stitches together a populated AgentTrace from the
// orchestrator label, captured events, and the plan-shape inputs the
// dispatcher passes through. Plan is only populated for plan_execute*.
func BuildAgentTrace(orchestrator, dispatchReason string, events []chat.TrajectoryEvent, plan PlanShapeInputs) *AgentTrace {
	t := &AgentTrace{
		Orchestrator:   orchestrator,
		DispatchReason: dispatchReason,
		Tools:          ExtractToolCounts(events),
	}
	switch orchestrator {
	case OrchestratorSupervisor:
		t.Specialist = ExtractSpecialist(events)
	case OrchestratorAgentic:
		t.Hops = ExtractHops(events)
	case OrchestratorPlanExecute, OrchestratorPlanExecuteDAG:
		t.Plan = &PlanShape{
			NodeCount: ExtractPlanNodeCount(events),
			MaxDepth:  plan.MaxDepth,
			DAG:       plan.DAG,
			Iterative: plan.Iterative,
			ToolAware: plan.ToolAware,
		}
	}
	return t
}

// OrchestratorDispatchAdapter routes each eval question through the same
// orchestrator predicate production uses, captures the trajectory, and
// returns chunks shaped for retrieval metrics. Embeds a
// ProductionContextAdapter for the standard-fallback branch and judge-mode
// caching.
type OrchestratorDispatchAdapter struct {
	prod           *ProductionContextAdapter
	aiResolver     *ai.ConfigResolver
	searchService  vector.Searcher
	siteCfg        chat.SiteConfigReader
	kbSystemPrompt func(ctx context.Context, kbID string) string

	traceCache map[string]*AgentTrace
	chunkCache map[string][]vector.SearchChunk
	// ctxCache holds the *chat.ChatContext an orchestrator branch produced, so
	// judge mode grades the prompt the orchestrator actually built instead of
	// falling through to the standard adapter's (which never ran).
	ctxCache map[string]*chat.ChatContext

	planExecuteMaxSubQueries int
	planExecuteMaxIterations int
	planExecuteTokenBudget   int
	planExecuteMaxDAGDepth   int
	planExecuteMaxDAGNodes   int
	planExecuteDAGIterative  bool
	agenticMaxHops           int
	planningModel            string
}

// NewOrchestratorDispatchAdapter constructs the adapter. kbSystemPrompt
// may be nil; when nil, orchestrators run without a KB system prompt
// (the LLM context drifts slightly from production where the prompt is
// always present).
func NewOrchestratorDispatchAdapter(
	aiResolver *ai.ConfigResolver,
	searchService vector.Searcher,
	siteCfg chat.SiteConfigReader,
	kbSystemPrompt func(ctx context.Context, kbID string) string,
	flags EvalFlags,
	prodOpts ...ProductionAdapterOption,
) *OrchestratorDispatchAdapter {
	// prodOpts reach the embedded production adapter, which serves this
	// adapter's standard-fallback route — that is where the tabular
	// router has to land for a default --production-context run.
	prod := NewProductionContextAdapter(aiResolver, searchService, siteCfg, flags, prodOpts...)
	bg := context.Background()
	planningModel := chat.ChatPlanExecuteModel(bg, siteCfg)
	if planningModel == "" {
		planningModel = chat.EnrichmentModel(bg, siteCfg)
	}
	return &OrchestratorDispatchAdapter{
		prod:                     prod,
		aiResolver:               aiResolver,
		searchService:            searchService,
		siteCfg:                  siteCfg,
		kbSystemPrompt:           kbSystemPrompt,
		traceCache:               map[string]*AgentTrace{},
		chunkCache:               map[string][]vector.SearchChunk{},
		ctxCache:                 map[string]*chat.ChatContext{},
		planExecuteMaxSubQueries: chat.ChatPlanExecuteMaxSubQueries(bg, siteCfg),
		planExecuteMaxIterations: chat.ChatPlanExecuteMaxIterations(bg, siteCfg),
		planExecuteTokenBudget:   chat.ChatPlanExecuteTokenBudget(bg, siteCfg),
		planExecuteMaxDAGDepth:   chat.ChatPlanExecuteMaxDAGDepth(bg, siteCfg),
		planExecuteMaxDAGNodes:   chat.ChatPlanExecuteMaxDAGNodes(bg, siteCfg),
		planExecuteDAGIterative:  chat.ChatPlanExecuteDAGIterative(bg, siteCfg),
		agenticMaxHops:           chat.ChatAgenticMaxHops(bg, siteCfg),
		planningModel:            planningModel,
	}
}

// Search implements eval.Searcher. Classifies the query, picks an
// orchestrator, runs it (or falls back to the standard adapter), and
// returns retrieval-shaped chunks sorted by score for metric purposes.
func (a *OrchestratorDispatchAdapter) Search(ctx context.Context, q Question, k int) ([]RetrievedChunk, error) {
	// W6-R7: wrap the whole question's dispatch (classification, the chosen
	// orchestrator's search/answer fan-out, and — for the standard branch —
	// a.prod.Search) in one call counter so AgentTrace.LLMCalls reports every
	// model-provider request this question caused, not just the ones inside
	// a single orchestrator branch. Every trace assignment below copies
	// callCounter.Count() before returning.
	ctx, callCounter := ai.WithCallCounter(ctx)
	queryType := ClassifyQueryTypeForEval(ctx, a.aiResolver, q.Question, q.KbID, q.Language)
	orchestrator, dispatchReason, policyDec := SelectOrchestrator(ctx, a.siteCfg, queryType, q.Question, PolicySignalsForQuestion(queryType, q))

	slog.Info("eval.orchestrator_dispatch",
		"question_id", q.ID,
		"kb_id", q.KbID,
		"query_type", queryType,
		"orchestrator", orchestrator,
		"dispatch_reason", dispatchReason,
	)

	// W6-R6: the rule index goes on the trace only when a rule actually
	// PINNED the route, mirroring agent_decisions.policy_rule. A matched but
	// unapplied "prefer" rule left the ladder in charge and is visible in the
	// dispatch reason, not here.
	var policyRule *int
	if policyDec.Applied {
		idx := policyDec.RuleIndex
		policyRule = &idx
	}

	if orchestrator == OrchestratorStandard {
		out, err := a.prod.Search(ctx, q, k)
		if err == nil {
			var tab *TabularEvalTrace
			if chatCtx, hit := a.prod.ChatContextForQuestion(q.ID); hit {
				tab = TabularEvalTraceFrom(chatCtx.TabularTrace)
			}
			a.traceCache[q.ID] = &AgentTrace{
				Orchestrator:        OrchestratorStandard,
				ClassifiedQueryType: queryType,
				DispatchReason:      dispatchReason,
				Tabular:             tab,
				PolicyRule:          policyRule,
				LLMCalls:            callCounter.Count(),
			}
		}
		return out, err
	}

	kbSystemPrompt := ""
	if a.kbSystemPrompt != nil {
		kbSystemPrompt = a.kbSystemPrompt(ctx, q.KbID)
	}

	var events []chat.TrajectoryEvent
	emit := CollectEmit(&events)

	planInputs := PlanShapeInputs{}
	var chatCtx *chat.ChatContext
	var err error
	switch orchestrator {
	case OrchestratorSupervisor:
		// Resolved HERE, not at wiring time — mirrors
		// internal/chat/http_send.go's OrchSupervisor case and
		// trajectory_runner.go's TrajectoryModeSupervisor case: a.siteCfg
		// is the per-KB-overlaid reader (when the caller supplies one),
		// so this must be the source of the router's config on THIS
		// question's KB rather than whatever reader a.prod.tabularRouter
		// was constructed with.
		var tabularCfg *chat.TabularRouterConfig
		if a.siteCfg != nil {
			cfg := chat.ResolveTabularRouterConfig(ctx, a.siteCfg)
			tabularCfg = &cfg
		}
		// Same reasoning as tabularCfg above for the W5-R7 conflict pass:
		// resolved from the per-KB-overlaid reader HERE, mirroring
		// internal/chat/http_send.go's OrchSupervisor case, so a
		// --conflict-surfacing run reaches the Supervisor path too. A
		// disabled config is the zero-cost no-op RunSupervisorChat
		// already handles.
		var conflictCfg chat.ConflictConfig
		if a.siteCfg != nil && chat.ChatConflictSurfacingEnabled(ctx, a.siteCfg) {
			conflictCfg = chat.ResolveConflictConfig(ctx, a.siteCfg)
		}
		chatCtx, err = chat.RunSupervisorChat(ctx, a.aiResolver, a.searchService, chat.SupervisorChatParams{
			KbID:                q.KbID,
			Query:               q.Question,
			Language:            q.Language,
			KbSystemPrompt:      kbSystemPrompt,
			PlanningModel:       a.planningModel,
			TabularRouter:       a.prod.tabularRouter,
			TabularRouterConfig: tabularCfg,
			ConflictConfig:      conflictCfg,
			FileDates:           a.prod.fileDates,
		}, emit)
	case OrchestratorPlanExecute, OrchestratorPlanExecuteDAG:
		dag := orchestrator == OrchestratorPlanExecuteDAG
		planInputs = PlanShapeInputs{
			DAG:       dag,
			MaxDepth:  a.planExecuteMaxDAGDepth,
			Iterative: dag && a.planExecuteDAGIterative,
		}
		chatCtx, err = chat.RunPlanExecuteChat(ctx, a.aiResolver, a.searchService, chat.PlanExecuteParams{
			KbID:           q.KbID,
			Query:          q.Question,
			Language:       q.Language,
			KbSystemPrompt: kbSystemPrompt,
			PlanningModel:  a.planningModel,
			MaxSubQueries:  a.planExecuteMaxSubQueries,
			MaxIterations:  a.planExecuteMaxIterations,
			TokenBudget:    a.planExecuteTokenBudget,
			DAG:            dag,
			MaxDAGDepth:    a.planExecuteMaxDAGDepth,
			MaxDAGNodes:    a.planExecuteMaxDAGNodes,
		}, emit)
	case OrchestratorDrift:
		// Resolved HERE, not at wiring time — mirrors the Supervisor case
		// above and http_send.go's OrchDrift case: chat_drift_model must
		// come from a.siteCfg (the per-KB-overlaid reader) for THIS
		// question's KB, not from a resolver snapshotted at construction.
		chatCtx, err = chat.RunDriftChat(ctx, a.aiResolver, a.searchService, chat.DriftChatParams{
			KbID:           q.KbID,
			Query:          q.Question,
			Language:       q.Language,
			KbSystemPrompt: kbSystemPrompt,
			PlanningModel:  chat.ResolveFastTierModel(ctx, a.siteCfg, "chat_drift_model"),
			MaxFollowups:   chat.ChatDriftMaxFollowups(ctx, a.siteCfg),
			PrimerTopK:     chat.ChatDriftPrimerTopK(ctx, a.siteCfg),
			SearchTopK:     chat.ChatDriftSearchTopK(ctx, a.siteCfg),
		}, emit)
	case OrchestratorLongContext:
		chatCtx, err = chat.RunLongContextChat(ctx, a.aiResolver, a.searchService, a.siteCfg, chat.LongContextParams{
			KbID:           q.KbID,
			Query:          q.Question,
			Language:       q.Language,
			KbSystemPrompt: kbSystemPrompt,
			Emit:           emit,
		})
	case OrchestratorAgentic:
		chatCtx, err = chat.RunAgenticChat(ctx, a.aiResolver, a.searchService, chat.AgenticChatParams{
			KbID:           q.KbID,
			Query:          q.Question,
			Language:       q.Language,
			KbSystemPrompt: kbSystemPrompt,
			PlanningModel:  a.planningModel,
			MaxHops:        a.agenticMaxHops,
		}, emit)
	default:
		err = fmt.Errorf("unknown orchestrator %q", orchestrator)
	}

	if err != nil {
		// Production silently falls through to the standard path when an
		// orchestrator errors (http_send.go:927). Mirror that here and
		// record the failed orchestrator on the dispatch reason so the
		// report tells the truth about what happened.
		slog.Warn("eval.orchestrator_failed_fallback",
			"question_id", q.ID,
			"orchestrator", orchestrator,
			"error", err.Error(),
		)
		out, perr := a.prod.Search(ctx, q, k)
		if perr == nil {
			a.traceCache[q.ID] = &AgentTrace{
				Orchestrator:        OrchestratorStandard,
				ClassifiedQueryType: queryType,
				DispatchReason:      orchestrator + "_error_fallback",
				// No PolicyRule: the forced orchestrator errored and the
				// standard path answered, so the rule did not decide the
				// route that produced these chunks (W6-R16's
				// "dependencies missing" fallback).
				LLMCalls: callCounter.Count(),
			}
		}
		return out, perr
	}

	trace := BuildAgentTrace(orchestrator, dispatchReason, events, planInputs)
	trace.ClassifiedQueryType = queryType
	trace.PolicyRule = policyRule
	trace.LLMCalls = callCounter.Count()
	// Only the Supervisor path actually runs the tabular router today
	// (RunPlanExecuteChat / RunAgenticChat never set TabularTrace), so
	// this is a no-op for those orchestrators — TabularEvalTraceFrom
	// returns nil for a nil TabularTrace.
	trace.Tabular = TabularEvalTraceFrom(chatCtx.TabularTrace)
	a.traceCache[q.ID] = trace
	a.chunkCache[q.ID] = chatCtx.FinalChunks
	a.ctxCache[q.ID] = chatCtx

	// FinalChunks is in sandwich order (best at 0 and N-1). For retrieval
	// metrics we need top-k by score, so re-sort. The cached chunks are
	// kept as-is so ContentsForQuestion can still serve them in their
	// original order for judge-mode (where sandwich order matches what
	// the LLM would have seen).
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

// AgentTraceForQuestion satisfies the agentTracer interface RunEval
// detects via type assertion.
func (a *OrchestratorDispatchAdapter) AgentTraceForQuestion(questionID string) *AgentTrace {
	return a.traceCache[questionID]
}

// ContentsForQuestion returns chunk content + file names per question.
// For orchestrator-branch questions, served from this adapter's
// chunkCache (the standard branch's chunks live on the embedded
// ProductionContextAdapter's cache).
func (a *OrchestratorDispatchAdapter) ContentsForQuestion(questionID string, k int) (contents []string, fileNames []string, ok bool) {
	if chunks, hit := a.chunkCache[questionID]; hit {
		if k > 0 && k < len(chunks) {
			chunks = chunks[:k]
		}
		contents = make([]string, len(chunks))
		fileNames = make([]string, len(chunks))
		for i, c := range chunks {
			contents[i] = c.Content
			fileNames[i] = c.FileName
		}
		return contents, fileNames, true
	}
	return a.prod.ContentsForQuestion(questionID, k)
}

// ChatContextForQuestion returns the ChatContext an orchestrator branch
// produced, falling back to the embedded ProductionContextAdapter for
// standard-branch questions.
//
// Before Wave 3 this delegated unconditionally, so every orchestrator-branch
// question missed in judge mode and the judge graded a generic content-based
// answer rather than the prompt the orchestrator actually assembled. That
// matters most for OrchLongContext's map_reduce mode, whose whole point is
// that the prompt carries findings instead of raw chunk bodies.
func (a *OrchestratorDispatchAdapter) ChatContextForQuestion(questionID string) (*chat.ChatContext, bool) {
	if cc, hit := a.ctxCache[questionID]; hit && cc != nil {
		return cc, true
	}
	return a.prod.ChatContextForQuestion(questionID)
}

// ConflictsForQuestion satisfies the conflictTracer interface RunEval detects
// by type assertion. Reads through ChatContextForQuestion, so it covers every
// branch that produces a ChatContext — the Supervisor and long-context
// orchestrators as well as the standard fallback — without a second cache.
func (a *OrchestratorDispatchAdapter) ConflictsForQuestion(questionID string) []chat.MessageConflict {
	cc, ok := a.ChatContextForQuestion(questionID)
	if !ok || cc == nil {
		return nil
	}
	return chat.ConflictsForWire(cc.Conflicts)
}

// PolicySignalsForQuestion resolves the chatpolicy signal bag from an eval
// question, mirroring what internal/chat builds per turn.
//
// Two signals are structurally unavailable here and are documented as false /
// zero rather than guessed: a golden question carries no user file selection
// (HasFileSelection) and single-turn eval has no conversation history
// (HistoryTurns) — a multi-turn replay does not reach this adapter at all
// (turns bypass orchestrator dispatch). A policy rule that keys on either of
// them therefore never fires under eval, which is the honest outcome: the
// harness cannot produce the turn shape it describes.
func PolicySignalsForQuestion(queryType string, q Question) chatpolicy.Signals {
	return chatpolicy.Signals{
		QueryType:        queryType,
		GlobalSynthesis:  chat.IsGlobalSynthesisQuery(q.Question),
		Enumeration:      chat.IsEnumerationQuery(q.Question, q.Language),
		RecencyListing:   chat.IsRecencyListingQuery(q.Question),
		HasFileSelection: false,
		HistoryTurns:     0,
		KBID:             q.KbID,
	}
}
