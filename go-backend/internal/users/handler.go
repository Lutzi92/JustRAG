package users

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
)

var uuidRegex = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// UserRow holds a full user record from the database.
type UserRow struct {
	ID           string    `json:"id" db:"id"`
	Username     string    `json:"username" db:"username"`
	PasswordHash string    `json:"-" db:"password_hash"`
	AuthMethod   string    `json:"-" db:"auth_method"`
	FirstName    *string   `json:"firstName,omitempty" db:"first_name"`
	LastName     *string   `json:"lastName,omitempty" db:"last_name"`
	Email        *string   `json:"email,omitempty" db:"email"`
	Role         string    `json:"role" db:"role"`
	ExternalID   *string   `json:"-" db:"external_id"`
	CreatedAt    time.Time `json:"createdAt" db:"created_at"`
}

// UserUpdate carries the fields to update for a user. Only non-nil fields are applied.
type UserUpdate struct {
	FirstName    *string
	LastName     *string
	Email        *string
	PasswordHash *string
}

// UserCreate carries the fields needed to insert a new user.
type UserCreate struct {
	Username     string
	PasswordHash string
	AuthMethod   string
	FirstName    *string
	LastName     *string
	Email        *string
	Role         string
	// ExternalID stores the OIDC `sub` for users provisioned via OIDC.
	// Nil for local / LDAP users.
	ExternalID *string
}

// Store is the persistence interface used by the users handlers.
type Store interface {
	GetUserByID(ctx context.Context, id string) (*UserRow, error)
	GetUserByUsername(ctx context.Context, username string) (*UserRow, error)
	SearchUserByTerm(ctx context.Context, term string) (*UserRow, error)
	UpdateUser(ctx context.Context, id string, data UserUpdate) (*UserRow, error)
}

// Handler holds the dependencies for the users endpoints.
type Handler struct {
	store Store
}

// NewHandler creates a new Handler backed by store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// fullUserResponse is the response shape for an admin or own-profile lookup.
type fullUserResponse struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	FirstName *string   `json:"firstName,omitempty"`
	LastName  *string   `json:"lastName,omitempty"`
	Email     *string   `json:"email,omitempty"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

// limitedUserResponse is the response shape returned to other authenticated users.
type limitedUserResponse struct {
	ID        string  `json:"id"`
	Username  string  `json:"username"`
	FirstName *string `json:"firstName,omitempty"`
	LastName  *string `json:"lastName,omitempty"`
}

func isAdmin(claims *auth.Claims) bool {
	return claims != nil && (claims.Role == "admin" || claims.Role == "superadmin")
}

// GetUser handles GET /api/users/{id}.
// It resolves the {id} path value as UUID → username → search term.
// Own profile or admin/superadmin receives the full response; others get a limited view.
func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing id")
		return
	}

	var found *UserRow
	var err error

	if uuidRegex.MatchString(id) {
		found, err = h.store.GetUserByID(ctx, id)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch user")
			return
		}
	}

	if found == nil {
		found, err = h.store.GetUserByUsername(ctx, id)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch user")
			return
		}
	}

	if found == nil {
		found, err = h.store.SearchUserByTerm(ctx, id)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch user")
			return
		}
	}

	if found == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "user not found")
		return
	}

	viewer := auth.UserFromContext(ctx)
	if viewer != nil && (viewer.ID == found.ID || isAdmin(viewer)) {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, fullUserResponse{
			ID:        found.ID,
			Username:  found.Username,
			FirstName: found.FirstName,
			LastName:  found.LastName,
			Email:     found.Email,
			Role:      found.Role,
			CreatedAt: found.CreatedAt,
		})
	} else {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, limitedUserResponse{
			ID:        found.ID,
			Username:  found.Username,
			FirstName: found.FirstName,
			LastName:  found.LastName,
		})
	}
}

// patchBody is the expected JSON body for PATCH /api/users/{id}.
type patchBody struct {
	FirstName *string `json:"firstName"`
	LastName  *string `json:"lastName"`
	Email     *string `json:"email"`
	Password  *string `json:"password"`
}

// UpdateUser handles PATCH /api/users/{id}.
// Only the user themselves or an admin/superadmin may update the profile.
func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing id")
		return
	}

	viewer := auth.UserFromContext(ctx)
	if viewer == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	if viewer.ID != id && !isAdmin(viewer) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "forbidden")
		return
	}

	existing, err := h.store.GetUserByID(ctx, id)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to fetch user")
		return
	}
	if existing == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "user not found")
		return
	}

	var body patchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Validate fields
	if body.FirstName != nil && len(*body.FirstName) > 50 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "firstName too long (max 50)")
		return
	}
	if body.LastName != nil && len(*body.LastName) > 50 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "lastName too long (max 50)")
		return
	}
	if body.Email != nil && !strings.Contains(*body.Email, "@") {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid email")
		return
	}
	if body.Password != nil && len(*body.Password) < 8 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	update := UserUpdate{
		FirstName: body.FirstName,
		LastName:  body.LastName,
		Email:     body.Email,
	}

	if body.Password != nil {
		// Cost 12 (~250ms) defends user-chosen low-entropy passwords against
		// offline brute-force if the hash table ever leaks. The login path
		// (authhandler) uses CompareHashAndPassword which reads the cost from
		// the stored hash, so older cost-10 hashes keep working.
		hashed, err := bcrypt.GenerateFromPassword([]byte(*body.Password), 12)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to hash password")
			return
		}
		s := string(hashed)
		update.PasswordHash = &s
	}

	updated, err := h.store.UpdateUser(ctx, id, update)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to update user")
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, fullUserResponse{
		ID:        updated.ID,
		Username:  updated.Username,
		FirstName: updated.FirstName,
		LastName:  updated.LastName,
		Email:     updated.Email,
		Role:      updated.Role,
		CreatedAt: updated.CreatedAt,
	})
}
