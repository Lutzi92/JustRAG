package adminkboverview

import (
	"net/http"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

// Handler exposes the KB overview and the superadmin KB actions over HTTP.
type Handler struct {
	svc     *Service
	store   ActionStore
	deleter CascadeDeleter
}

// NewHandler creates a Handler that serves only the read-only Overview
// endpoint; its store and deleter are left as nil interfaces, so the
// mutating handlers (DeleteKB, TransferOwner) are not safe to call on it —
// use NewHandlerWithActions when those endpoints are needed.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// NewHandlerWithActions creates a Handler that also serves the superadmin
// delete and owner-transfer endpoints.
func NewHandlerWithActions(svc *Service, store ActionStore, deleter CascadeDeleter) *Handler {
	return &Handler{svc: svc, store: store, deleter: deleter}
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
