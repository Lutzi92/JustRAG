package agentteams

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/siteconfig"
)

const (
	maxNameLen        = 100
	maxDescriptionLen = 1000
	maxIconLen        = 64
	maxPromptLen      = 8000
	maxTeamMembers    = 8
	maxAgentsPerTurn  = 5
)

// handlerStore is the persistence surface the handler needs (satisfied by *Store).
type handlerStore interface {
	CreateAgent(ctx context.Context, a AgentRecord) (*AgentRecord, error)
	ListAgentsByUser(ctx context.Context, userID string) ([]AgentRecord, error)
	GetAgent(ctx context.Context, id, userID string) (*AgentRecord, error)
	UpdateAgent(ctx context.Context, a AgentRecord) (*AgentRecord, error)
	DeleteAgent(ctx context.Context, id, userID string) (bool, error)
	CountOwnedAgents(ctx context.Context, userID string, ids []string) (int, error)
	CreateTeam(ctx context.Context, t TeamRecord) (*TeamRecord, error)
	ListTeamsByUser(ctx context.Context, userID string) ([]TeamRecord, error)
	GetTeam(ctx context.Context, id, userID string) (*TeamRecord, error)
	UpdateTeam(ctx context.Context, t TeamRecord) (*TeamRecord, error)
	DeleteTeam(ctx context.Context, id, userID string) (bool, error)
	AttachAgent(ctx context.Context, kbID, agentID string, isDefault bool) error
	DetachAgent(ctx context.Context, kbID, agentID string) (bool, error)
	AttachTeam(ctx context.Context, kbID, teamID string, isDefault bool) error
	DetachTeam(ctx context.Context, kbID, teamID string) (bool, error)
	ListAttachedForKB(ctx context.Context, kbID string) (*KBAgents, error)
}

// HandlerDeps carries the validation callbacks app wiring injects.
type HandlerDeps struct {
	// AvailableTools returns the set of tool names a user agent may select:
	// the MCP registry's global catalog minus privileged tools unless
	// agents_allow_privileged_tools is on. Re-evaluated per request so admin
	// changes apply without restart.
	AvailableTools func(ctx context.Context) (map[string]bool, error)
	// ModelExists reports whether a chat_model value is a configured model.
	ModelExists func(ctx context.Context, name string) (bool, error)
}

// Handler serves the agents / teams / KB-attachment endpoints.
type Handler struct {
	store handlerStore
	deps  HandlerDeps
}

// NewHandler constructs a Handler.
func NewHandler(s handlerStore, deps HandlerDeps) *Handler {
	return &Handler{store: s, deps: deps}
}

func requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	u := auth.UserFromContext(r.Context())
	if u == nil || u.ID == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return "", false
	}
	return u.ID, true
}

// agentUpsertRequest is the parsed body for agent create/update.
type agentUpsertRequest struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Icon         string            `json:"icon"`
	SystemPrompt string            `json:"systemPrompt"`
	ChatModel    string            `json:"chatModel"`
	ToolNames    []string          `json:"toolNames"`
	Config       map[string]string `json:"config"`
	IsEnabled    *bool             `json:"isEnabled"`
}

func (h *Handler) validateAgent(ctx context.Context, req *agentUpsertRequest) string {
	req.Name = strings.TrimSpace(req.Name)
	switch {
	case req.Name == "" || len(req.Name) > maxNameLen:
		return "name is required (max 100 chars)"
	case len(req.Description) > maxDescriptionLen:
		return "description too long (max 1000 chars)"
	case len(req.Icon) > maxIconLen:
		return "icon too long"
	case len(req.SystemPrompt) > maxPromptLen:
		return "systemPrompt too long (max 8000 chars)"
	}
	if err := siteconfig.ValidateAgentConfig(req.Config); err != nil {
		return httputil.SanitizeError(err)
	}
	if len(req.ToolNames) > 0 {
		allowed, err := h.deps.AvailableTools(ctx)
		if err != nil {
			logctx.From(ctx).Error("agentteams.available_tools", "error", err)
			return "tool catalog unavailable"
		}
		for _, name := range req.ToolNames {
			if !allowed[name] {
				return "tool not available for user agents: " + name
			}
		}
	}
	if req.ChatModel != "" {
		ok, err := h.deps.ModelExists(ctx, req.ChatModel)
		if err != nil {
			logctx.From(ctx).Error("agentteams.model_exists", "error", err)
			return "model validation unavailable"
		}
		if !ok {
			return "chatModel is not a configured model: " + req.ChatModel
		}
	}
	return ""
}

