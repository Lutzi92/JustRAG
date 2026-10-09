package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/justrag/go-backend/internal/adkbridge"
	"github.com/justrag/go-backend/internal/agui"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/sessionmem"
	"github.com/justrag/go-backend/internal/siteconfig"
	"github.com/justrag/go-backend/internal/usage"
	"github.com/justrag/go-backend/internal/vector"
)

// agentChatApp is the ADK app name of the agent chat: the runner's, the
// agui handler's and the session/run rows'.
const agentChatApp = "agentchat"

// agentChatMaxBody matches agui's own body cap.
const agentChatMaxBody = 1 << 20

// agentKBSearchSchema is the answer-time kb_search input. AgentRetriever
// reads only the query: the production pipeline decides everything else.
const agentKBSearchSchema = `{"type":"object","properties":{"query":{"type":"string","description":"What to search the knowledge base for."}},"required":["query"]}`

// AgentChatDeps are the shared dependencies of the agent chat endpoint.
type AgentChatDeps struct {
	Store      *PGStore
	SiteConfig SiteConfigReader       // global reader
	KBConfig   KBConfigOverrideLister // per-KB overrides (as Handler.forKB)
	AI         *ai.ConfigResolver
	Search     *vector.SearchService
	Registry   *mcp.Registry // memory_* tools, dead-end web action; may be nil
	Sessions   *adkbridge.PGSessionService
	Runs       *adkbridge.RunStore
	Usage      usage.Recorder // may be nil
	// SessionMemory, when set, enables the memory_* tools on KBs with
	// chat session memory on.
	SessionMemory sessionmem.Store
	Importer      *confluence.Importer // may be nil
	// ConfluenceConns looks up the user's Confluence connection: the
	// confluence action is offered only to a user who has one. nil never
	// offers it.
	ConfluenceConns ConfluenceConnections
	Library         LibraryAdder // may be nil
	// Files counts a KB's files (empty KB → no_files); nil skips the check.
	Files *files.PGStore
	// FileDates resolves source files' dates for the retrieval (as the
	// legacy chat's WithFileDates); may be nil.
	FileDates FileDateLookup

	// Test seams: the answer model and the retrieval (PrepareChatContext).
	modelFor func(ctx context.Context, kbID string) (*adkbridge.Model, error)
	prepare  func(ctx context.Context, p ChatContextParams) (*ChatContext, error)
	// kbPrompt is Store.GetKBSystemPrompt; condense is CondenseFollowUp
	// over Store.
	kbPrompt func(ctx context.Context, kbID string) (*string, error)
	condense func(ctx context.Context, chatID string, parent *string, q, kbID, lang string) (string, error)
}

// ConfluenceConnections is the connection lookup of confluence.PGStore.
type ConfluenceConnections interface {
	GetConfluenceConnectionByUserID(ctx context.Context, userID string) (*confluence.ConfluenceConnectionRow, error)
}

// AgentChatHandler serves POST /api/kb/{id}/agui/chat: the agentic chat as
// an AG-UI endpoint, behind the per-KB flag chat_agent_chat_enabled.
type AgentChatHandler struct{ d AgentChatDeps }

// NewAgentChatHandler returns the handler.
func NewAgentChatHandler(d AgentChatDeps) *AgentChatHandler { return &AgentChatHandler{d: d} }

