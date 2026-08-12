// HTTP layer for the four-role KB permission model. The routes and their
// chains are registered in internal/app/routes.go:
//
//	GET    /api/kb/{id}/members                kbAdminChain
//	PUT    /api/kb/{id}/members/{userId}        kbAdminChain
//	DELETE /api/kb/{id}/members/{userId}        kbAdminChain
//	POST   /api/kb/{id}/members/bulk            kbAdminChain
//	POST   /api/kb/{id}/transfer-owner          kbViewChain + owner check here
//	DELETE /api/kb/{id}/membership              kbViewChain
//	GET    /api/kb/{id}/membership/impact       kbViewChain
//
// The member list itself sits behind admin, not view: on a public KB every
// authenticated user resolves to view, and the membership roster is not
// theirs to read.
package kbmembers

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
)

// MaxBulkUsernames caps a single bulk-invite request to bound payload + DB
// work. Mirrors kb.MaxBulkUsernames — the /share/bulk endpoint (DEPRECATED,
// see internal/kb/http_sharing.go) keeps its own copy of this constant
// because it is frozen to a pre-existing test suite; both are 500 and must
// be kept in sync until Task 9 deletes the /share* surface.
const MaxBulkUsernames = 500

// PendingInvite is one row of an unresolved invite for a username that has
// no account yet.
type PendingInvite struct {
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

// PendingInviteStore is the subset of pending-invite bookkeeping the members
// handler needs for usernames that don't have an account yet: BulkInvite
// parks them here instead of granting a role directly, and ListMembers
// surfaces them alongside real members so the UI can show both in one
// fetch. Backed today by internal/kb's PGStore (see the adapter wired in
// internal/app/routes.go), which already owns the pending_kb_invites table
// — there is no reason to duplicate that SQL in this package.
//
// Column note for Task 7: pending_kb_invites.permission is renamed to .role
// in that task's migration. The `role` parameter to UpsertPendingInvite
// below is passed straight through as the underlying SQL `permission`
// argument until then — the rename only has to touch the concrete store
// (internal/kb/store_pg.go) and its adapter in routes.go, not this
// interface, BulkInvite, or ListMembers.
type PendingInviteStore interface {
	GetUserIDByUsername(ctx context.Context, username string) (userID string, found bool, err error)
	UpsertPendingInvite(ctx context.Context, kbID, username, role, invitedBy string) error
	ListPendingInvites(ctx context.Context, kbID string) ([]PendingInvite, error)
}

// Handler serves the member-management and ownership-transfer endpoints.
type Handler struct {
	store   Store
	pending PendingInviteStore
}

// NewHandler creates a Handler backed by store for member rows and pending
// for not-yet-registered invitees.
func NewHandler(store Store, pending PendingInviteStore) *Handler {
	return &Handler{store: store, pending: pending}
}

// listMembersResponse is the GET /api/kb/{id}/members body: real members
// plus not-yet-applied pending invites, so the modal (Task 9) renders both
// from one fetch — mirrors the shape kb.SharingHandler.ListShares used.
type listMembersResponse struct {
	Members []Member        `json:"members"`
	Pending []PendingInvite `json:"pending"`
}

// ListMembers handles GET /api/kb/{id}/members.
func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")

	members, err := h.store.ListMembers(ctx, kbID)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to list members")
		return
	}
	pending, err := h.pending.ListPendingInvites(ctx, kbID)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to list pending invites")
		return
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, listMembersResponse{Members: members, Pending: pending})
}

type setRoleRequest struct {
	Role string `json:"role"`
}

// SetMemberRole handles PUT /api/kb/{id}/members/{userId}.
func (h *Handler) SetMemberRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body setRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Checked before the store, so the caller gets a comprehensible message
	// instead of a generic conflict. Assignable rules out 'owner' and any
	// unknown value in one step.
	if !kbaccess.Assignable(body.Role) {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest,
			`role must be "view", "edit" or "admin" — ownership moves only via transfer`)
		return
	}

	var grantedBy string
	if u := auth.UserFromContext(ctx); u != nil {
		grantedBy = u.ID
	}

	err := h.store.SetRole(ctx, r.PathValue("id"), r.PathValue("userId"), body.Role, grantedBy)
	switch {
	case errors.Is(err, ErrOwnerImmutable):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, "the owner's role cannot be changed")
	case err != nil:
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to set role")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// RemoveMember handles DELETE /api/kb/{id}/members/{userId}. An admin
// removing their own row is allowed — that's a valid way for an admin to
// leave — and it goes through Store.RemoveMember exactly like removing
// anyone else, so (unlike DELETE /membership) it never touches the caller's
// chats: revocation is an administrative act and must never be destructive
// as a side effect of who happens to be logged in.
func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	err := h.store.RemoveMember(ctx, r.PathValue("id"), r.PathValue("userId"))
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "member not found")
	case errors.Is(err, ErrOwnerImmutable):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, "the owner cannot be removed")
	case err != nil:
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to remove member")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type transferOwnerRequest struct {
	UserID string `json:"userId"`
}

