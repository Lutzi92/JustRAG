package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/workflow"

	"github.com/justrag/go-backend/internal/adkbridge"
	"github.com/justrag/go-backend/internal/agui"
	"github.com/justrag/go-backend/internal/logctx"
)

// Session state keys the retrieve node writes (non-temp, so they survive a
// pause): the dead-end nodes recompute the offered actions from them
// server-side, never from the client.
const (
	AgentChatStateQuestion = "agentchat.question"
	AgentChatStateReason   = "agentchat.reason"
)

// agentNoEvidenceText is kb_search's result when retrieval abstains: the
// model must not see rendered chunks whose [n] markers Sources() lacks.
const agentNoEvidenceText = "Keine relevanten Treffer in der Wissensbasis."

// agentToolBudgetExhaustedText is the result of an answer-time tool call
// past the turn's cap.
const agentToolBudgetExhaustedText = "Werkzeug-Budget für diese Antwort erschöpft: beantworte die Frage jetzt mit dem vorhandenen Kontext."

// FileCounter reports how many files a KB holds.
type FileCounter func(ctx context.Context, kbID string) (int, error)

// AgentFlowDeps are the per-turn dependencies of NewAgentFlow.
type AgentFlowDeps struct {
	Model     *adkbridge.Model
	Retriever *AgentRetriever
	// Tools are the answer-time tools, approval-free only (kb_search via
	// Retriever, memory_*; Ruling P2-R15).
	Tools []tool.Tool
	// FileCounter, when set, routes a KB with no files to no_files before
	// retrieval. nil skips the check.
	FileCounter FileCounter
	// Allowed are the tool names a dead end may offer (DeadEndActions,
	// filtered by adkbridge.VisibleActions). Empty offers nothing.
	Allowed []string
	// ActDispatch executes a chosen action (see ActDispatchers); entries
	// for tools outside Allowed are dropped.
	ActDispatch map[string]adkbridge.DispatchFunc
	// Available reports whether a dead-end tool can run for this user right
	// now (web search enabled and configured, Confluence enabled and
	// connected). suggest and act both apply it, so a button is never
	// offered that would fail. nil treats every dispatchable tool as
	// available.
	Available ActionAvailability
	// MaxToolCalls caps the answer agent's tool calls per turn
	// (chat_answer_tools_max_rounds); 0 = no cap.
	MaxToolCalls int
}

// ActionAvailability is AgentFlowDeps.Available.
type ActionAvailability func(ctx context.Context, sc adkbridge.Scope, tool string) bool

// NewAgentFlow returns the per-turn workflow agent:
//
//	Start → retrieve ─found→ answer → sources
//	                 └no_evidence|no_files→ suggest ⏸ → act ─web_answer→ web_answer
//	                                                  └no_actions→ dead_end
//
// suggest pauses with the offered actions; the resume payload is act's
// input. act ends the turn itself unless the user chose a web search.
func NewAgentFlow(deps AgentFlowDeps) (agent.Agent, error) {
	if deps.Model == nil || deps.Retriever == nil {
		return nil, errors.New("chat: agent flow needs a model and a retriever")
	}
	retriever := deps.Retriever
	budget := &toolCallBudget{max: deps.MaxToolCalls}
	answerAgent, err := llmagent.New(llmagent.Config{
		Name:        "answer",
		Description: "Answers the question from the retrieved knowledge-base context.",
		Model:       deps.Model,
		InstructionProvider: func(agent.ReadonlyContext) (string, error) {
			return retriever.SystemPrompt(), nil
		},
		Tools:                deps.Tools,
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{budget.beforeModel},
		BeforeToolCallbacks:  []llmagent.BeforeToolCallback{budget.beforeTool},
	})
	if err != nil {
		return nil, fmt.Errorf("chat: answer agent: %w", err)
	}
	answer, err := workflow.NewAgentNode(answerAgent, workflow.NodeConfig{})
	if err != nil {
		return nil, fmt.Errorf("chat: answer node: %w", err)
	}
	retrieve := agentRetrieveNode(retriever, deps.FileCounter)
	sources := agentSourcesNode(retriever)

	edges := workflow.Concat(
		workflow.Chain(workflow.Start, retrieve),
		[]workflow.Edge{{From: retrieve, To: answer, Route: workflow.StringRoute(adkbridge.RouteFound)}},
		workflow.Chain(answer, sources),
	)
	deadEnd, err := deadEndEdges(retrieve, deps)
	if err != nil {
		return nil, err
	}
	edges = append(edges, deadEnd...)
	return workflowagent.New(workflowagent.Config{
		Name:        "agentchat",
		Description: "Agentic chat turn: retrieval floor, then answer.",
		Edges:       edges,
	})
}