// ServeHTTP answers 403 without KB access and 404 {"error":"not_found"}
// while the flag is off for the KB — both before anything is read, written
// or sent to a model. Otherwise it builds the turn's flow and runner and
// hands the request to agui (see agui.NewHandler for its status codes).
func (h *AgentChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := kbaccess.AccessFromContext(ctx)
	if access == nil || access.KB == nil {
		writeAgentChatError(w, http.StatusForbidden, "forbidden")
		return
	}
	kbID := access.KB.ID
	ctx = logctx.Attach(logctx.WithKB(ctx, kbID))
	reader, searcher := h.forKB(ctx, kbID)
	if !ChatAgentChatEnabled(ctx, reader) {
		writeAgentChatError(w, http.StatusNotFound, "not_found")
		return
	}

	// agui decodes the body itself; read it once here for forwardedProps
	// and hand agui an identical copy.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, agentChatMaxBody))
	if err != nil {
		writeAgentChatError(w, http.StatusBadRequest, "bad_request")
		return
	}
	var in types.RunAgentInput
	decoded := json.Unmarshal(raw, &in) == nil
	if decoded && len(in.Resume) == 0 {
		// A new question gets the legacy chat's checks (Ruling P2-R13),
		// before anything is written or sent to a model. A body agui
		// cannot use is left for agui to refuse.
		if m, err := agui.Input(ctx, &in, nil); err == nil {
			userID := ""
			if u := auth.UserFromContext(ctx); u != nil {
				userID = u.ID
			}
			if msg := messageTextError(userID, contentTextOf(m)); msg != "" {
				httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, msg)
				return
			}
		}
	}
	props := agentChatProps{language: defaultLanguage}
	if decoded {
		props = agentChatPropsOf(&in)
	}

	if ChatSessionMemoryEnabled(ctx, reader) {
		ctx = sessionmem.WithWriteCounter(ctx, sessionmem.NewWriteCounter())
	}
	hooks := &agentChatHooks{store: h.d.Store, usage: h.d.Usage}
	retriever := h.newRetriever(ctx, kbID, reader, searcher, props.language, hooks)
	hooks.retriever = retriever
	run, err := h.runner(ctx, kbID, reader, retriever, props.reasoning)
	if err != nil {
		logctx.From(ctx).Error("agentchat: build turn", "error", err)
		writeAgentChatError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	_, runTimeout := agentTurnLimits(ctx, reader)
	r = r.WithContext(ctx)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	agui.NewHandler(agui.Config{
		AppName:    agentChatApp,
		Runner:     run,
		Sessions:   h.d.Sessions,
		Runs:       h.d.Runs,
		Scope:      h.scope(access),
		Hooks:      hooks,
		RunTimeout: runTimeout,
	}).ServeHTTP(w, r)
}

// newRetriever builds the turn's retriever with the legacy standard path's
// prompt inputs (Ruling P2-R16): the KB's configured system prompt, the
// overlay's current-date line, the file-date lookup, and a condenser that
// rewrites a follow-up into a standalone query over the thread's persisted
// history (the history before this turn's question, read when the floor
// retrieval runs — after TurnStarted stored the question).
func (h *AgentChatHandler) newRetriever(ctx context.Context, kbID string, reader SiteConfigReader, searcher vector.Searcher, lang string, hooks *agentChatHooks) *AgentRetriever {
	r := &AgentRetriever{
		AI:              h.d.AI,
		Search:          searcher,
		Config:          reader,
		Lang:            lang,
		KbSystemPrompt:  h.kbSystemPrompt(ctx, kbID),
		CurrentDateLine: SystemPromptDateLine(ctx, reader, lang),
		FileDates:       h.d.FileDates,
		prepare:         h.d.prepare,
	}
	r.Condense = func(ctx context.Context, q string) string {
		if hooks.parentMsgID == nil || hooks.threadID == "" {
			return q // a new thread: no history to resolve against
		}
		out, err := h.condenseFollowUp(ctx, hooks.threadID, hooks.parentMsgID, q, kbID, lang)
		if err != nil {
			logctx.From(ctx).Warn("agentchat: condense follow-up failed", "chat_id", hooks.threadID, "error", err)
			return q
		}
		return out
	}
	return r
}

// kbSystemPrompt is the KB's configured prompt, "" when unset or on error
// (as the legacy assembleSystemPrompt).
func (h *AgentChatHandler) kbSystemPrompt(ctx context.Context, kbID string) string {
	get := h.d.kbPrompt
	if get == nil {
		if h.d.Store == nil {
			return ""
		}
		get = h.d.Store.GetKBSystemPrompt
	}
	sp, err := get(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Warn("agentchat: kb system prompt", "kb_id", kbID, "error", err)
		return ""
	}
	if sp == nil {
		return ""
	}
	return *sp
}

func (h *AgentChatHandler) condenseFollowUp(ctx context.Context, chatID string, parent *string, q, kbID, lang string) (string, error) {
	if h.d.condense != nil {
		return h.d.condense(ctx, chatID, parent, q, kbID, lang)
	}
	if h.d.Store == nil {
		return q, nil
	}
	return CondenseFollowUp(ctx, h.d.AI, h.d.Store, chatID, parent, q, kbID, lang)
}

// agentTurnLimits are the turn's limits, from the legacy readers on the KB
// overlay: the answer agent's tool-call cap (chat_answer_tools_max_rounds,
// counted per call) and the run's time budget (chat_turn_budget_seconds,
// 0 = none).
func agentTurnLimits(ctx context.Context, reader SiteConfigReader) (maxToolCalls int, runTimeout time.Duration) {
	return ChatAnswerToolsMaxRounds(ctx, reader), time.Duration(ChatTurnBudgetSeconds(ctx, reader)) * time.Second
}

