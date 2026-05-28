package kb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/store"
)

// ShareRow holds the data for a single KB share entry.
type ShareRow struct {
	ID         string    `json:"id" db:"id"`
	UserID     string    `json:"userId" db:"user_id"`
	Username   string    `json:"username" db:"username"`
	Permission string    `json:"permission" db:"permission"`
	CreatedAt  time.Time `json:"createdAt" db:"created_at"`
}

// ShareStore is the data-access interface for KB sharing operations.
type ShareStore interface {
	ListKBShares(ctx context.Context, kbID string) ([]ShareRow, error)
	AddKBShare(ctx context.Context, kbID, userID, permission string) (*ShareRow, error)
	RemoveKBShare(ctx context.Context, kbID, userID string) error
}

// SharingHandler holds the dependencies for KB sharing endpoints.
type SharingHandler struct {
	store ShareStore
}

// NewSharingHandler creates a SharingHandler using the given ShareStore.
func NewSharingHandler(store ShareStore) *SharingHandler {
	return &SharingHandler{store: store}
}

// addShareRequest is the expected JSON body for POST /api/kb/{id}/share.
type addShareRequest struct {
	UserID     string `json:"userId"`
	Permission string `json:"permission"`
}

// isOwnerOrSuperadmin returns true when the current user is the KB owner or has the superadmin role.
func isOwnerOrSuperadmin(r *http.Request) bool {
	access := kbaccess.AccessFromContext(r.Context())
	user := auth.UserFromContext(r.Context())
	if user != nil && user.Role == "superadmin" {
		return true
	}
	if access != nil && access.IsOwner {
		return true
	}
	return false
}

// ListShares handles GET /api/kb/{id}/shares.
// Only KB owners and superadmins can see the share list.
func (h *SharingHandler) ListShares(w http.ResponseWriter, r *http.Request) {
	if !isOwnerOrSuperadmin(r) {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "only the KB owner or a superadmin can view shares"})
		return
	}

	kbID := r.PathValue("id")

	shares, err := h.store.ListKBShares(r.Context(), kbID)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to list shares"})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, shares)
}

// AddShare handles POST /api/kb/{id}/share.
// Only KB owners and superadmins may add shares.
// Returns 201 on success, 400 for invalid input, 403 for insufficient permissions.
func (h *SharingHandler) AddShare(w http.ResponseWriter, r *http.Request) {
	if !isOwnerOrSuperadmin(r) {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "only the KB owner or a superadmin can share this knowledge base"})
		return
	}

	var body addShareRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if body.UserID == "" {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "userId is required"})
		return
	}
	if body.Permission != "view" && body.Permission != "edit" {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "permission must be \"view\" or \"edit\""})
		return
	}

	kbID := r.PathValue("id")
	share, err := h.store.AddKBShare(r.Context(), kbID, body.UserID, body.Permission)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to add share"})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, share)
}

// RemoveShare handles DELETE /api/kb/{id}/share/{userId}.
// Only KB owners and superadmins may remove shares.
// Returns 204 on success, 403 for insufficient permissions, 404 if the share does not exist.
func (h *SharingHandler) RemoveShare(w http.ResponseWriter, r *http.Request) {
	if !isOwnerOrSuperadmin(r) {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "only the KB owner or a superadmin can remove shares"})
		return
	}

	kbID := r.PathValue("id")
	userID := r.PathValue("userId")

	err := h.store.RemoveKBShare(r.Context(), kbID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusNotFound, map[string]string{"error": "share not found"})
			return
		}
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to remove share"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
