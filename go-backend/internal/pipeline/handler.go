package pipeline

import (
	"context"
	"net/http"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/siteconfig"
)

// store loads a KB's per-KB site_config overrides. Satisfied by
// *kbconfig.Store (same surface internal/kbconfig uses).
type store interface {
	ListKBOverrides(ctx context.Context, kbID string) (map[string]*string, error)
}

// globalReader fetches global site_config values, in batch and per-key.
// Satisfied by *chat.PGStore. The per-key method is required so h.global can
// also serve as siteconfig.NewKBOverlay's base reader below — BatchReader
// alone (just GetSiteConfigValues) does not satisfy that interface.
type globalReader interface {
	siteconfig.BatchReader
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// Handler serves the read-only workflow projection.
type Handler struct {
	store  store
	global globalReader
}

// NewHandler constructs the workflow Handler.
func NewHandler(s store, g globalReader) *Handler { return &Handler{store: s, global: g} }

// GetWorkflow handles GET /api/kb/{id}/workflow?lane=…
//
// Mounted on kbAdminChain (KB role admin). Read-only: node edits go through the
// existing PUT /api/kb/{id}/settings so validation stays in one place.
func (h *Handler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")

	lane := LaneComplex
	if raw := r.URL.Query().Get("lane"); raw != "" {
		lane = Lane(raw)
		if _, ok := lane.queryType(); !ok {
			httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "unbekannter lane-Parameter")
			return
		}
	}

	overrides, err := h.store.ListKBOverrides(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Error("pipeline.workflow.list_overrides", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	overlay := siteconfig.NewKBOverlay(h.global, overrides)

	g, err := Project(ctx, overlay, h.global, lane)
	if err != nil {
		logctx.From(ctx).Error("pipeline.workflow.project", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, g)
}

// ListPresets handles GET /api/workflow/presets.
//
// Mounted authenticated-only, WITHOUT a KB in the path: presets are curated,
// deployment-wide starting points (internal/pipeline/presets.go), not a
// per-KB resource, so kbAdminChain's RequireKBRole gate does not apply here —
// there is no {id} to resolve a role against. Follows the same
// "authenticated, no KB" pattern as GET /api/kb/catalog and GET
// /api/kb-categories in routes.go (rc.authMw.Authenticate directly, no
// kbaccess middleware).
func (h *Handler) ListPresets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	priced, err := PricePresets(ctx, h.global)
	if err != nil {
		logctx.From(ctx).Error("pipeline.workflow.list_presets", "error", err)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, priced)
}