func (h *Handler) decodeAgent(w http.ResponseWriter, r *http.Request) (*agentUpsertRequest, bool) {
	var req agentUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON: "+httputil.SanitizeError(err))
		return nil, false
	}
	if req.ToolNames == nil {
		req.ToolNames = []string{}
	}
	if req.Config == nil {
		req.Config = map[string]string{}
	}
	if msg := h.validateAgent(r.Context(), &req); msg != "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, msg)
		return nil, false
	}
	return &req, true
}

func (r *agentUpsertRequest) toRecord(id, userID string) AgentRecord {
	enabled := true
	if r.IsEnabled != nil {
		enabled = *r.IsEnabled
	}
	return AgentRecord{
		ID: id, UserID: userID, Name: r.Name, Description: r.Description,
		Icon: r.Icon, SystemPrompt: r.SystemPrompt, ChatModel: r.ChatModel,
		ToolNames: r.ToolNames, Config: r.Config, IsEnabled: enabled,
	}
}

// CreateAgent handles POST /api/agents.
func (h *Handler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	req, ok := h.decodeAgent(w, r)
	if !ok {
		return
	}
	created, err := h.store.CreateAgent(r.Context(), req.toRecord("", userID))
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, created)
}

// ListAgents handles GET /api/agents.
func (h *Handler) ListAgents(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	list, err := h.store.ListAgentsByUser(r.Context(), userID)
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	if list == nil {
		list = []AgentRecord{}
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, list)
}

// GetAgent handles GET /api/agents/{id}.
func (h *Handler) GetAgent(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	rec, err := h.store.GetAgent(r.Context(), r.PathValue("id"), userID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "agent not found")
		return
	}
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, rec)
}

// UpdateAgent handles PUT /api/agents/{id}.
func (h *Handler) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	req, ok := h.decodeAgent(w, r)
	if !ok {
		return
	}
	rec, err := h.store.UpdateAgent(r.Context(), req.toRecord(r.PathValue("id"), userID))
	if errors.Is(err, ErrNotFound) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "agent not found")
		return
	}
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, rec)
}

// DeleteAgent handles DELETE /api/agents/{id}.
func (h *Handler) DeleteAgent(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	deleted, err := h.store.DeleteAgent(r.Context(), r.PathValue("id"), userID)
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	if !deleted {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "agent not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// registryResponse is the agent-form bootstrap payload: the per-agent config
// field registry plus the tool names a user agent may currently select
// (registry catalog minus privileged tools unless the admin flag is on —
// same AvailableTools closure that validates saves, so the form can never
// drift from what the backend accepts).
type registryResponse struct {
	Fields []siteconfig.KBConfigField `json:"fields"`
	Tools  []string                   `json:"tools"`
}

// GetRegistry handles GET /api/agents/registry.
func (h *Handler) GetRegistry(w http.ResponseWriter, r *http.Request) {
	allowed, err := h.deps.AvailableTools(r.Context())
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	tools := make([]string, 0, len(allowed))
	for name := range allowed {
		tools = append(tools, name)
	}
	sort.Strings(tools)
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, registryResponse{
		Fields: siteconfig.AgentFields(),
		Tools:  tools,
	})
}

// teamUpsertRequest is the parsed body for team create/update.
type teamUpsertRequest struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Icon             string   `json:"icon"`
	MaxAgentsPerTurn int      `json:"maxAgentsPerTurn"`
	MemberIDs        []string `json:"memberIds"`
	IsEnabled        *bool    `json:"isEnabled"`
}

func (h *Handler) decodeTeam(w http.ResponseWriter, r *http.Request, userID string) (*teamUpsertRequest, bool) {
	var req teamUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON: "+httputil.SanitizeError(err))
		return nil, false
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.MemberIDs == nil {
		req.MemberIDs = []string{}
	}
	if req.MaxAgentsPerTurn <= 0 {
		req.MaxAgentsPerTurn = 3
	}
	var msg string
	switch {
	case req.Name == "" || len(req.Name) > maxNameLen:
		msg = "name is required (max 100 chars)"
	case len(req.Description) > maxDescriptionLen:
		msg = "description too long (max 1000 chars)"
	case len(req.Icon) > maxIconLen:
		msg = "icon too long"
	case len(req.MemberIDs) > maxTeamMembers:
		msg = "too many members (max 8)"
	case req.MaxAgentsPerTurn > maxAgentsPerTurn:
		msg = "maxAgentsPerTurn too large (max 5)"
	}
	if msg == "" && len(req.MemberIDs) > 0 {
		n, err := h.store.CountOwnedAgents(r.Context(), userID, req.MemberIDs)
		if err != nil {
			httputil.WriteInternalErrorCtx(r.Context(), w, err)
			return nil, false
		}
		if n != len(req.MemberIDs) {
			msg = "memberIds must all be your own agents"
		}
	}
	if msg != "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, msg)
		return nil, false
	}
	return &req, true
}

