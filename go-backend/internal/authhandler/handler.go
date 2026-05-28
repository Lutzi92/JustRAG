// Package authhandler implements the POST /api/auth/login, /logout, and /refresh endpoints.
package authhandler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/justrag/go-backend/internal/adminproviders"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/users"
)

// dummyHash is used in constant-time comparisons when the requested username
// does not exist, preventing user enumeration via timing attacks.
const dummyHash = "$2b$10$5gEqnyws7hSy6rfxgXeOOuOKRY7T9VdAnZx/mpHtO8XfwJVq5ASIi"

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// Store is the persistence interface used by this handler package.
type Store interface {
	GetUserByUsername(ctx context.Context, username string) (*users.UserRow, error)
	CreateUser(ctx context.Context, data users.UserCreate) (*users.UserRow, error)
	GetActiveAuthProviders(ctx context.Context) ([]adminproviders.AuthProviderRow, error)
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// LoginFailureRecorder is called to record a failed login attempt for
// rate-limiting purposes. Both RedisRateLimiter and RateLimiter satisfy this.
type LoginFailureRecorder interface {
	RecordFailure(r *http.Request)
}

// Handler holds the dependencies for the auth endpoints.
type Handler struct {
	store         Store
	jwtSecret     string
	blacklist     *auth.Blacklist
	failRecorders []LoginFailureRecorder
}

// NewHandler creates a new Handler.
func NewHandler(store Store, jwtSecret string, blacklist *auth.Blacklist) *Handler {
	return &Handler{store: store, jwtSecret: jwtSecret, blacklist: blacklist}
}

// SetFailureRecorders configures rate limiters that should be incremented
// only on failed login attempts (not on every request).
func (h *Handler) SetFailureRecorders(recorders ...LoginFailureRecorder) {
	h.failRecorders = recorders
}

// recordLoginFailure increments all configured rate-limit counters.
func (h *Handler) recordLoginFailure(r *http.Request) {
	for _, rec := range h.failRecorders {
		rec.RecordFailure(r)
	}
}

// ---------------------------------------------------------------------------
// POST /api/auth/login
// ---------------------------------------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string      `json:"token"`
	User  userPayload `json:"user"`
}

type userPayload struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	Role       string `json:"role"`
	AuthMethod string `json:"authMethod"`
}

// Login handles POST /api/auth/login.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}
	if req.Username == "" || req.Password == "" {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "Username and password are required"})
		return
	}
	if len(req.Username) < 3 || len(req.Username) > 50 {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "Username must be between 3 and 50 characters"})
		return
	}

	ctx := r.Context()

	// Look up user by username.
	user, err := h.store.GetUserByUsername(ctx, req.Username)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Attempt local authentication. When DISABLE_LOCAL_AUTH is set, local
	// password login is rejected for everyone EXCEPT superadmins, which keeps
	// the /admin breakglass working in OIDC-only deployments.
	localAuthDisabled := os.Getenv("DISABLE_LOCAL_AUTH") == "true"
	isSuperadmin := user != nil && user.Role == auth.RoleSuperAdmin
	localAllowed := !localAuthDisabled || isSuperadmin
	localAuthOK := false

	if user != nil &&
		user.PasswordHash != "" &&
		!strings.HasPrefix(user.PasswordHash, "$ldap$") &&
		localAllowed {
		// User exists with a local password hash — compare directly.
		err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password))
		localAuthOK = (err == nil)
	} else {
		// No local user or local auth disabled for this user — run a dummy
		// compare to spend the same time, preventing username enumeration via
		// timing.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(req.Password))
	}

	if localAuthOK {
		h.respondWithToken(r.Context(), w, user)
		return
	}

	// ---- LDAP fallback -------------------------------------------------------
	providers, err := h.store.GetActiveAuthProviders(ctx)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	slog.Info("ldap fallback: evaluating providers", "count", len(providers))
	for _, p := range providers {
		slog.Info("ldap fallback: provider", "id", p.ID, "type", p.Type, "name", p.Name)
		if p.Type != "ldap" {
			continue
		}
		var cfg LDAPConfig
		if jsonErr := json.Unmarshal(p.Config, &cfg); jsonErr != nil {
			slog.Warn("ldap config unmarshal failed", "id", p.ID, "err", jsonErr, "raw", string(p.Config))
			continue
		}
		// Decrypt the service-account password. Values written before LDAP
		// encryption was added carry no `enc:` prefix and are returned verbatim,
		// so legacy plaintext rows keep working until they're re-saved.
		decryptedCreds, decErr := DecryptProviderSecret(cfg.BindCredentials)
		if decErr != nil {
			slog.Warn("ldap bind credentials decrypt failed", "id", p.ID, "err", decErr)
			continue
		}
		cfg.BindCredentials = decryptedCreds
		slog.Info("ldap config parsed",
			"url", cfg.URL,
			"bindDN", cfg.BindDN,
			"bindCredsLen", len(cfg.BindCredentials),
			"searchBase", cfg.SearchBase,
			"searchFilter", cfg.SearchFilter,
		)
		result := AuthenticateLDAP(req.Username, req.Password, cfg)
		if result == nil || !result.Success {
			continue
		}

		// LDAP succeeded.
		if user != nil && user.AuthMethod != "ldap" {
			// A local account with the same username exists but wasn't created via LDAP.
			// Reject the login to prevent an LDAP user from inheriting a privileged local account.
			slog.Warn("ldap login rejected: existing local user with non-ldap auth_method",
				"username", req.Username, "authMethod", user.AuthMethod)
			logctx.From(r.Context()).Warn("auth.login_failed",
				"username", req.Username,
				"method", "ldap",
				"reason", "auth_method_mismatch",
			)
			h.recordLoginFailure(r)
			httputil.WriteJSONCtx(r.Context(), w, http.StatusUnauthorized, map[string]string{"error": "Invalid username or password"})
			return
		}

		if user == nil {
			// Auto-provision the user.
			firstName := result.FirstName
			lastName := result.LastName
			email := result.Email

			var fn, ln, em *string
			if firstName != "" {
				fn = &firstName
			}
			if lastName != "" {
				ln = &lastName
			}
			if email != "" {
				em = &email
			}

			newUser, createErr := h.store.CreateUser(ctx, users.UserCreate{
				Username:     req.Username,
				PasswordHash: "$ldap$provisioned",
				AuthMethod:   "ldap",
				FirstName:    fn,
				LastName:     ln,
				Email:        em,
				Role:         "user",
			})
			if createErr != nil {
				httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "Failed to provision user"})
				return
			}
			user = newUser
		}

		h.respondWithToken(r.Context(), w, user)
		return
	}

	// All auth methods failed.
	logctx.From(r.Context()).Warn("auth.login_failed",
		"username", req.Username,
		"user_exists", user != nil,
	)
	h.recordLoginFailure(r)
	httputil.WriteJSONCtx(r.Context(), w, http.StatusUnauthorized, map[string]string{"error": "Invalid username or password"})
}

