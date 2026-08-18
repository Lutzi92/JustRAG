// Package apikeyauth provides HTTP middleware that authenticates requests using
// API keys issued by the JustRAG system (prefixed with "jrag_").
package apikeyauth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/safego"
)

const (
	tokenPrefix = "jrag_"
	prefixLen   = 13 // first 13 chars of the token become key_prefix
	throttleMs  = int64(60_000)
)

// ApiKeyCandidate holds the fields needed to verify a candidate API key from
// the database.
type ApiKeyCandidate struct {
	ID        string
	UserID    string
	KeyHash   string
	ExpiresAt *time.Time
}

// UserInfo carries the minimal user fields needed to build auth.Claims.
type UserInfo struct {
	ID       string
	Username string
	Role     string
}

// Store is the persistence interface required by Middleware.
type Store interface {
	// GetApiKeysByPrefix returns all API keys whose key_prefix matches.
	GetApiKeysByPrefix(ctx context.Context, prefix string) ([]ApiKeyCandidate, error)
	// GetUserByID returns the user with the given UUID, or nil if not found.
	GetUserByID(ctx context.Context, id string) (*UserInfo, error)
	// UpdateApiKeyLastUsed sets last_used_at = NOW() for the given key ID.
	// Implementations should treat this as fire-and-forget.
	UpdateApiKeyLastUsed(ctx context.Context, id string) error
}

// Middleware authenticates HTTP requests via API keys.
type Middleware struct {
	store      Store
	lastUsed   sync.Map // map[string]int64 — keyID → unix millisecond timestamp
	throttleMs int64
}

// NewMiddleware creates a Middleware backed by store.
func NewMiddleware(store Store) *Middleware {
	return &Middleware{
		store:      store,
		throttleMs: throttleMs,
	}
}

// Authenticate is an HTTP middleware that validates the Bearer token as a
// JustRAG API key and populates the request context with auth.Claims on
// success. On failure it responds with 401 JSON.
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "missing or invalid Authorization header")
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")

		if !strings.HasPrefix(token, tokenPrefix) {
			logctx.From(ctx).Warn("auth.apikey_format_invalid")
			httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "invalid API key format")
			return
		}

		if len(token) < prefixLen {
			logctx.From(ctx).Warn("auth.apikey_format_invalid", "reason", "too_short")
			httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "invalid API key")
			return
		}

		prefix := token[:prefixLen]

		candidates, err := m.store.GetApiKeysByPrefix(ctx, prefix)
		if err != nil {
			logctx.From(ctx).Warn("auth.apikey_lookup_failed", "prefix", prefix, "error", err.Error())
			httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "invalid API key")
			return
		}
		if len(candidates) == 0 {
			logctx.From(ctx).Warn("auth.apikey_unknown_prefix", "prefix", prefix)
			httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "invalid API key")
			return
		}

		var matched *ApiKeyCandidate
		for i := range candidates {
			if bcrypt.CompareHashAndPassword([]byte(candidates[i].KeyHash), []byte(token)) == nil {
				matched = &candidates[i]
				break
			}
		}
		if matched == nil {
			logctx.From(ctx).Warn("auth.apikey_mismatch", "prefix", prefix)
			httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "invalid API key")
			return
		}

		// Check expiry.
		if matched.ExpiresAt != nil && time.Now().After(*matched.ExpiresAt) {
			logctx.From(ctx).Warn("auth.apikey_expired", "key_id", matched.ID, "user_id", matched.UserID)
			httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "API key has expired")
			return
		}

		// Load the owning user.
		user, err := m.store.GetUserByID(ctx, matched.UserID)
		if err != nil || user == nil {
			logctx.From(ctx).Warn("auth.apikey_user_missing", "key_id", matched.ID, "user_id", matched.UserID, "error", errString(err))
			httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "invalid API key")
			return
		}

		// Throttled last-used update (at most once per minute per key).
		m.maybeUpdateLastUsed(ctx, matched.ID)

		// Inject claims into context — same shape as JWT auth.
		claims := &auth.Claims{
			ID:       user.ID,
			Username: user.Username,
			Role:     user.Role,
		}
		ctx = auth.WithUser(ctx, claims)
		// Expose the authenticating key so downstream handlers can attribute a
		// usage_events row to it (internal/usage). Without this the key id never
		// leaves this middleware.
		ctx = auth.WithAPIKeyID(ctx, matched.ID)
		// Expose the resolved user ID to the outer access-log middleware
		// — see logctx.UserIDCapture and the JWT-auth equivalent.
		logctx.SetCapturedUserID(ctx, user.ID)
		logctx.From(ctx).Info("auth.apikey_validated", "key_id", matched.ID, "user_id", user.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// errString returns err.Error() when non-nil, otherwise the empty string —
// used to pass an optional error into slog without a separate branch.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// maybeUpdateLastUsed fires UpdateApiKeyLastUsed if more than m.throttleMs
// milliseconds have elapsed since the last update for this key.
func (m *Middleware) maybeUpdateLastUsed(ctx context.Context, keyID string) {
	nowMs := time.Now().UnixMilli()

	prev, loaded := m.lastUsed.Load(keyID)
	if loaded {
		if nowMs-prev.(int64) < m.throttleMs {
			return
		}
	}

	m.lastUsed.Store(keyID, nowMs)

	// Fire-and-forget in background with a short timeout so it doesn't block
	// the request or pile up on a slow database.
	safego.GoCtx(ctx, func() {
		// Detach cancellation (the update should outlive the request) but keep
		// tracing + request-id values from ctx for observability.
		tctx, tcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer tcancel()
		if err := m.store.UpdateApiKeyLastUsed(tctx, keyID); err != nil {
			logctx.From(tctx).Warn("apikeyauth: update last_used failed", "key_id", keyID, "error", err)
		}
	})
}
