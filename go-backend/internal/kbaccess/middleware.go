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
	ID          string
	UserID      *string
	IsGlobal    bool
	IsPublished bool
}

// KBAccessResult holds the resolved access information for a request.
type KBAccessResult struct {
	KB      *KnowledgeBase
	Role    string
	IsOwner bool
}

// KBStore is the data-access interface used by Middleware. Implementations
// can be real database stores or mocks in tests.
type KBStore interface {
	GetKBByID(ctx context.Context, id string) (*KnowledgeBase, error)
	GetKBRole(ctx context.Context, kbID, userID string) (string, error)
}

// Middleware holds the dependencies for KB access checks.
type Middleware struct {
	store KBStore
}

// NewMiddleware creates a Middleware that uses store for KB lookups.
func NewMiddleware(store KBStore) *Middleware {
	return &Middleware{store: store}
}

// EffectiveRole implements the five-rule ladder from the design doc. Ordering
// matters: an explicit membership row (rule 2) beats the implicit roles a
// public KB grants (rules 3 and 4), otherwise an editor on a public KB would
// be demoted to view.
//
//  1. system role superadmin           -> owner
//  2. a row in kb_members               -> that role
//  3. IsGlobal and system role admin    -> admin
//  4. IsGlobal and IsPublished          -> view
//  5. otherwise                         -> "" (403)
//
// This is the single source of truth for resolving a caller's KB role.
// RequireKBRole calls it for the standard middleware chain; packages that
// need a second, handler-internal permission check (internal/files,
// internal/openaicompat, internal/academic) call it directly instead of
// hand-rolling their own owner/IsGlobal/superadmin special-cases — those
// would drift from this ladder.
func EffectiveRole(kb *KnowledgeBase, sysRole, memberRole string) string {
	if sysRole == auth.RoleSuperAdmin {
		return RoleOwner
	}
	if Valid(memberRole) {
		return memberRole
	}
	if kb.IsGlobal {
		if sysRole == auth.RoleAdmin {
			return RoleAdmin
		}
		if kb.IsPublished {
			return RoleView
		}
	}
	return ""
}

// RequireKBRole returns middleware that resolves the caller's effective role on
// the KB named by {id} and rejects the request unless it meets required.
// Stores a KBAccessResult in the request context via WithAccess.
func (m *Middleware) RequireKBRole(required string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			user := auth.UserFromContext(ctx)
			if user == nil {
				httputil.WriteJSONCtx(ctx, w, http.StatusUnauthorized,
					map[string]string{"error": "Authentication required"})
				return
			}

			kbID := r.PathValue("id")
			kb, err := m.store.GetKBByID(ctx, kbID)
			if err != nil {
				httputil.WriteJSONCtx(ctx, w, http.StatusInternalServerError,
					map[string]string{"error": "Internal Server Error"})
				return
			}
			if kb == nil {
				httputil.WriteJSONCtx(ctx, w, http.StatusNotFound,
					map[string]string{"error": "Knowledge base not found"})
				return
			}

			// Der Store wird auch fuer Superadmins befragt: sie loesen zwar zu
			// owner auf, aber eine vorhandene Zeile soll in KBAccessResult
			// sichtbar bleiben. Ein Query mehr, dafuer keine Sonderfaelle.
			memberRole, err := m.store.GetKBRole(ctx, kbID, user.ID)
			if err != nil {
				httputil.WriteJSONCtx(ctx, w, http.StatusInternalServerError,
					map[string]string{"error": "Internal Server Error"})
				return
			}

			role := EffectiveRole(kb, user.Role, memberRole)
			if !AtLeast(role, required) {
				// 403 und nicht 404: die Existenz der KB ist ueber die
				// bestehenden Endpunkte ohnehin erkennbar, und ein 404 hier
				// wuerde "nicht gefunden" von "kein Zugriff" ununterscheidbar
				// machen — schlecht fuer den Support.
				httputil.WriteJSONCtx(ctx, w, http.StatusForbidden,
					map[string]string{"error": "Insufficient permissions"})
				return
			}

			result := &KBAccessResult{KB: kb, Role: role, IsOwner: role == RoleOwner}
			next.ServeHTTP(w, r.WithContext(WithAccess(ctx, result)))
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

		if user.Role != auth.RoleAdmin && user.Role != auth.RoleSuperAdmin {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusForbidden, map[string]string{"error": "Insufficient permissions"})
			return
		}

		result := &KBAccessResult{
			KB:      kb,
			IsOwner: isOwner(kb, user.ID),
			Role:    RoleAdmin,
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

// AccessFromContext retrieves the KBAccessResult stored by RequireKBRole
// or RequireAnalyticsAccess. Returns nil if none is present.
func AccessFromContext(ctx context.Context) *KBAccessResult {
	result, _ := ctx.Value(accessKey{}).(*KBAccessResult)
	return result
}

// isOwner returns true when the KB has a non-nil UserID that equals userID.
func isOwner(kb *KnowledgeBase, userID string) bool {
	return kb.UserID != nil && *kb.UserID == userID
}
