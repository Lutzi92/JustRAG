// Package adminusers provides handlers for admin user management endpoints.
package adminusers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

// UserListRow is the shape returned to API consumers for the user list.
type UserListRow struct {
	ID        string    `json:"id"        db:"id"`
	Username  string    `json:"username"  db:"username"`
	FirstName *string   `json:"firstName" db:"first_name"`
	LastName  *string   `json:"lastName"  db:"last_name"`
	Role      string    `json:"role"      db:"role"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

// Store is the persistence interface required by Handler.
type Store interface {
	ListAllUsers(ctx context.Context) ([]UserListRow, error)
	GetUserByID(ctx context.Context, id string) (*UserListRow, error)
	UpdateUserRole(ctx context.Context, id, role string) (*UserListRow, error)
	LogAuditAction(ctx context.Context, operatorID, action, targetType, targetID string, diff any) error
}

// CascadeDeleter is the interface used by Handler to remove a user and all
// their owned KBs together with all associated assets.
type CascadeDeleter interface {
	DeleteUser(ctx context.Context, userID string) error
}

// Handler holds the dependencies for the admin user management endpoints.
type Handler struct {
	store     Store
	blacklist *auth.Blacklist
	deleter   CascadeDeleter
}

// NewHandler creates a Handler with a CascadeDeleter for full cascade user deletion.
func NewHandler(store Store, blacklist *auth.Blacklist, deleter CascadeDeleter) *Handler {
	return &Handler{store: store, blacklist: blacklist, deleter: deleter}
}

// ListUsers handles GET /api/admin/users.
// Returns all users ordered by created_at DESC.
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := h.store.ListAllUsers(ctx)
	if err != nil {
		logctx.From(ctx).ErrorContext(ctx, "adminusers: list users failed", "error", err)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	if rows == nil {
		rows = []UserListRow{}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, rows)
}

// updateRoleBody is the expected JSON body for PATCH /api/admin/users/{id}/role.
type updateRoleBody struct {
	Role string `json:"role"`
}

// UpdateUserRole handles PATCH /api/admin/users/{id}/role.
// Rules:
//  1. Validate role value.
//  2. Cannot change superadmin role unless caller is superadmin.
//  3. Cannot change own role.
//  4. Update role in DB, invalidate user's tokens, audit log.
func (h *Handler) UpdateUserRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing id")
		return
	}

	caller := auth.UserFromContext(ctx)
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Cannot change own role.
	if caller.ID == id {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "cannot change own role")
		return
	}

	var body updateRoleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Role assignment rules (matching Node.js):
	// - Superadmins can assign: user, api-user, admin
	// - Admins can only assign: user, api-user
	// - Nobody can assign superadmin via this endpoint
	var assignableRoles map[string]bool
	if caller.Role == auth.RoleSuperAdmin {
		assignableRoles = map[string]bool{auth.RoleUser: true, auth.RoleAPIUser: true, auth.RoleAdmin: true}
	} else {
		assignableRoles = map[string]bool{auth.RoleUser: true, auth.RoleAPIUser: true}
	}
	if !assignableRoles[body.Role] {
		allowed := make([]string, 0, len(assignableRoles))
		for r := range assignableRoles {
			allowed = append(allowed, r)
		}
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Invalid role. Allowed roles: "+strings.Join(allowed, ", "))
		return
	}

	existing, err := h.store.GetUserByID(ctx, id)
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "user not found")
		return
	}

	// Admins cannot modify other admins or superadmins.
	if caller.Role == auth.RoleAdmin && (existing.Role == auth.RoleAdmin || existing.Role == auth.RoleSuperAdmin) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "Admins cannot modify other admin or superadmin roles.")
		return
	}

	updated, err := h.store.UpdateUserRole(ctx, id, body.Role)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to update user role")
		return
	}
	if updated == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "user not found")
		return
	}

	// Invalidate the user's tokens so the role change takes effect immediately.
	h.blacklist.InvalidateUserTokens(ctx, id)

	// Audit log.
	diff := map[string]string{"from": existing.Role, "to": body.Role}
	_ = h.store.LogAuditAction(ctx, caller.ID, "user.role_change", "user", id, diff)

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, updated)
}

// DeleteUser handles DELETE /api/admin/users/{id}.
//
// Rules:
//  1. Cannot delete self.
//  2. Cascade-delete the user (all owned KBs, files, vector chunks, storage, shares).
//  3. Invalidate the user's tokens.
//  4. Audit log.
//  5. Return 204.
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing id")
		return
	}

	caller := auth.UserFromContext(ctx)
	if caller == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Cannot delete self.
	if caller.ID == id {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "cannot delete own account")
		return
	}

	if err := h.deleter.DeleteUser(ctx, id); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to delete user")
		return
	}

	// Invalidate the deleted user's tokens after successful deletion,
	// so any existing JWTs are revoked immediately.
	h.blacklist.InvalidateUserTokens(ctx, id)

	// Audit log.
	_ = h.store.LogAuditAction(ctx, caller.ID, "user.delete", "user", id, nil)

	w.WriteHeader(http.StatusNoContent)
}
