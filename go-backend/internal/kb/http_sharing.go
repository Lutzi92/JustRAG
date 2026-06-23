package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

// PendingInviteRow is an invite for a username that does not yet exist as a user.
type PendingInviteRow struct {
	Username   string    `json:"username" db:"username"`
	Permission string    `json:"permission" db:"permission"`
	CreatedAt  time.Time `json:"createdAt" db:"created_at"`
}

// ShareStore is the data-access interface for KB sharing operations.
type ShareStore interface {
	ListKBShares(ctx context.Context, kbID string) ([]ShareRow, error)
	AddKBShare(ctx context.Context, kbID, userID, permission string) (*ShareRow, error)
	RemoveKBShare(ctx context.Context, kbID, userID string) error

	// Bulk-invite support.
	GetUserIDByUsername(ctx context.Context, username string) (userID string, found bool, err error)
	ShareExists(ctx context.Context, kbID, userID string) (bool, error)
	UpsertPendingInvite(ctx context.Context, kbID, username, permission, invitedBy string) error
	ListPendingInvites(ctx context.Context, kbID string) ([]PendingInviteRow, error)
	RemovePendingInvite(ctx context.Context, kbID, username string) error
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

// MaxBulkUsernames caps a single bulk-invite request to bound payload + DB work.
const MaxBulkUsernames = 500

// bulkInviteRequest is the body for POST /api/kb/{id}/share/bulk.
type bulkInviteRequest struct {
	Usernames  []string `json:"usernames"`
	Permission string   `json:"permission"`
}

// bulkInviteResult is the per-username categorization returned to the UI.
type bulkInviteResult struct {
	Shared           []string `json:"shared"`
	Pending          []string `json:"pending"`
	AlreadyHadAccess []string `json:"alreadyHadAccess"`
}

// sharesResponse is the GET /api/kb/{id}/shares body: real shares plus
// not-yet-applied pending invites, so the modal renders both in one fetch.
type sharesResponse struct {
	Shares  []ShareRow         `json:"shares"`
	Pending []PendingInviteRow `json:"pending"`
}

// normalizeUsernames trims, lowercases, drops empties, and dedupes (preserving
// first-seen order).
func normalizeUsernames(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		u := strings.ToLower(strings.TrimSpace(raw))
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
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
	pending, err := h.store.ListPendingInvites(r.Context(), kbID)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to list pending invites"})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, sharesResponse{Shares: shares, Pending: pending})
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

// BulkInvite handles POST /api/kb/{id}/share/bulk. For each pasted username:
// existing user -> knowledge_base_shares (shared, or alreadyHadAccess if a share
// already existed); unknown user -> pending_kb_invites (pending), applied on
// their first OIDC login. Only KB owners and superadmins may bulk-invite.
func (h *SharingHandler) BulkInvite(w http.ResponseWriter, r *http.Request) {
	if !isOwnerOrSuperadmin(r) {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "only the KB owner or a superadmin can share this knowledge base"})
		return
	}

	var body bulkInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Permission != "view" && body.Permission != "edit" {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "permission must be \"view\" or \"edit\""})
		return
	}

	usernames := normalizeUsernames(body.Usernames)
	if len(usernames) == 0 {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "no usernames provided"})
		return
	}
	if len(usernames) > MaxBulkUsernames {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("too many usernames (max %d)", MaxBulkUsernames)})
		return
	}

	kbID := r.PathValue("id")
	var inviterID string
	if u := auth.UserFromContext(r.Context()); u != nil {
		inviterID = u.ID
	}

	result := bulkInviteResult{Shared: []string{}, Pending: []string{}, AlreadyHadAccess: []string{}}
	for _, username := range usernames {
		userID, found, err := h.store.GetUserIDByUsername(r.Context(), username)
		if err != nil {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to look up users"})
			return
		}
		if !found {
			if err := h.store.UpsertPendingInvite(r.Context(), kbID, username, body.Permission, inviterID); err != nil {
				httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to record pending invite"})
				return
			}
			result.Pending = append(result.Pending, username)
			continue
		}
		// Skip the inviter themselves — they already have access.
		if userID == inviterID {
			result.AlreadyHadAccess = append(result.AlreadyHadAccess, username)
			continue
		}
		exists, err := h.store.ShareExists(r.Context(), kbID, userID)
		if err != nil {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to check existing shares"})
			return
		}
		if exists {
			result.AlreadyHadAccess = append(result.AlreadyHadAccess, username)
			continue
		}
		if _, err := h.store.AddKBShare(r.Context(), kbID, userID, body.Permission); err != nil {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to add share"})
			return
		}
		result.Shared = append(result.Shared, username)
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, result)
}

// RemovePendingShare handles DELETE /api/kb/{id}/share/pending/{username}.
// Only KB owners and superadmins may revoke a pending invite.
func (h *SharingHandler) RemovePendingShare(w http.ResponseWriter, r *http.Request) {
	if !isOwnerOrSuperadmin(r) {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "only the KB owner or a superadmin can remove invites"})
		return
	}

	kbID := r.PathValue("id")
	username := r.PathValue("username")

	if err := h.store.RemovePendingInvite(r.Context(), kbID, username); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusNotFound, map[string]string{"error": "pending invite not found"})
			return
		}
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to remove pending invite"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
