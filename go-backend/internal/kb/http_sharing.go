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
	"github.com/justrag/go-backend/internal/kbmembers"
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
//
// DEPRECATED: kept only through Task 9 of the four-role KB permission model
// plan, so the frontend can move to /api/kb/{id}/members in its own commit.
// The endpoints below already write through to kb_members (see
// kbMembersShareStore) — knowledge_base_shares is no longer the authority
// source as of migration 0064, it just isn't dropped until after Phase 2.
// Task 9 deletes this file, its test file, and its five routes.
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

// isAdminOrAbove reports whether the caller's resolved KB role meets the
// admin bar. Replaces the old isOwnerOrSuperadmin, which hand-rolled the
// same resolution kbaccess.EffectiveRole already does — superadmins resolve
// to RoleOwner there, so no separate system-role branch is needed here.
func isAdminOrAbove(r *http.Request) bool {
	access := kbaccess.AccessFromContext(r.Context())
	return access != nil && kbaccess.AtLeast(access.Role, kbaccess.RoleAdmin)
}

// ListShares handles GET /api/kb/{id}/shares.
// Only KB owners and superadmins can see the share list.
func (h *SharingHandler) ListShares(w http.ResponseWriter, r *http.Request) {
	if !isAdminOrAbove(r) {
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
	if !isAdminOrAbove(r) {
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
	if !kbaccess.Assignable(body.Permission) {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": `permission must be "view", "edit" or "admin"`})
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
	if !isAdminOrAbove(r) {
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

// BulkInvite handles POST /api/kb/{id}/share/bulk. DEPRECATED wrapper: the
// canonical implementation of this loop is kbmembers.Handler.BulkInvite
// (POST /api/kb/{id}/members/bulk); this copy exists only because
// SharingHandler's constructor and its test suite are frozen to a
// ShareStore-shaped single dependency that predates internal/kbmembers.
// Both ultimately write kb_members through the same Store — see
// kbMembersShareStore below — so there is one implementation of the SQL,
// even though the per-username categorization loop is necessarily
// duplicated here. For each pasted username: existing user -> kb_members
// (shared, or alreadyHadAccess if they already had a role); unknown user ->
// pending_kb_invites (pending), applied on their first OIDC login. Only KB
// admins, owners and superadmins may bulk-invite.
func (h *SharingHandler) BulkInvite(w http.ResponseWriter, r *http.Request) {
	if !isAdminOrAbove(r) {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "only the KB owner or a superadmin can share this knowledge base"})
		return
	}

	var body bulkInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if !kbaccess.Assignable(body.Permission) {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": `permission must be "view", "edit" or "admin"`})
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
	if !isAdminOrAbove(r) {
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

// kbMembersShareStore adapts a legacy ShareStore so that its list/add/remove
// and existence-check operations actually write and read kb_members (via
// members, a kbmembers.Store) instead of the retired knowledge_base_shares
// table. Pending-invite bookkeeping (GetUserIDByUsername, UpsertPendingInvite,
// ListPendingInvites, RemovePendingInvite) is a different table untouched by
// the four-role migration, so those four methods are simply promoted from the
// embedded legacy store.
//
// This exists so the /share* endpoints above (DEPRECATED, see SharingHandler)
// keep working as a real grant path — not a silent no-op against a table
// kbaccess no longer reads — while the frontend still calls them, and so
// that grant is the single kb_members writer kbmembers.Store already is:
// no second SQL implementation to reconcile once Task 9 deletes this file.
type kbMembersShareStore struct {
	ShareStore
	members kbmembers.Store
}

// NewKBMembersShareStore builds a ShareStore whose list/add/remove/exists
// operations go through members (kb_members), while everything else —
// pending-invite handling — falls through to legacy unchanged.
func NewKBMembersShareStore(legacy ShareStore, members kbmembers.Store) ShareStore {
	return &kbMembersShareStore{ShareStore: legacy, members: members}
}

// ListKBShares lists non-owner kb_members rows in the ShareRow shape the
// deprecated /shares response uses. The owner is excluded because
// knowledge_base_shares never held an owner row either — it was represented
// solely via knowledge_bases.user_id.
func (a *kbMembersShareStore) ListKBShares(ctx context.Context, kbID string) ([]ShareRow, error) {
	members, err := a.members.ListMembers(ctx, kbID)
	if err != nil {
		return nil, err
	}
	rows := make([]ShareRow, 0, len(members))
	for _, m := range members {
		if m.Role == kbaccess.RoleOwner {
			continue
		}
		rows = append(rows, ShareRow{
			UserID:     m.UserID,
			Username:   m.Username,
			Permission: m.Role,
			CreatedAt:  m.CreatedAt,
		})
	}
	return rows, nil
}

// AddKBShare grants permission on kbID to userID via kb_members.SetRole, then
// re-reads the row to populate the ShareRow the deprecated handler returns.
func (a *kbMembersShareStore) AddKBShare(ctx context.Context, kbID, userID, permission string) (*ShareRow, error) {
	var grantedBy string
	if u := auth.UserFromContext(ctx); u != nil {
		grantedBy = u.ID
	}
	if err := a.members.SetRole(ctx, kbID, userID, permission, grantedBy); err != nil {
		return nil, err
	}
	members, err := a.members.ListMembers(ctx, kbID)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if m.UserID == userID {
			return &ShareRow{UserID: m.UserID, Username: m.Username, Permission: m.Role, CreatedAt: m.CreatedAt}, nil
		}
	}
	return &ShareRow{UserID: userID, Permission: permission}, nil
}

// RemoveKBShare revokes userID's kb_members role for kbID. Translates
// kbmembers.ErrNotFound to store.ErrNotFound so RemoveShare's existing
// errors.Is(err, store.ErrNotFound) check keeps mapping to 404.
func (a *kbMembersShareStore) RemoveKBShare(ctx context.Context, kbID, userID string) error {
	err := a.members.RemoveMember(ctx, kbID, userID)
	if errors.Is(err, kbmembers.ErrNotFound) {
		return fmt.Errorf("kb_share kb=%s user=%s: %w", kbID, userID, store.ErrNotFound)
	}
	return err
}

// ShareExists reports whether userID already has a kb_members role for kbID
// — the bulk-invite loop's "alreadyHadAccess" check.
func (a *kbMembersShareStore) ShareExists(ctx context.Context, kbID, userID string) (bool, error) {
	_, err := a.members.GetRole(ctx, kbID, userID)
	if errors.Is(err, kbmembers.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
