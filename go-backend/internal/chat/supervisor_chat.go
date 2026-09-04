package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/agents"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
)

// SupervisorChatParams configures a Phase 3 §3.2 supervisor chat run.
// Mirrors PlanExecuteParams's surface so tryDeepChat can swap one for
// the other without rebuilding the orchestrator-call site.
type SupervisorChatParams struct {
	KbID           string
	Query          string
	Language       string
	FileIDs        []string
	KbSystemPrompt string
	PlanningModel  string
	// GraphChunkIDs forwards the AP-C4 graph router's resolved
	// subgraph chunk IDs into the agents.Input the supervisor
	// dispatches. The Retriever / Enumerator specialist then folds
	// them into its single SearchOptions call. Empty (default)
	// preserves legacy behaviour.
	GraphChunkIDs []string
	// BridgeChunks forwards the bridge-evidence tally (chunk_id -> bridge
	// count) into the specialist's SearchOptions for post-rerank multi-hop
	// boosting. Nil (default) leaves the boost inert.
	BridgeChunks map[string]int
	// HyPESearch enables the HyPE query-time arm on this orchestrator's
	// initial search (resolved from hype_search_enabled at dispatch).
	HyPESearch bool
	// SufficientContextEnabled enables the Q2 sufficient-context
	// abstention gate on the assembled specialist context. Resolved by
	// the caller from chat_sufficient_context_enabled (the supervisor
	// has no SiteConfigReader — flags arrive pre-resolved, same pattern
	// as MultiSpecialist). SufficientContextModel carries the resolved
	// fast-tier model override. False (default) skips the gate.
	SufficientContextEnabled bool
	SufficientContextModel   string
	// MultiSpecialist routes through Supervisor.RunMulti instead of the
	// single-specialist Run: on enumeration intent the retriever and
	// enumerator dispatch in parallel and their results merge via RRF.
	// Set from chat_supervisor_multi_specialist. False (default)
	// preserves the legacy single-pass behaviour.
	MultiSpecialist bool
	// CurrentDateLine is the localized current-date line to append to the
	// answer system prompt (empty when chat_date_awareness_enabled is off).
	// Set at dispatch via SystemPromptDateLine.
	CurrentDateLine string
	// TabularRouter backs the deterministic spreadsheet path — same
	// contract as ChatContextParams.TabularRouter: it runs before the
	// specialist dispatch, promotes identifier literals in the query the
	// specialist searches with, forces the simple BM25 arm, and injects
	// its result rows as a system-prompt addendum. Nil disables it
	// (nil-receiver safe regardless).
	TabularRouter *TabularRouter
	// TabularRouterConfig is the router's config resolved by the caller
	// from the reader in force for THIS KB (the handler overlays it per
	// KB — see Handler.forKB). The supervisor has no SiteConfigReader of
	// its own, so like SufficientContextEnabled the flag arrives
	// pre-resolved. Nil falls back to the router's wiring-time cfgFn,
	// which reads the global reader only.
	TabularRouterConfig *TabularRouterConfig
}

// RunSupervisorChat is the production entry point. It routes the query
// to the supervisor's classifier and assembles a ChatContext from the
// resulting chunks. By default it is single-pass (one specialist). When
// SupervisorChatParams.MultiSpecialist is set (from
// chat_supervisor_multi_specialist), enumeration-intent queries
// dispatch the retriever and enumerator in parallel and merge their
// results via RRF — see Supervisor.RunMulti.
func RunSupervisorChat(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	params SupervisorChatParams,
	emit func(data map[string]any),
) (*ChatContext, error) {
	return runSupervisorChatTestable(ctx, aiResolver, searchSvc, IsEnumerationQuery, params, emit)
}

