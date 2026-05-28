// Package apikeys provides handlers for managing API keys.
package apikeys

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
)

const (
	maxKeysPerUser = 10
	keyPrefix      = "jrag_"
	keyRandomBytes = 16
)

// ApiKeyRow is the shape returned to API consumers (no key_hash).
type ApiKeyRow struct {
	ID         string     `json:"id"         db:"id"`
	Name       string     `json:"name"       db:"name"`
	KeyPrefix  string     `json:"keyPrefix"  db:"key_prefix"`
	LastUsedAt *time.Time `json:"lastUsedAt" db:"last_used_at"`
	ExpiresAt  *time.Time `json:"expiresAt"  db:"expires_at"`
	CreatedAt  time.Time  `json:"createdAt"  db:"created_at"`
}

// ApiKeyCreate holds the data required to persist a new API key.
type ApiKeyCreate struct {
	UserID    string
	Name      string
	KeyHash   string
	KeyPrefix string
	ExpiresAt *time.Time
}

// Store is the database interface required by Handler.
type Store interface {
	CreateApiKey(ctx context.Context, data ApiKeyCreate) (*ApiKeyRow, error)
	GetApiKeysByUser(ctx context.Context, userID string) ([]ApiKeyRow, error)
	CountApiKeysByUser(ctx context.Context, userID string) (int, error)
	DeleteApiKey(ctx context.Context, id, userID string) (bool, error)
}

// Handler holds the Store dependency for the API key endpoints.
type Handler struct {
	store Store
}

// NewHandler creates a Handler using the given Store implementation.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// createRequest is the parsed body for POST /api/api-keys.
type createRequest struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

// createResponse is returned on successful key creation (includes plaintext key).
type createResponse struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Key       string     `json:"key"`
	KeyPrefix string     `json:"keyPrefix"`
	ExpiresAt *time.Time `json:"expiresAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

// CreateApiKey handles POST /api/api-keys.
func (h *Handler) CreateApiKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}

	var body createRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 100 {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "name is required and must be 1-100 characters"})
		return
	}

	count, err := h.store.CountApiKeysByUser(ctx, user.ID)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to count existing keys"})
		return
	}
	if count >= maxKeysPerUser {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "maximum number of API keys (10) reached"})
		return
	}

	// Generate key: "jrag_" + 32 hex chars (16 random bytes).
	rawBytes := make([]byte, keyRandomBytes)
	if _, err := rand.Read(rawBytes); err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to generate key"})
		return
	}
	plaintext := keyPrefix + hex.EncodeToString(rawBytes)
	prefix := plaintext[:13]

	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), 10)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to hash key"})
		return
	}

	row, err := h.store.CreateApiKey(ctx, ApiKeyCreate{
		UserID:    user.ID,
		Name:      name,
		KeyHash:   string(hash),
		KeyPrefix: prefix,
		ExpiresAt: body.ExpiresAt,
	})
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to create API key"})
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, createResponse{
		ID:        row.ID,
		Name:      row.Name,
		Key:       plaintext,
		KeyPrefix: row.KeyPrefix,
		ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt,
	})
}

// ListApiKeys handles GET /api/api-keys.
func (h *Handler) ListApiKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}

	rows, err := h.store.GetApiKeysByUser(ctx, user.ID)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to list API keys"})
		return
	}

	if rows == nil {
		rows = []ApiKeyRow{}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, rows)
}

// DeleteApiKey handles DELETE /api/api-keys/{id}.
func (h *Handler) DeleteApiKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}

	keyID := r.PathValue("id")
	if keyID == "" {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "missing key id"})
		return
	}

	deleted, err := h.store.DeleteApiKey(ctx, keyID, user.ID)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to delete API key"})
		return
	}

	if !deleted {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusNotFound, map[string]string{"error": "API key not found"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
