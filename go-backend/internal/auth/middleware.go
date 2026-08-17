package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

type userKey struct{}

// WithUser returns a derived context that carries the given claims under the
// internal user-context key. Other packages and tests should use this helper
// instead of constructing the key themselves — keeping the key unexported
// preserves type safety: a stray context.WithValue with key string("user")
// would silently fail to satisfy UserFromContext.
func WithUser(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, userKey{}, claims)
}

// apiKeyIDKey carries the id of the API key a request authenticated with.
type apiKeyIDKey struct{}

// WithAPIKeyID records which API key authenticated this request. Set by
// apikeyauth.Middleware; absent on JWT-authenticated (web) requests.
func WithAPIKeyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, apiKeyIDKey{}, id)
}

// APIKeyIDFromContext returns the authenticating API key's id, or nil when the
// request was not API-key authenticated. The pointer return is deliberate: it
// maps straight onto the nullable usage_events.api_key_id column, so a web turn
// stores SQL NULL rather than the empty string.
func APIKeyIDFromContext(ctx context.Context) *string {
	id, ok := ctx.Value(apiKeyIDKey{}).(string)
	if !ok || id == "" {
		return nil
	}
	return &id
}

type Middleware struct {
	secret    string
	blacklist *Blacklist
}

func NewMiddleware(secret string, blacklist *Blacklist) *Middleware {
	return &Middleware{secret: secret, blacklist: blacklist}
}

func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			httputil.WriteJSONCtx(ctx, w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

		claims, err := ParseToken(tokenStr, m.secret)
		if err != nil {
			logctx.From(ctx).Warn("auth.jwt_invalid", "error", err.Error())
			httputil.WriteJSONCtx(ctx, w, http.StatusUnauthorized, map[string]string{"error": "Invalid or expired token"})
			return
		}

		checks := m.blacklist.CheckAll(ctx, claims.JTI, claims.ID, claims.IssuedAt)
		if checks.IsBlacklisted {
			logctx.From(ctx).Warn("auth.token_revoked", "user_id", claims.ID, "jti", claims.JTI)
			httputil.WriteJSONCtx(ctx, w, http.StatusUnauthorized, map[string]string{"error": "Token has been revoked"})
			return
		}
		if checks.IsUserTokenInvalidated {
			logctx.From(ctx).Warn("auth.token_user_invalidated", "user_id", claims.ID, "jti", claims.JTI)
			httputil.WriteJSONCtx(ctx, w, http.StatusUnauthorized, map[string]string{"error": "Token has been invalidated"})
			return
		}
		if checks.IsTokenBeforeServerBoot {
			logctx.From(ctx).Warn("auth.token_pre_boot", "user_id", claims.ID, "jti", claims.JTI)
			httputil.WriteJSONCtx(ctx, w, http.StatusUnauthorized, map[string]string{"error": "Token issued before server restart"})
			return
		}

		ctx = WithUser(ctx, claims)
		// Make the resolved user ID visible to outer middleware (e.g.
		// the access-log wrapper) that runs before this handler returns
		// but after the inner chain finishes — see logctx.UserIDCapture.
		logctx.SetCapturedUserID(ctx, claims.ID)
		// Debug, not Info: this fires on every authenticated request. The access
		// log already records the (now-captured) user ID per request, so emitting
		// a successful-validation line at Info is pure low-signal volume in prod.
		// Failures stay at Warn below — those are the lines worth alerting on.
		logctx.From(ctx).Debug("auth.jwt_validated", "user_id", claims.ID, "jti", claims.JTI)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OptionalAuth parses a JWT token if present but does not reject unauthenticated requests.
// If the token is valid, the user claims are added to the context. Otherwise, the request
// proceeds without user claims.
func (m *Middleware) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			next.ServeHTTP(w, r)
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := ParseToken(tokenStr, m.secret)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()
		checks := m.blacklist.CheckAll(ctx, claims.JTI, claims.ID, claims.IssuedAt)
		if checks.IsBlacklisted || checks.IsUserTokenInvalidated || checks.IsTokenBeforeServerBoot {
			next.ServeHTTP(w, r)
			return
		}

		ctx = WithUser(ctx, claims)
		logctx.SetCapturedUserID(ctx, claims.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *Middleware) RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			user := UserFromContext(ctx)
			if user == nil {
				httputil.WriteJSONCtx(ctx, w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
				return
			}

			// Superadmin bypasses every RequireRole guard. Hoisted out of the
			// loop so the override is obvious to anyone scanning new
			// RequireRole call sites — adding a role here implicitly grants
			// it to superadmin without the caller having to opt in.
			if user.Role == RoleSuperAdmin {
				next.ServeHTTP(w, r)
				return
			}

			for _, role := range roles {
				if user.Role == role {
					next.ServeHTTP(w, r)
					return
				}
			}

			logctx.From(ctx).Warn("auth.permission_denied",
				"user_id", user.ID,
				"user_role", user.Role,
				"required_roles", roles,
				"path", r.URL.Path,
				"method", r.Method,
			)
			httputil.WriteJSONCtx(ctx, w, http.StatusForbidden, map[string]string{"error": "Insufficient permissions"})
		})
	}
}

func UserFromContext(ctx context.Context) *Claims {
	user, _ := ctx.Value(userKey{}).(*Claims)
	return user
}

// RoleChain returns the standard Authenticate → RequireRole composition used
// by every role-gated route group (admin, superadmin, api-key). Extracted out
// of routes.go so the wiring can be exercised directly in unit tests without
// standing up the full server infra dependency tree.
func RoleChain(mw *Middleware, roles ...string) func(http.HandlerFunc) http.Handler {
	return func(h http.HandlerFunc) http.Handler {
		return mw.Authenticate(mw.RequireRole(roles...)(http.HandlerFunc(h)))
	}
}