// forKB overlays the KB's per-KB overrides on the global reader and the
// search service, as Handler.forKB does. searcher is nil without a
// search service.
func (h *AgentChatHandler) forKB(ctx context.Context, kbID string) (SiteConfigReader, vector.Searcher) {
	var reader SiteConfigReader = h.d.SiteConfig
	var searcher vector.Searcher
	if h.d.Search != nil {
		searcher = h.d.Search
	}
	if h.d.KBConfig == nil {
		return reader, searcher
	}
	overrides, err := h.d.KBConfig.ListKBOverrides(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Warn("chat.kb_config.load_failed", "kb_id", kbID, "error", err)
		return reader, searcher
	}
	if len(overrides) == 0 {
		return reader, searcher
	}
	overlay := siteconfig.NewKBOverlay(h.d.SiteConfig, overrides)
	if h.d.Search != nil {
		searcher = h.d.Search.CloneWithSiteConfigReader(overlay)
	}
	return overlay, searcher
}

// scope builds the run scope from the authenticated request: the user from
// auth, the KB and role from kbaccess, the privilege flag from the GLOBAL
// reader (a KB override must not unlock privileged tools).
func (h *AgentChatHandler) scope(access *kbaccess.KBAccessResult) agui.ScopeFunc {
	return func(r *http.Request) (adkbridge.Scope, error) {
		user := auth.UserFromContext(r.Context())
		if user == nil || user.ID == "" {
			return adkbridge.Scope{}, agui.ErrUnauthorized
		}
		return adkbridge.Scope{
			UserID:          user.ID,
			KBID:            access.KB.ID,
			Role:            access.Role,
			IsGlobal:        access.KB.IsGlobal,
			AllowPrivileged: AgentsAllowPrivilegedTools(r.Context(), h.d.SiteConfig),
		}, nil
	}
}

// runner builds the turn's flow (model, tools, dead-end actions) and its
// runner. Everything is per request: the retriever holds the turn's sources.
func (h *AgentChatHandler) runner(ctx context.Context, kbID string, reader SiteConfigReader, retriever *AgentRetriever, effort string) (*runner.Runner, error) {
	model, err := h.model(ctx, kbID, effort)
	if err != nil {
		return nil, err
	}
	act := ActDispatchers(h.d.Registry, h.d.Importer, h.d.Library)
	// Offer exactly what can execute.
	allowed := make([]string, 0, len(act))
	for name := range act {
		allowed = append(allowed, name)
	}
	slices.Sort(allowed)
	maxToolCalls, _ := agentTurnLimits(ctx, reader)
	var counter FileCounter
	if h.d.Files != nil {
		counter = FileCounterFromLimits(h.d.Files)
	}
	flow, err := NewAgentFlow(AgentFlowDeps{
		Model:        model,
		Retriever:    retriever,
		Tools:        h.answerTools(ctx, kbID, reader, retriever),
		FileCounter:  counter,
		Allowed:      allowed,
		ActDispatch:  act,
		Available:    h.actionAvailable,
		MaxToolCalls: maxToolCalls,
	})
	if err != nil {
		return nil, err
	}
	return runner.New(runner.Config{AppName: agentChatApp, Agent: flow, SessionService: h.d.Sessions, AutoCreateSession: true})
}

// actionAvailable reports whether a dead-end tool can run for the user
// now, from the same settings its executor reads, so the client is never
// offered a button that can only fail. Reads the GLOBAL reader: web search
// (research.WebClient) and Confluence read global settings, not KB
// overrides. Any lookup error fails closed.
func (h *AgentChatHandler) actionAvailable(ctx context.Context, sc adkbridge.Scope, tool string) bool {
	switch tool {
	case "web_search":
		// research.WebClient.Search refuses without all three.
		return siteConfigEquals(ctx, h.d.SiteConfig, "web_search_enabled", "true") &&
			siteConfigSet(ctx, h.d.SiteConfig, "google_search_api_key") &&
			siteConfigSet(ctx, h.d.SiteConfig, "google_search_cx")
	case "confluence_import":
		if h.d.ConfluenceConns == nil || !siteConfigEquals(ctx, h.d.SiteConfig, "confluence_enabled", "true") {
			return false
		}
		conn, err := h.d.ConfluenceConns.GetConfluenceConnectionByUserID(ctx, sc.UserID)
		if err != nil {
			logctx.From(ctx).Warn("agentchat: confluence connection lookup", "error", err)
			return false
		}
		return conn != nil
	default:
		return true
	}
}

