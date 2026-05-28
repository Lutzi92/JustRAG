package kb

import (
	"context"
	"net/http"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// CascadeDeleter is the interface used by DeleteHandler to remove a KB and all
// its associated assets (vector chunks, storage files, and database rows).
type CascadeDeleter interface {
	DeleteKB(ctx context.Context, kbID string) error
}

// DeleteHandler handles DELETE /api/kb/{id}.
type DeleteHandler struct {
	deleter CascadeDeleter
}

// NewDeleteHandler creates a DeleteHandler backed by the given CascadeDeleter.
func NewDeleteHandler(deleter CascadeDeleter) *DeleteHandler {
	return &DeleteHandler{deleter: deleter}
}

// DeleteKB handles DELETE /api/kb/{id}.
//
// Permission rules (enforced by the kbaccess middleware upstream):
//   - The middleware already resolved view access. Here we additionally verify
//     that the caller is the owner or a superadmin — only they may delete.
//
// Flow:
//  1. Confirm owner or superadmin.
//  2. Delegate cascade deletion to the CascadeDeleter service.
//  3. Return 204 No Content.
func (h *DeleteHandler) DeleteKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	access := kbaccess.AccessFromContext(ctx)
	if access == nil || access.KB == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "KB access context missing")
		return
	}

	// Only the owner or a superadmin may delete a KB.
	if !access.IsOwner && user.Role != "superadmin" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "only the owner or a superadmin may delete this knowledge base")
		return
	}

	kbID := access.KB.ID

	if err := h.deleter.DeleteKB(ctx, kbID); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to delete knowledge base")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