// ---------------------------------------------------------------------------
// POST /api/auth/logout
// ---------------------------------------------------------------------------

// Logout handles POST /api/auth/logout. Requires auth middleware.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// The auth middleware has already verified the token's signature and
	// stored the parsed claims in the context — use those rather than
	// re-decoding the bearer token unverified. A forged or expired token
	// never reaches this handler, so we can only ever blacklist a JTI that
	// belonged to a genuine, still-valid session.
	claims := auth.UserFromContext(ctx)
	if claims == nil {
		logctx.From(ctx).Info("auth.logout", "decoded", false)
		httputil.WriteJSONCtx(ctx, w, http.StatusOK, map[string]string{"message": "Logged out"})
		return
	}

	expTime := time.Unix(claims.ExpiresAt, 0)
	h.blacklist.Add(ctx, claims.JTI, expTime)

	logctx.From(ctx).Info("auth.logout", "user_id", claims.ID, "jti", claims.JTI)
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, map[string]string{"message": "Logged out"})
}

// ---------------------------------------------------------------------------
// POST /api/auth/refresh
// ---------------------------------------------------------------------------

// Refresh handles POST /api/auth/refresh. Requires auth middleware.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	claims := auth.UserFromContext(r.Context())
	if claims == nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
		return
	}

	tokenStr, err := h.signToken(claims.ID, claims.Username, claims.Role)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate token"})
		return
	}

	// Invalidate the previous token so a leaked copy can't keep working until
	// its natural expiry alongside the freshly issued one. signToken mints a
	// new JTI, so the new token isn't affected by blacklisting the old one.
	h.blacklist.Add(r.Context(), claims.JTI, time.Unix(claims.ExpiresAt, 0))

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, loginResponse{
		Token: tokenStr,
		User: userPayload{
			ID:       claims.ID,
			Username: claims.Username,
			Role:     claims.Role,
		},
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (h *Handler) respondWithToken(ctx context.Context, w http.ResponseWriter, user *users.UserRow) {
	tokenStr, err := h.signToken(user.ID, user.Username, user.Role)
	if err != nil {
		logctx.From(ctx).Error("auth.login_token_error", "user_id", user.ID, "error", err.Error())
		httputil.WriteJSONCtx(ctx, w, http.StatusInternalServerError, map[string]string{"error": "Failed to generate token"})
		return
	}
	logctx.From(ctx).Info("auth.login_success",
		"user_id", user.ID,
		"username", user.Username,
		"role", user.Role,
		"method", user.AuthMethod,
	)
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, loginResponse{
		Token: tokenStr,
		User: userPayload{
			ID:         user.ID,
			Username:   user.Username,
			Role:       user.Role,
			AuthMethod: user.AuthMethod,
		},
	})
}

func (h *Handler) signToken(userID, username, role string) (string, error) {
	jti := uuid.NewString()
	now := time.Now()
	exp := now.Add(24 * time.Hour)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":       userID,
		"username": username,
		"role":     role,
		"jti":      jti,
		"iat":      now.Unix(),
		"exp":      exp.Unix(),
	})

	return token.SignedString([]byte(h.jwtSecret))
}
