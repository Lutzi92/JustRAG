// Package kggraph exposes the knowledge-graph overview (the frontend "mind
// map") over HTTP: a one-shot read (GET /graph) plus a live update stream
// (GET /graph/stream) that pushes graph_changed / status events while the KB
// is being ingested or edited.
package kggraph

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kg"
	"github.com/justrag/go-backend/internal/kgevents"
	"github.com/justrag/go-backend/internal/sserelay"
)

// Store is the narrow read surface this handler needs. Satisfied by
// *kg.PgStore; tests inject a fake.
type Store interface {
	GraphOverview(ctx context.Context, kbID string, maxNodes int) (kg.GraphOverview, error)
	KBHasActiveIngestion(ctx context.Context, kbID string) (bool, error)
}

// Handler serves the KG overview + live update endpoints.
type Handler struct {
	store Store
	relay *sserelay.Relay
}

// NewHandler creates a Handler backed by store. Call SetRelay to enable the
// /graph/stream endpoint (without it, StreamGraph returns 503).
func NewHandler(store Store) *Handler { return &Handler{store: store} }

// SetRelay injects the shared SSE relay used by StreamGraph. Optional.
func (h *Handler) SetRelay(r *sserelay.Relay) { h.relay = r }

// graphResponse is the GET /graph payload: the graph plus a "processing" flag
// the mindmap uses to show its spinner on initial load (and on every
// re-fetch). Embedding flattens nodes/edges alongside processing.
type graphResponse struct {
	kg.GraphOverview
	Processing bool `json:"processing"`
}

func kbIDFrom(ctx context.Context, r *http.Request) string {
	if access := kbaccess.AccessFromContext(ctx); access != nil && access.KB != nil {
		return access.KB.ID
	}
	return r.PathValue("id")
}

// GetGraph handles GET /api/kb/{id}/graph[?maxNodes=N].
func (h *Handler) GetGraph(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := kbIDFrom(ctx, r)

	maxNodes := 0
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
	// Best-effort processing flag — a query failure should not fail the graph.
	processing, _ := h.store.KBHasActiveIngestion(ctx, kbID)
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, graphResponse{GraphOverview: graph, Processing: processing})
}

// StreamGraph handles GET /api/kb/{id}/graph/stream — a passive SSE
// subscription to the KB's mindmap update channel. It enqueues no job and
// never sends a terminator; it ends when the client disconnects. The relay's
// heartbeat keeps the connection alive and detects a dead client on the next
// heartbeat write.
func (h *Handler) StreamGraph(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.relay == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusServiceUnavailable, "graph stream unavailable")
		return
	}
	kbID := kbIDFrom(ctx, r)
	_ = h.relay.Run(ctx, w, sserelay.Options{
		Channel: kgevents.Channel(kbID),
		// AbortKey is unused (no worker to abort) but the relay sets it on
		// disconnect; a per-KB key keeps it harmless.
		AbortKey:    "kg:abort:" + kbID,
		SkipEnqueue: true,
		// Effectively disable the inactivity cutoff: an idle-but-open mindmap
		// is normal. Heartbeat-write failure (client gone) ends the relay.
		InactivityTimeout: 24 * time.Hour,
	})
}