func siteConfigValue(ctx context.Context, r SiteConfigReader, key string) string {
	if r == nil {
		return ""
	}
	v, err := r.GetSiteConfigValue(ctx, key)
	if err != nil {
		logctx.From(ctx).Warn("agentchat: read site config", "key", key, "error", err)
		return ""
	}
	if v == nil {
		return ""
	}
	return *v
}

func siteConfigEquals(ctx context.Context, r SiteConfigReader, key, want string) bool {
	return siteConfigValue(ctx, r, key) == want
}

func siteConfigSet(ctx context.Context, r SiteConfigReader, key string) bool {
	return siteConfigValue(ctx, r, key) != ""
}

func (h *AgentChatHandler) model(ctx context.Context, kbID, effort string) (*adkbridge.Model, error) {
	if h.d.modelFor != nil {
		return h.d.modelFor(ctx, kbID)
	}
	return adkbridge.NewModelFactory(h.d.AI).For(ctx, kbID, "", effort)
}

// answerTools are the answer agent's tools: kb_search over the turn's
// retriever (production pipeline, numbered into the turn's sources) and
// memory_read/memory_write when session memory is on for the KB — only
// tools whose policy needs no approval (Ruling P2-R15).
//
// Why no approval-gated tools (web_search, remote/unknown tools): in adk-go
// v2.5.0 a tool confirmation raised inside a workflow AgentNode cannot be
// resumed. workflowagent's resume detection only handles adk_request_input
// (the dead-end pause); an adk_request_confirmation reply falls through to
// a fresh workflow run from Start, so the approved call never runs. Web
// search stays reachable through the dead-end "web" action.
func (h *AgentChatHandler) answerTools(ctx context.Context, kbID string, reader SiteConfigReader, retriever *AgentRetriever) []tool.Tool {
	out := []tool.Tool{adkbridge.NewTool(adkbridge.ToolSpec{
		Name:        "kb_search",
		Description: "Search this knowledge base again, e.g. for a follow-up aspect of the question. Results are numbered like the context; cite them as [n].",
		InputSchema: json.RawMessage(agentKBSearchSchema),
		Policy:      adkbridge.PolicyFor("kb_search"),
	}, retriever.Dispatch)}
	var names []string
	if h.d.SessionMemory != nil && ChatSessionMemoryEnabled(ctx, reader) {
		for _, n := range []string{"memory_read", "memory_write"} {
			if _, ok := registryHas(h.d.Registry, n); ok {
				names = append(names, n)
			}
		}
	}
	if len(names) > 0 {
		more, err := adkbridge.RegistryTools(h.d.Registry, kbID, names)
		if err != nil {
			logctx.From(ctx).Warn("agentchat: registry tools", "tools", names, "error", err)
		} else {
			out = append(out, more...)
		}
	}
	return approvalFreeTools(out)
}

// approvalFreeTools drops every tool whose policy needs approval (see
// answerTools for why). The single gate for the answer agent's tools.
func approvalFreeTools(tools []tool.Tool) []tool.Tool {
	out := tools[:0:0]
	for _, t := range tools {
		if adkbridge.PolicyFor(t.Name()).Approval == adkbridge.ApprovalNever {
			out = append(out, t)
		}
	}
	return out
}

// agentChatProps are the forwardedProps the agent chat reads.
type agentChatProps struct {
	reasoning string // low | medium | high; "" = none
	language  string // a supported language, default otherwise
}

// agentChatPropsOf reads forwardedProps. Anything malformed or unknown
// falls back to the defaults.
func agentChatPropsOf(in *types.RunAgentInput) agentChatProps {
	out := agentChatProps{language: defaultLanguage}
	fp, _ := in.ForwardedProps.(map[string]any)
	switch v, _ := fp["reasoning"].(string); v {
	case "low", "medium", "high":
		out.reasoning = v
	}
	if v, _ := fp["language"].(string); supportedLanguages[v] {
		out.language = v
	}
	return out
}

// contentTextOf joins the text parts of a message (as agui does for the
// user text it hands TurnStarted).
func contentTextOf(c *genai.Content) string {
	var b strings.Builder
	for _, p := range c.Parts {
		if p != nil {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func writeAgentChatError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
