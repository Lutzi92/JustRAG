// Package kggraph exposes the knowledge-graph overview (the frontend "mind
// map") over HTTP. It is a thin read endpoint on top of kg.PgStore; the graph
// data is populated by the AP-C1 ingestion stage when KG extraction is enabled,
// so on KBs without extraction it simply returns empty nodes/edges.
package kggraph

import (
	"context"
	"net/http"
	"strconv"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kg"
)

// Store is the narrow read surface this handler needs. Satisfied by
// *kg.PgStore; tests inject a fake. Kept local (not added to kg.Store) so the
// many kg.Store fakes across the codebase don't have to grow a method they
// don't use.
type Store interface {
	GraphOverview(ctx context.Context, kbID string, maxNodes int) (kg.GraphOverview, error)
}

// Handler serves the KG overview endpoint.
type Handler struct {
	store Store
}

// NewHandler creates a Handler backed by store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// GetGraph handles GET /api/kb/{id}/graph[?maxNodes=N]. KB-view authorization
// is enforced by the surrounding middleware chain; the KB id is read from the
// kbaccess context (preferred) or the URL path.
func (h *Handler) GetGraph(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")
	if access := kbaccess.AccessFromContext(ctx); access != nil && access.KB != nil {
		kbID = access.KB.ID
	}

	maxNodes := 0 // 0 ⇒ store applies its default/clamp
	if v := r.URL.Query().Get("maxNodes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxNodes = n
		}
	}

	graph, err := h.store.GraphOverview(ctx, kbID, maxNodes)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to load knowledge graph")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, graph)
}