// deadEndEdges wires the no_evidence and no_files routes to suggest → act.
// suggest → act is the Default route: a resume hands the payload over
// with no event, and only unconditional or Default edges fire then.
func deadEndEdges(retrieve workflow.Node, deps AgentFlowDeps) ([]workflow.Edge, error) {
	webAgent, err := llmagent.New(llmagent.Config{
		Name:        "web_answer",
		Description: "Answers the question from the web search results the user asked for.",
		Model:       deps.Model,
		Instruction: webAnswerInstruction,
		// Only the current turn (the question and the fenced results act
		// hands over): the session's earlier KB chunks and answers must not
		// sit next to attacker-controlled web text, which could exfiltrate
		// them through links or images in the answer. Explicit, so it does
		// not depend on the node placement's default.
		IncludeContents: llmagent.IncludeContentsNone,
	})
	if err != nil {
		return nil, fmt.Errorf("chat: web answer agent: %w", err)
	}
	webAnswer, err := workflow.NewAgentNode(webAgent, workflow.NodeConfig{})
	if err != nil {
		return nil, fmt.Errorf("chat: web answer node: %w", err)
	}
	// One allowlist-filtered map for both nodes, so suggest offers exactly
	// what act can run.
	dispatch := allowlistedDispatch(deps.ActDispatch, deps.Allowed)
	offer := actionOffer{allowed: deps.Allowed, dispatch: dispatch, available: deps.Available}
	suggest := agentSuggestNode(offer)
	act := agentActNode(offer)
	// Repeats suggest's text as the turn's final output.
	deadEnd := workflow.NewFunctionNode("dead_end",
		func(_ agent.Context, text string) (string, error) { return text, nil }, workflow.NodeConfig{})
	return []workflow.Edge{
		{From: retrieve, To: suggest, Route: workflow.MultiRoute[string]{adkbridge.RouteNoEvidence, adkbridge.RouteNoFiles}},
		{From: suggest, To: deadEnd, Route: workflow.StringRoute(routeNoActions)},
		{From: suggest, To: act, Route: workflow.Default},
		{From: act, To: webAnswer, Route: workflow.StringRoute(routeWebAnswer)},
	}, nil
}

// agentRetrieveNode runs the production retrieval floor for the question
// (the node input) and routes found / no_evidence / no_files. Its output is
// the question, which the answer node receives as user content.
func agentRetrieveNode(r *AgentRetriever, files FileCounter) workflow.Node {
	return workflow.NewEmittingFunctionNode("retrieve",
		func(ctx agent.Context, q string, emit func(*session.Event) error) (string, error) {
			route, query, err := agentRetrieveRoute(ctx, r, files, q)
			if err != nil {
				return "", err
			}
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.Routes = []string{route}
			// The searched (condensed) query: the dead end offers it to the
			// web search, which sees no history to resolve a follow-up.
			ev.Actions.StateDelta = map[string]any{AgentChatStateQuestion: query, AgentChatStateReason: route}
			if err := emit(ev); err != nil {
				return "", err
			}
			return q, nil
		}, workflow.NodeConfig{})
}

// agentRetrieveRoute routes the turn and returns the query the floor
// retrieval searched (the question itself when it routed before
// retrieval).
func agentRetrieveRoute(ctx context.Context, r *AgentRetriever, files FileCounter, q string) (route, query string, err error) {
	sc, ok := adkbridge.ScopeFrom(ctx)
	if !ok {
		return "", q, adkbridge.ErrNoScope
	}
	if sc.KBID == "" {
		if len(sc.LibraryFileIDs) > 0 {
			return "", q, adkbridge.ErrLibraryScopeUnsupported
		}
		return adkbridge.RouteNoFiles, q, nil
	}
	// The node dispatches kb_search itself, outside the bridge's tool
	// policy, so it enforces kb_search's role floor (fails closed on an
	// empty or unknown role) — as adkbridge.RetrieveNode does.
	if need := adkbridge.PolicyFor("kb_search").RequiresRole; !adkbridge.RoleAtLeast(sc.Role, need) {
		return "", q, fmt.Errorf("%w: kb_search requires role %s", adkbridge.ErrForbiddenTool, need)
	}
	if files != nil {
		n, err := files(ctx, sc.KBID)
		switch {
		case err != nil:
			// Fail soft: an empty KB then routes no_evidence via retrieval.
			logctx.From(ctx).Warn("agentchat: file count failed", "kb_id", sc.KBID, "error", err)
		case n == 0:
			return adkbridge.RouteNoFiles, q, nil
		}
	}
	query = r.floorQuery(ctx, q)
	args, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		return "", query, err
	}
	res, err := r.Dispatch(ctx, sc.KBID, "kb_search", args)
	if err != nil {
		return "", query, err
	}
	if len(res.Chunks) == 0 {
		return adkbridge.RouteNoEvidence, query, nil
	}
	return adkbridge.RouteFound, query, nil
}

// agentSourcesNode emits the turn's final numbered sources ([]ChatSource
// JSON) as the justrag.sources.v1 CUSTOM event. It has no output: the
// answer already reached the client as model text, and a string output
// would be shown a second time as the run's final word.
func agentSourcesNode(r *AgentRetriever) workflow.Node {
	return workflow.NewEmittingFunctionNode("sources",
		func(ctx agent.Context, _ string, emit func(*session.Event) error) (any, error) {
			ev := session.NewEvent(ctx, ctx.InvocationID())
			ev.CustomMetadata = agui.CustomMetadata(agui.SourcesEvent, r.Sources())
			return nil, emit(ev)
		}, workflow.NodeConfig{})
}

// toolCallBudget caps the answer agent's tool calls in one turn (the
// legacy answer-tools loop's chat_answer_tools_max_rounds). One per flow,
// i.e. per request. max 0 = no cap.
type toolCallBudget struct {
	max  int
	mu   sync.Mutex
	used int
}

func (b *toolCallBudget) exhausted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.max > 0 && b.used >= b.max
}

// beforeTool counts a call, or refuses it past the cap with a model-visible
// result (the tool does not run).
func (b *toolCallBudget) beforeTool(_ agent.Context, _ tool.Tool, _ map[string]any) (map[string]any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max > 0 && b.used >= b.max {
		return map[string]any{"error": agentToolBudgetExhaustedText}, nil
	}
	b.used++
	return nil, nil
}

// beforeModel stops declaring tools once the budget is spent, so the model
// answers instead of asking for more (as the legacy loop forces a no-tool
// finish). Only the declarations go: the tool map stays, so a call the
// model emits anyway is still resolved — and refused by beforeTool.
func (b *toolCallBudget) beforeModel(_ agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	if b.exhausted() && req.Config != nil {
		req.Config.Tools = nil
		req.Config.ToolConfig = nil
	}
	return nil, nil
}