// runSupervisorChatTestable accepts the searcher + classifier as
// injectable arguments so unit tests drive the loop deterministically.
func runSupervisorChatTestable(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searcher searchInvoker,
	classify func(query, lang string) bool,
	params SupervisorChatParams,
	emit func(data map[string]any),
) (*ChatContext, error) {
	const maxTokens = 120_000

	retriever := agents.NewRetrieverAgent(searcher, params.PlanningModel)
	enumerator := agents.NewEnumeratorAgent(searcher, params.PlanningModel, classify)
	sup := agents.NewSupervisor(retriever, enumerator, classify)

	// AP-A3: supervisor is one-stage by design (no loop), so a
	// pre-dispatch budget check just records exhaustion in the
	// metric — the dispatch still runs because returning here would
	// give the user nothing. The plan explicitly calls out logging
	// only for this orchestrator.
	if budget := TurnBudgetFromContext(ctx); budget != nil {
		_ = budget.CheckExceeded(ctx, "supervisor")
	}

	emitTrajectory(emit,
		TrajectoryEvent{Stage: "plan", Reason: "supervisor.dispatch"},
		map[string]any{"supervisorStage": "dispatch"},
	)

	// Deterministic tabular path (design §5.1), same contract as the
	// standard path in PrepareChatContext: it runs before the specialist
	// dispatch so its retrieval hints reach the specialist's single
	// SearchOptions, and its rows are injected below as a prompt
	// addendum. Fail-open: Run never errors.
	in := agents.Input{
		KbID:          params.KbID,
		Query:         params.Query,
		Language:      params.Language,
		FileIDs:       params.FileIDs,
		GraphChunkIDs: params.GraphChunkIDs,
		BridgeChunks:  params.BridgeChunks,
		HyPESearch:    params.HyPESearch,
	}
	var tabularAddendum string
	var tabularTrace *TabularTrace
	if params.TabularRouter != nil {
		tab := params.TabularRouter.Run(ctx, TabularRouterInput{
			KbID:     params.KbID,
			Query:    params.Query,
			Language: params.Language,
			Emit:     emit,
			Config:   params.TabularRouterConfig,
		})
		tabularTrace = tab.Trace
		tabularAddendum = tab.Addendum
		if tab.SearchQuery != "" {
			// Retrieval only. The supervisor's routing classifier and the
			// specialists both read in.Query, so they see the promoted
			// phrasing; params.Query keeps the user's wording for the
			// sufficient-context gate below. Note the supervisor
			// carries no Enhance mode, so unlike the standard path there
			// is no query-rewriting stage to feed the quotes into.
			in.Query = tab.SearchQuery
		}
		in.ForceBM25SimpleArm = tab.ForceSimpleArm
	}
	var (
		res agents.SupervisorResult
		err error
	)
	if params.MultiSpecialist {
		res, err = sup.RunMulti(ctx, in)
	} else {
		res, err = sup.Run(ctx, in)
	}
	if err != nil {
		return nil, fmt.Errorf("supervisor: %w", err)
	}

	emitTrajectory(emit,
		TrajectoryEvent{
			Stage:    "decision",
			Decision: "agent_dispatch",
			Reason:   res.Specialist,
			Findings: len(res.Chunks),
		},
		map[string]any{"agentDispatch": map[string]any{
			"specialist": res.Specialist,
			"chunks":     len(res.Chunks),
		}},
	)
	logctx.From(ctx).Info("rag.supervisor_chat.dispatch",
		"specialist", res.Specialist,
		"chunks", len(res.Chunks),
	)

	accumulated := res.Chunks
	if len(accumulated) == 0 {
		return nil, fmt.Errorf("supervisor: no chunks from specialist %s", res.Specialist)
	}

	// ---------- GENERATE (same tail as plan-execute / agentic) ----------
	accumulated = TruncateChunksToFit(accumulated, maxTokens)
	accumulated = SandwichOrder(accumulated)

	sources, contextText := buildChatSourcesAndContext(accumulated)

	// Q2 sufficient-context gate, same contract as the standard path in
	// PrepareChatContext (see the comment there). The supervisor is the
	// production orchestrator, so the gate matters most here: the
	// specialist's chunks can be individually relevant yet jointly
	// insufficient. Fail-open inside JudgeContextSufficiency.
	abstain := false
	if params.SufficientContextEnabled {
		if !ai.JudgeContextSufficiency(ctx, aiResolver, params.KbID, params.Query, contextText, params.Language, params.SufficientContextModel) {
			abstain = true
			logctx.From(ctx).Info("rag.sufficient_context.abstain",
				"chunks_in_context", len(accumulated), "kb_id", params.KbID, "orchestrator", "supervisor")
		}
	}

	var sb strings.Builder
	if params.KbSystemPrompt != "" {
		sb.WriteString(params.KbSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompts.ChatSystemPromptWithDate(params.Language, params.CurrentDateLine))
	switch {
	case abstain:
		sb.WriteString(prompts.ChatAbstainNotice(params.Language))
	case IsLowConfidence(accumulated):
		sb.WriteString(prompts.ChatLowConfidenceNotice(params.Language))
	}
	if tabularAddendum != "" {
		// Before AGENT NOTES and CONTEXT: the executed rows are ground
		// truth the answer LLM should prefer over the retrieved prose.
		sb.WriteString("\n\n")
		sb.WriteString(tabularAddendum)
	}
	if res.Notes != "" {
		sb.WriteString("\n\nAGENT NOTES:\n")
		sb.WriteString(res.Notes)
	}
	sb.WriteString("\n\nCONTEXT:\n")
	sb.WriteString(contextText)

	return &ChatContext{
		SystemPrompt: sb.String(),
		Sources:      sources,
		Context:      contextText,
		FinalChunks:  accumulated,
		Abstain:      abstain,
		TabularTrace: tabularTrace,
	}, nil
}