func (r *teamUpsertRequest) toRecord(id, userID string) TeamRecord {
	enabled := true
	if r.IsEnabled != nil {
		enabled = *r.IsEnabled
	}
	return TeamRecord{
		ID: id, UserID: userID, Name: r.Name, Description: r.Description,
		Icon: r.Icon, MaxAgentsPerTurn: r.MaxAgentsPerTurn,
		MemberIDs: r.MemberIDs, IsEnabled: enabled,
	}
}

// CreateTeam handles POST /api/agent-teams.
func (h *Handler) CreateTeam(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	req, ok := h.decodeTeam(w, r, userID)
	if !ok {
		return
	}
	created, err := h.store.CreateTeam(r.Context(), req.toRecord("", userID))
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, created)
}

// ListTeams handles GET /api/agent-teams.
func (h *Handler) ListTeams(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	list, err := h.store.ListTeamsByUser(r.Context(), userID)
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	if list == nil {
		list = []TeamRecord{}
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, list)
}

// GetTeam handles GET /api/agent-teams/{id}.
func (h *Handler) GetTeam(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	rec, err := h.store.GetTeam(r.Context(), r.PathValue("id"), userID)
	if errors.Is(err, ErrNotFound) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "team not found")
		return
	}
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, rec)
}

// UpdateTeam handles PUT /api/agent-teams/{id}.
func (h *Handler) UpdateTeam(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	req, ok := h.decodeTeam(w, r, userID)
	if !ok {
		return
	}
	rec, err := h.store.UpdateTeam(r.Context(), req.toRecord(r.PathValue("id"), userID))
	if errors.Is(err, ErrNotFound) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "team not found")
		return
	}
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, rec)
}

// DeleteTeam handles DELETE /api/agent-teams/{id}.
func (h *Handler) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	deleted, err := h.store.DeleteTeam(r.Context(), r.PathValue("id"), userID)
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	if !deleted {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "team not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// KB attachment endpoints. Route-level ACL: ListKBAgents behind kbViewChain,
// attach/detach behind kbEditChain — this handler adds ownership of the
// agent/team itself (only the owner may attach their agent to a KB they edit).
// ---------------------------------------------------------------------------

// ListKBAgents handles GET /api/kb/{id}/agents (picker payload).
func (h *Handler) ListKBAgents(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.ListAttachedForKB(r.Context(), r.PathValue("id"))
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, out)
}

type attachRequest struct {
	IsDefault bool `json:"isDefault"`
}

// AttachAgent handles PUT /api/kb/{id}/agents/{agentId}.
func (h *Handler) AttachAgent(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("agentId")
	if _, err := h.store.GetAgent(r.Context(), agentID, userID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "agent not found")
			return
		}
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	var req attachRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // empty body = {isDefault:false}
	if err := h.store.AttachAgent(r.Context(), r.PathValue("id"), agentID, req.IsDefault); err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]bool{"success": true})
}

// DetachAgent handles DELETE /api/kb/{id}/agents/{agentId}.
func (h *Handler) DetachAgent(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}
	if _, err := h.store.DetachAgent(r.Context(), r.PathValue("id"), r.PathValue("agentId")); err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AttachTeam handles PUT /api/kb/{id}/teams/{teamId}.
func (h *Handler) AttachTeam(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUser(w, r)
	if !ok {
		return
	}
	teamID := r.PathValue("teamId")
	if _, err := h.store.GetTeam(r.Context(), teamID, userID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "team not found")
			return
		}
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	var req attachRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := h.store.AttachTeam(r.Context(), r.PathValue("id"), teamID, req.IsDefault); err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]bool{"success": true})
}

// DetachTeam handles DELETE /api/kb/{id}/teams/{teamId}.
func (h *Handler) DetachTeam(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}
	if _, err := h.store.DetachTeam(r.Context(), r.PathValue("id"), r.PathValue("teamId")); err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
