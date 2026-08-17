package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/usage"
)

const protocolVersion = "2025-06-18"

// ConfigReader reads site_config values. Structurally satisfied by the
// chat store used elsewhere (GetSiteConfigValue).
type ConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// Handler serves the MCP server JSON-RPC verbs for a single KB (the KB is
// taken per-request from the URL path).
type Handler struct {
	answerer Answerer
	cfg      ConfigReader

	// usageRecorder writes one usage_events row per accepted turn. Optional.
	usageRecorder usage.Recorder
}

func NewHandler(answerer Answerer, cfg ConfigReader) *Handler {
	return &Handler{answerer: answerer, cfg: cfg}
}

// SetUsageRecorder injects the usage ledger. Optional — when unset, turns on
// this surface are not counted.
func (h *Handler) SetUsageRecorder(r usage.Recorder) {
	h.usageRecorder = r
}

func (h *Handler) enabled(ctx context.Context) bool {
	v, err := h.cfg.GetSiteConfigValue(ctx, "mcp_server_enabled")
	return err == nil && v != nil && *v == "true"
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !h.enabled(ctx) {
		http.NotFound(w, r)
		return
	}
	kbID := r.PathValue("id")

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		writeRPCError(w, nil, codeParse, "failed to read request body")
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCError(w, nil, codeParse, "invalid JSON")
		return
	}

	switch req.Method {
	case "initialize":
		writeResult(w, req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "justrag-kb", "version": "1.0.0"},
		})
	case "tools/list":
		writeResult(w, req.ID, map[string]any{
			"tools": []toolDescriptor{askKBDescriptor()},
		})
	case "tools/call":
		h.handleToolsCall(ctx, w, kbID, req)
	default:
		writeRPCError(w, req.ID, codeMethodNotFound, "unknown method: "+req.Method)
	}
}

func (h *Handler) handleToolsCall(ctx context.Context, w http.ResponseWriter, kbID string, req rpcRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeRPCError(w, req.ID, codeInvalidParams, "invalid params")
			return
		}
	}
	if params.Name == "" {
		writeRPCError(w, req.ID, codeInvalidParams, "missing tool name")
		return
	}
	if params.Name != askKBToolName {
		writeRPCError(w, req.ID, codeMethodNotFound, "unknown tool: "+params.Name)
		return
	}

	// Usage ledger (internal/usage). Only tools/call is a turn; initialize and
	// tools/list are handshakes and are deliberately not counted.
	if h.usageRecorder != nil {
		user := auth.UserFromContext(ctx)
		userID := ""
		if user != nil {
			userID = user.ID
		}
		h.usageRecorder.Record(ctx, usage.Event{
			KbID:     kbID,
			UserID:   userID,
			APIKeyID: auth.APIKeyIDFromContext(ctx),
			Surface:  usage.SurfaceMCP,
		})
	}

	res, err := runAskKB(ctx, h.answerer, kbID, params.Arguments)
	if err != nil {
		writeRPCError(w, req.ID, codeInvalidParams, err.Error())
		return
	}
	writeResult(w, req.ID, res)
}
