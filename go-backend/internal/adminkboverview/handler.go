package adminkboverview

import (
	"net/http"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

// Handler exposes the KB overview over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler creates a Handler backed by svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Overview handles GET /api/admin/kb-overview.
func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	resp, err := h.svc.Overview(r.Context())
	if err != nil {
		logctx.From(r.Context()).Error("adminkboverview.overview", "error", err)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, resp)
}
