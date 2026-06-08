package eval

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
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
// (internal/chat/http_send.go:816-823). Returns the orchestrator label
// and a short human-readable dispatch reason. Eval has no Enhance
// parameter so the `body.Enhance == ""` clause is implicitly true.
func SelectOrchestrator(ctx context.Context, siteCfg chat.SiteConfigReader, queryType string) (string, string) {
	if queryType != vector.QueryTypeComplexReasoning {
		return OrchestratorStandard, "fallback_query_type_" + queryType
	}
	if chat.ChatSupervisorEnabled(ctx, siteCfg) {
		return OrchestratorSupervisor, "complex_reasoning_supervisor_gate"
	}
	if chat.ChatPlanExecuteEnabled(ctx, siteCfg) {
		if chat.ChatPlanExecuteDAG(ctx, siteCfg) {
			return OrchestratorPlanExecuteDAG, "complex_reasoning_plan_execute_dag_gate"
		}
		return OrchestratorPlanExecute, "complex_reasoning_plan_execute_gate"
	}
	if chat.ChatAgenticEnabled(ctx, siteCfg) {
		return OrchestratorAgentic, "complex_reasoning_agentic_gate"
	}
	return OrchestratorStandard, "fallback_no_orchestrator_enabled"
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
) *OrchestratorDispatchAdapter {
	prod := NewProductionContextAdapter(aiResolver, searchService, siteCfg, flags)
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
	queryType := ClassifyQueryTypeForEval(ctx, a.aiResolver, q.Question, q.KbID, q.Language)
	orchestrator, dispatchReason := SelectOrchestrator(ctx, a.siteCfg, queryType)

	slog.Info("eval.orchestrator_dispatch",
		"question_id", q.ID,
		"kb_id", q.KbID,
		"query_type", queryType,
		"orchestrator", orchestrator,
		"dispatch_reason", dispatchReason,
	)

	if orchestrator == OrchestratorStandard {
		out, err := a.prod.Search(ctx, q, k)
		if err == nil {
			a.traceCache[q.ID] = &AgentTrace{
				Orchestrator:        OrchestratorStandard,
				ClassifiedQueryType: queryType,
				DispatchReason:      dispatchReason,
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
		chatCtx, err = chat.RunSupervisorChat(ctx, a.aiResolver, a.searchService, chat.SupervisorChatParams{
			KbID:           q.KbID,
			Query:          q.Question,
			Language:       q.Language,
			KbSystemPrompt: kbSystemPrompt,
			PlanningModel:  a.planningModel,
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
			}
		}
		return out, perr
	}

	trace := BuildAgentTrace(orchestrator, dispatchReason, events, planInputs)
	trace.ClassifiedQueryType = queryType
	a.traceCache[q.ID] = trace
	a.chunkCache[q.ID] = chatCtx.FinalChunks

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

// ChatContextForQuestion delegates to the embedded ProductionContextAdapter.
// For orchestrator-branch questions, no full ChatContext is constructed
// (we have only the raw chunks), so judge-mode answer generation for
// those returns (nil, false) and the caller falls through to a generic
// content-based answer path.
func (a *OrchestratorDispatchAdapter) ChatContextForQuestion(questionID string) (*chat.ChatContext, bool) {
	return a.prod.ChatContextForQuestion(questionID)
}