// TransferOwner handles POST /api/kb/{id}/transfer-owner. Registered on
// kbViewChain (see routes.go) rather than a role-gated chain, because
// RequireKBRole has no owner tier to gate on without also excluding
// superadmins — they resolve to RoleOwner via kbaccess.EffectiveRole and
// must be let through. The owner check happens here instead.
func (h *Handler) TransferOwner(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	access := kbaccess.AccessFromContext(ctx)
	if access == nil || access.Role != kbaccess.RoleOwner {
		httputil.WriteErrorCtx(ctx, w, http.StatusForbidden, "only the owner may transfer ownership")
		return
	}

	var body transferOwnerRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UserID == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}

	err := h.store.TransferOwner(ctx, r.PathValue("id"), body.UserID)
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "target user not found")
	case err != nil:
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to transfer ownership")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type leaveResponse struct {
	DeletedChats int `json:"deletedChats"`
}

// LeaveKB handles DELETE /api/kb/{id}/membership — the caller removing their
// own access. Destructive by design: it also deletes their chats in this
// KB. The UI states that in the confirmation dialog, seeded by
// /membership/impact.
func (h *Handler) LeaveKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}

	deleted, err := h.store.LeaveKB(ctx, r.PathValue("id"), user.ID)
	switch {
	case errors.Is(err, ErrOwnerImmutable):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict,
			"the owner cannot leave — transfer ownership or delete the knowledge base")
	case errors.Is(err, ErrNotAMember):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "not a member of this knowledge base")
	case err != nil:
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to leave")
	default:
		httputil.WriteJSONCtx(ctx, w, http.StatusOK, leaveResponse{DeletedChats: deleted})
	}
}

type membershipImpactResponse struct {
	ChatCount int `json:"chatCount"`
}

// MembershipImpact handles GET /api/kb/{id}/membership/impact — the number
// backing the leave-confirmation dialog (Task 8), so the UI can tell the
// user how many chats they are about to lose before they confirm leaving.
func (h *Handler) MembershipImpact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}

	n, err := h.store.CountOwnChats(ctx, r.PathValue("id"), user.ID)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to count chats")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, membershipImpactResponse{ChatCount: n})
}

// normalizeUsernames trims, lowercases, drops empties, and dedupes
// (preserving first-seen order).
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

// bulkInviteRequest is the body for POST /api/kb/{id}/members/bulk.
type bulkInviteRequest struct {
	Usernames []string `json:"usernames"`
	Role      string   `json:"role"`
}

// BulkInviteResult is the per-username categorization returned to the UI.
type BulkInviteResult struct {
	Shared           []string `json:"shared"`
	Pending          []string `json:"pending"`
	AlreadyHadAccess []string `json:"alreadyHadAccess"`
}

// BulkInvite handles POST /api/kb/{id}/members/bulk — the canonical home of
// the bulk-invite loop. For each pasted username: a known user gets a
// kb_members row via Store.SetRole (shared, or alreadyHadAccess if they
// already had a role); an unknown username is parked in pending_kb_invites
// (pending), applied on their first OIDC login
// (internal/authhandler.ApplyPendingInvites).
//
// This used to live on kb.SharingHandler.BulkInvite. POST
// /api/kb/{id}/share/bulk (DEPRECATED, see internal/kb/http_sharing.go)
// keeps its own copy of this loop's control flow — not because there are
// two implementations of the write path (both ultimately call this
// package's Store, via the kbMembersShareStore adapter wired in
// routes.go), but because kb.SharingHandler's constructor and its existing
// test suite are frozen to a ShareStore-shaped single dependency that
// predates this package and could not be changed without weakening that
// suite. See the comment on kb.SharingHandler.BulkInvite.
func (h *Handler) BulkInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body bulkInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !kbaccess.Assignable(body.Role) {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest,
			`role must be "view", "edit" or "admin" — ownership moves only via transfer`)
		return
	}

	usernames := normalizeUsernames(body.Usernames)
	if len(usernames) == 0 {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "no usernames provided")
		return
	}
	if len(usernames) > MaxBulkUsernames {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest,
			fmt.Sprintf("too many usernames (max %d)", MaxBulkUsernames))
		return
	}

	kbID := r.PathValue("id")
	var inviterID string
	if u := auth.UserFromContext(ctx); u != nil {
		inviterID = u.ID
	}

	result := BulkInviteResult{Shared: []string{}, Pending: []string{}, AlreadyHadAccess: []string{}}
	for _, username := range usernames {
		userID, found, err := h.pending.GetUserIDByUsername(ctx, username)
		if err != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to look up users")
			return
		}
		if !found {
			if err := h.pending.UpsertPendingInvite(ctx, kbID, username, body.Role, inviterID); err != nil {
				httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to record pending invite")
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
		_, err = h.store.GetRole(ctx, kbID, userID)
		if err == nil {
			result.AlreadyHadAccess = append(result.AlreadyHadAccess, username)
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to check existing membership")
			return
		}
		if err := h.store.SetRole(ctx, kbID, userID, body.Role, inviterID); err != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to add member")
			return
		}
		result.Shared = append(result.Shared, username)
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, result)
}
