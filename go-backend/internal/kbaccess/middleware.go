package kbaccess

import (
	"context"
	"net/http"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
)

// accessKey is an unexported empty-struct context key used to store
// KBAccessResult. Other packages must use AccessFromContext / WithAccess to
// read or write it; the key itself stays unexported so the accessor is the
// only entry point.
type accessKey struct{}

// KnowledgeBase represents a knowledge base record.
type KnowledgeBase struct {
	ID       string
	UserID   *string
	IsGlobal bool
}

// KBShare represents a share record linking a user to a KB.
type KBShare struct {
	Permission string // "view" or "edit"
}

// KBAccessResult holds the resolved access information for a request.
type KBAccessResult struct {
	KB             *KnowledgeBase
	IsOwner        bool
	Permission     string
	IsGlobalEditor bool
}

// KBStore is the data-access interface used by Middleware. Implementations
// can be real database stores or mocks in tests.
type KBStore interface {
	GetKBByID(ctx context.Context, id string) (*KnowledgeBase, error)
	GetKBShare(ctx context.Context, kbID, userID string) (*KBShare, error)
	IsGlobalKBEditor(ctx context.Context, kbID, userID string) (bool, error)
}

// Middleware holds the dependencies for KB access checks.
type Middleware struct {
	store KBStore
}

// NewMiddleware creates a Middleware that uses store for KB lookups.
func NewMiddleware(store KBStore) *Middleware {
	return &Middleware{store: store}
}

// RequireKBPermission returns an http.Handler middleware that:
//  1. Verifies the user is authenticated (401 otherwise).
//  2. Looks up the KB identified by {id} in the URL path (404 if not found).
//  3. Checks whether the user has at least requiredPerm ("view" or "edit").
//  4. Stores a KBAccessResult in the request context via WithAccess.
//
// Permission hierarchy (highest to lowest):
//   - superadmin  → full access
//   - owner       → full access
//   - global KB   → any authenticated user can view; admin/superadmin/global-editors can edit
//   - shared KB   → per knowledge_base_shares.permission
//   - otherwise   → 403
func (m *Middleware) RequireKBPermission(requiredPerm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := auth.UserFromContext(r.Context())
			if user == nil {
				httputil.WriteJSONCtx(r.Context(), w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
				return
			}

			kbID := r.PathValue("id")
			kb, err := m.store.GetKBByID(r.Context(), kbID)
			if err != nil {
				httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "Internal Server Error"})
				return
			}
			if kb == nil {
				httputil.WriteJSONCtx(r.Context(), w, http.StatusNotFound, map[string]string{"error": "Knowledge base not found"})
				return
			}

			result := &KBAccessResult{KB: kb}

			// Superadmin: full access.
			if user.Role == "superadmin" {
				result.Permission = "edit"
				result.IsOwner = isOwner(kb, user.ID)
				ctx := WithAccess(r.Context(), result)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Owner: full access.
			if isOwner(kb, user.ID) {
				result.IsOwner = true
				result.Permission = "edit"
				ctx := WithAccess(r.Context(), result)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Global KB rules.
			if kb.IsGlobal {
				globalEditor, err := m.store.IsGlobalKBEditor(r.Context(), kbID, user.ID)
				if err != nil {
					httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "Internal Server Error"})
					return
				}
				result.IsGlobalEditor = globalEditor

				if globalEditor || user.Role == "admin" {
					result.Permission = "edit"
				} else {
					result.Permission = "view"
				}

				if !permissionSatisfies(result.Permission, requiredPerm) {
					httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "Insufficient permissions"})
					return
				}
				ctx := WithAccess(r.Context(), result)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Non-global KB: check shares.
			share, err := m.store.GetKBShare(r.Context(), kbID, user.ID)
			if err != nil {
				httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "Internal Server Error"})
				return
			}
			if share == nil {
				httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "Access denied"})
				return
			}

			result.Permission = share.Permission
			if !permissionSatisfies(result.Permission, requiredPerm) {
				httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "Insufficient permissions"})
				return
			}

			ctx := WithAccess(r.Context(), result)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAnalyticsAccess is a standalone middleware (not a method) that checks:
//   - The user is authenticated.
//   - The KB (from {id} in the path) is global.
//   - The user has role admin or superadmin.
//
// It stores a KBAccessResult in context on success.
func (m *Middleware) RequireAnalyticsAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		if user == nil {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
			return
		}

		kbID := r.PathValue("id")
		kb, err := m.store.GetKBByID(r.Context(), kbID)
		if err != nil {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "Internal Server Error"})
			return
		}
		if kb == nil {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusNotFound, map[string]string{"error": "Knowledge base not found"})
			return
		}

		if !kb.IsGlobal {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "Analytics only available for global knowledge bases"})
			return
		}

		if user.Role != "admin" && user.Role != "superadmin" {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "Insufficient permissions"})
			return
		}

		result := &KBAccessResult{
			KB:         kb,
			IsOwner:    isOwner(kb, user.ID),
			Permission: "edit",
		}
		ctx := WithAccess(r.Context(), result)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// WithAccess returns a derived context that carries the given KBAccessResult.
// Other packages and tests should use this helper instead of constructing the
// key themselves — keeping the key unexported preserves type safety and makes
// the accessor pair (WithAccess / AccessFromContext) the only entry point.
func WithAccess(ctx context.Context, result *KBAccessResult) context.Context {
	return context.WithValue(ctx, accessKey{}, result)
}

// AccessFromContext retrieves the KBAccessResult stored by RequireKBPermission
// or RequireAnalyticsAccess. Returns nil if none is present.
func AccessFromContext(ctx context.Context) *KBAccessResult {
	result, _ := ctx.Value(accessKey{}).(*KBAccessResult)
	return result
}

// isOwner returns true when the KB has a non-nil UserID that equals userID.
func isOwner(kb *KnowledgeBase, userID string) bool {
	return kb.UserID != nil && *kb.UserID == userID
}

// permissionSatisfies returns true if the granted permission meets the required
// level. "edit" satisfies both "edit" and "view"; "view" only satisfies "view".
func permissionSatisfies(granted, required string) bool {
	if granted == "edit" {
		return true
	}
	return granted == required
}
