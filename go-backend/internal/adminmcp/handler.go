// Package adminmcp serves the admin UI's MCP-server management endpoints.
// The Phase 2 §2.1 MCP integration ships built-in tools (kb_search,
// web_search, memory_*) plus an admin-configurable list of remote MCP
// servers (the `mcp_servers` site_config JSON). This package exposes:
//
//	GET  /api/admin/mcp/status   — returns the registry's built-in tools
//	                               + the result of a live `tools/list`
//	                               call against every configured remote
//	                               server (reachability + advertised
//	                               tools per server).
//	POST /api/admin/mcp/reload   — re-reads `mcp_servers` from site_config,
//	                               calls Registry.ConfigureGlobal on the
//	                               shared registry, and returns the
//	                               same status payload.
//
// Save flows through the existing site_config endpoint — operators
// edit `mcp_servers` like any other site_config key, then call reload
// here to apply the change without restarting the server.
package adminmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/mcp"
)

// SiteConfigReader is the read-only surface needed to look up the
// `mcp_servers` JSON. Production callers pass the chat.PGStore; tests
// inject a fake.
type SiteConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// Handler exposes the MCP admin endpoints. The shared *mcp.Registry is
// the same instance the chat handler dispatches against, so a reload
// here takes effect on the next chat request without a server restart.
type Handler struct {
	registry *mcp.Registry
	cfg      SiteConfigReader
}

// NewHandler constructs the admin handler.
func NewHandler(reg *mcp.Registry, cfg SiteConfigReader) *Handler {
	return &Handler{registry: reg, cfg: cfg}
}

// StatusResponse is the shape returned by both GET /status and POST
// /reload — the reload variant guarantees the data is freshly probed,
// the status variant returns cached state from the last reload.
type StatusResponse struct {
	UseMCPTools bool               `json:"use_mcp_tools"`
	Builtins    []mcp.ToolSummary  `json:"builtins"`
	Servers     []mcp.ServerStatus `json:"servers"`
	ServerSpec  []mcp.ServerSpec   `json:"server_spec"`
}

// GetStatus serves GET /api/admin/mcp/status. It returns the current
// registry contents (built-ins + last-configured remote servers' health
// + tool lists). Network probing happens here — the response time is
// bounded by `mcp.dialTimeout` (10s) per server.
func (h *Handler) GetStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := h.buildStatus(ctx)
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, resp)
}

// PostReload serves POST /api/admin/mcp/reload. Reads `mcp_servers`
// from site_config, reconfigures the shared registry, and returns the
// fresh status. Failures during JSON parsing surface as 4xx; per-server
// connect failures are NOT errors here — they are reported on the
// per-server `reachable=false` flag in the response.
func (h *Handler) PostReload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	specs, err := h.loadSpecs(ctx)
	if err != nil {
		logctx.From(ctx).Warn("admin.mcp.reload: parse mcp_servers failed", "error", err)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "mcp_servers is not a valid JSON array of {name,url,headers}: "+err.Error())
		return
	}
	h.registry.ConfigureGlobal(ctx, specs)
	resp := h.buildStatus(ctx)
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, resp)
}

// buildStatus is the shared status-builder used by both GET and POST.
// It does NOT re-probe — callers that want fresh probes call GetStatus
// (which probes via Registry.GlobalStatus) directly.
func (h *Handler) buildStatus(ctx context.Context) StatusResponse {
	builtins, servers := h.registry.GlobalStatus(ctx)
	useMCP := false
	if v, err := h.cfg.GetSiteConfigValue(ctx, "chat_use_mcp_tools"); err == nil && v != nil {
		useMCP = *v == "true" || *v == "1"
	}
	return StatusResponse{
		UseMCPTools: useMCP,
		Builtins:    builtins,
		Servers:     servers,
		ServerSpec:  h.registry.GlobalSpec(),
	}
}

// loadSpecs reads the `mcp_servers` site_config and parses it as a
// JSON array of ServerSpec. Returns an empty slice (and no error) when
// the key is absent or empty — that's "no servers configured", not an
// error state.
func (h *Handler) loadSpecs(ctx context.Context) ([]mcp.ServerSpec, error) {
	v, err := h.cfg.GetSiteConfigValue(ctx, "mcp_servers")
	if err != nil {
		return nil, fmt.Errorf("read site_config: %w", err)
	}
	if v == nil || *v == "" {
		return nil, nil
	}
	var specs []mcp.ServerSpec
	if err := json.Unmarshal([]byte(*v), &specs); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return specs, nil
}

// LoadAndApplyAtStartup is the helper app/routes.go calls during boot
// so the registry is populated from site_config before the first chat
// request arrives. Per-server connect failures are logged at WARN; the
// server still starts.
func LoadAndApplyAtStartup(ctx context.Context, reg *mcp.Registry, cfg SiteConfigReader) {
	h := &Handler{registry: reg, cfg: cfg}
	specs, err := h.loadSpecs(ctx)
	if err != nil {
		logctx.From(ctx).Warn("mcp.startup: invalid mcp_servers; skipping load", "error", err)
		return
	}
	if len(specs) == 0 {
		return
	}
	reg.ConfigureGlobal(ctx, specs)
}
