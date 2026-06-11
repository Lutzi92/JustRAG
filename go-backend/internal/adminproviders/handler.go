// Package adminproviders provides CRUD handlers for the auth_providers table.
// All endpoints require admin role — enforced by middleware registered in main.go.
package adminproviders

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
	"github.com/justrag/go-backend/internal/store"
)

// AuthProviderRow holds a full auth_provider record from the database.
type AuthProviderRow struct {
	ID        string          `json:"id" db:"id"`
	Type      string          `json:"type" db:"type"`
	Name      string          `json:"name" db:"name"`
	Config    json.RawMessage `json:"config" db:"config"`
	IsActive  bool            `json:"isActive" db:"is_active"`
	CreatedAt time.Time       `json:"createdAt" db:"created_at"`
}

// sensitiveConfigKeys lists JSON keys in auth provider configs that contain secrets.
var sensitiveConfigKeys = map[string]bool{
	"bindCredentials": true,
	"clientSecret":    true,
}

// maskProviderConfig redacts sensitive fields in the config JSON before returning to clients.
func maskProviderConfig(row *AuthProviderRow) {
	if row == nil || len(row.Config) == 0 {
		return
	}
	var cfg map[string]any
	if err := json.Unmarshal(row.Config, &cfg); err != nil {
		return
	}
	for key := range sensitiveConfigKeys {
		if v, ok := cfg[key]; ok {
			if s, isStr := v.(string); isStr && len(s) > 0 {
				cfg[key] = "********"
			}
		}
	}
	masked, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	row.Config = masked
}

// maskProviderConfigs applies masking to a slice of rows.
func maskProviderConfigs(rows []AuthProviderRow) {
	for i := range rows {
		maskProviderConfig(&rows[i])
	}
}

// AuthProviderCreate carries the fields for inserting a new auth provider.
type AuthProviderCreate struct {
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	Config   json.RawMessage `json:"config"`
	IsActive *bool           `json:"isActive"`
}

// AuthProviderUpdate carries the fields for updating an existing auth provider.
// Only non-nil fields are applied.
type AuthProviderUpdate struct {
	Type     *string          `json:"type"`
	Name     *string          `json:"name"`
	Config   *json.RawMessage `json:"config"`
	IsActive *bool            `json:"isActive"`
}

// Store is the persistence interface used by the adminproviders handlers.
type Store interface {
	ListAuthProviders(ctx context.Context) ([]AuthProviderRow, error)
	GetAuthProvider(ctx context.Context, id string) (*AuthProviderRow, error)
	CreateAuthProvider(ctx context.Context, data AuthProviderCreate) (*AuthProviderRow, error)
	UpdateAuthProvider(ctx context.Context, id string, data AuthProviderUpdate) (*AuthProviderRow, error)
	DeleteAuthProvider(ctx context.Context, id string) error
	LogAuditAction(ctx context.Context, operatorID, action, targetType, targetID string, diff any) error
}

// operatorID extracts the authenticated admin's user ID from the request
// context for audit-log attribution. Empty when unauthenticated, which cannot
// happen behind the admin middleware.
func operatorID(r *http.Request) string {
	claims := auth.UserFromContext(r.Context())
	if claims == nil {
		return ""
	}
	return claims.ID
}

// providerSecretMask is the placeholder maskProviderConfig substitutes for
// sensitive values before returning configs to clients. When a create/update
// submits this exact value we treat it as "unchanged" and preserve the stored
// secret rather than persisting the mask.
const providerSecretMask = "********"

// Handler holds the dependencies for the auth provider endpoints.
type Handler struct {
	store               Store
	wrapSecret          ProviderSecretWrapper
	checkOIDCConflict   OIDCConflictChecker
	invalidateOIDCCache func(providerID string)
}

// NewHandler creates a new Handler backed by store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// WithOIDCHooks wires the OIDC-specific helpers. The handler still works
// without them (LDAP-only deployments), but OIDC create/update needs them to
// enforce single-active and to encrypt clientSecret at rest.
func (h *Handler) WithOIDCHooks(wrap ProviderSecretWrapper, conflict OIDCConflictChecker, invalidate func(string)) *Handler {
	h.wrapSecret = wrap
	h.checkOIDCConflict = conflict
	h.invalidateOIDCCache = invalidate
	return h
}

// prepareOIDCConfig validates an OIDC config blob, encrypts clientSecret, and
// returns the rewritten JSON. Called on create/update before the row hits
// the DB. If the caller PATCHed without clientSecret (i.e. left the masked
// placeholder in the field), we preserve the existing encrypted value by
// returning a "skip" sentinel — see UpdateAuthProvider for the merge logic.
func (h *Handler) prepareOIDCConfig(raw json.RawMessage) (json.RawMessage, string, error) {
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, "", errors.New("config must be a valid JSON object")
	}
	for _, k := range []string{"issuerURL", "clientID", "redirectURI"} {
		v, ok := cfg[k]
		if !ok {
			return nil, "missing " + k, nil
		}
		s, isStr := v.(string)
		if !isStr || strings.TrimSpace(s) == "" {
			return nil, "missing " + k, nil
		}
	}
	if secretRaw, ok := cfg["clientSecret"]; ok {
		if secretStr, isStr := secretRaw.(string); isStr && secretStr != "" && secretStr != "********" {
			if h.wrapSecret == nil {
				return nil, "", errors.New("OIDC clientSecret cannot be stored: encryption helper not configured")
			}
			encrypted, err := h.wrapSecret(secretStr)
			if err != nil {
				return nil, "", fmt.Errorf("encrypt clientSecret: %w", err)
			}
			cfg["clientSecret"] = encrypted
		} else if secretStr == "********" {
			// Caller submitted the masked placeholder — strip it so the
			// existing encrypted value persists (handled by the update path).
			delete(cfg, "clientSecret")
		}
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, "", err
	}
	return out, "", nil
}

// prepareLDAPConfig encrypts a newly supplied bindCredentials with the secret
// wrapper, or preserves the existing encrypted value when the caller submitted
// the masked placeholder (i.e. edited an LDAP provider without re-typing the
// password). existingConfig is the currently stored config JSON (nil on
// create). Returns the rewritten config to persist.
func (h *Handler) prepareLDAPConfig(raw json.RawMessage, existingConfig json.RawMessage) (json.RawMessage, error) {
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, errors.New("config must be a valid JSON object")
	}

	const key = "bindCredentials"
	v, ok := cfg[key]
	s, isStr := v.(string)
	switch {
	case ok && isStr && s == providerSecretMask:
		// Unchanged — carry the existing encrypted value forward so we never
		// persist the literal mask (which would break the LDAP service bind).
		if prev, had := existingSensitiveValue(existingConfig, key); had {
			cfg[key] = prev
		} else {
			delete(cfg, key)
		}
	case ok && isStr && s != "":
		// New plaintext secret — encrypt at rest.
		if h.wrapSecret == nil {
			return nil, errors.New("LDAP bindCredentials cannot be stored: encryption helper not configured")
		}
		encrypted, err := h.wrapSecret(s)
		if err != nil {
			return nil, fmt.Errorf("encrypt bindCredentials: %w", err)
		}
		cfg[key] = encrypted
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// existingSensitiveValue extracts a (still-encrypted) sensitive field from a
// stored config JSON blob. Returns ("", false) when the blob is empty, invalid,
// or lacks a string value for key.
func existingSensitiveValue(existingConfig json.RawMessage, key string) (string, bool) {
	if len(existingConfig) == 0 {
		return "", false
	}
	var existing map[string]any
	if err := json.Unmarshal(existingConfig, &existing); err != nil {
		return "", false
	}
	if v, ok := existing[key]; ok {
		if s, isStr := v.(string); isStr && s != "" {
			return s, true
		}
	}
	return "", false
}

// validProviderTypes is the set of accepted provider type values.
var validProviderTypes = map[string]bool{
	"ldap":   true,
	"oidc":   true,
	"oauth2": true,
	"saml":   true,
}

// ProviderSecretWrapper encrypts clientSecret (and other sensitive fields)
// before they're persisted to auth_providers.config. Wired by main.go so the
// adminproviders package stays decoupled from the encryption helper that
// lives in authhandler.
type ProviderSecretWrapper func(plaintext string) (string, error)

// OIDCConflictChecker reports whether enabling/creating an OIDC provider with
// the given excludeID (empty on create) would create more than one active
// OIDC row. The handler enforces the single-active invariant up front.
type OIDCConflictChecker func(ctx context.Context, excludeID string) (bool, error)

// ListAuthProviders handles GET /api/admin/auth-providers.
func (h *Handler) ListAuthProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := h.store.ListAuthProviders(r.Context())
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to list auth providers")
		return
	}
	maskProviderConfigs(providers)
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, providers)
}

// createBody is the expected JSON body for POST /api/admin/auth-providers.
type createBody struct {
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	Config   json.RawMessage `json:"config"`
	IsActive *bool           `json:"isActive"`
}

// CreateAuthProvider handles POST /api/admin/auth-providers.
func (h *Handler) CreateAuthProvider(w http.ResponseWriter, r *http.Request) {
	var body createBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Validate name
	if len(body.Name) == 0 || len(body.Name) > 100 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "name is required and must be 1-100 characters")
		return
	}

	// Validate type
	if !validProviderTypes[body.Type] {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "type must be one of: ldap, oidc, oauth2, saml")
		return
	}

	// Validate config: must be a non-empty, valid JSON object
	if len(body.Config) == 0 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "config is required")
		return
	}
	var configObj map[string]any
	if err := json.Unmarshal(body.Config, &configObj); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "config must be a valid JSON object")
		return
	}

	cfgToStore := body.Config
	if body.Type == "oidc" {
		// Single-active invariant — refuse a 2nd active OIDC row.
		wantActive := body.IsActive == nil || *body.IsActive
		if wantActive && h.checkOIDCConflict != nil {
			conflict, err := h.checkOIDCConflict(r.Context(), "")
			if err != nil {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to check OIDC conflict")
				return
			}
			if conflict {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "an active OIDC provider already exists; deactivate it first or update the existing row")
				return
			}
		}
		prepared, badField, err := h.prepareOIDCConfig(body.Config)
		if err != nil {
			httputil.WriteInternalErrorCtx(r.Context(), w, err)
			return
		}
		if badField != "" {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "OIDC config: "+badField)
			return
		}
		cfgToStore = prepared
	} else if body.Type == "ldap" {
		prepared, err := h.prepareLDAPConfig(body.Config, nil)
		if err != nil {
			httputil.WriteInternalErrorCtx(r.Context(), w, err)
			return
		}
		cfgToStore = prepared
	}

	created, err := h.store.CreateAuthProvider(r.Context(), AuthProviderCreate{
		Type:     body.Type,
		Name:     body.Name,
		Config:   cfgToStore,
		IsActive: body.IsActive,
	})
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to create auth provider")
		return
	}

	// Audit log — never include the config (it carries encrypted secrets).
	_ = h.store.LogAuditAction(r.Context(), operatorID(r), "auth_provider.create", "auth_provider", created.ID, map[string]any{"name": created.Name, "type": created.Type})

	maskProviderConfig(created)
	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, created)
}

// updateBody is the expected JSON body for PATCH /api/admin/auth-providers/{id}.
type updateBody struct {
	Type     *string          `json:"type"`
	Name     *string          `json:"name"`
	Config   *json.RawMessage `json:"config"`
	IsActive *bool            `json:"isActive"`
}

// UpdateAuthProvider handles PATCH /api/admin/auth-providers/{id}.
func (h *Handler) UpdateAuthProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing id")
		return
	}

	var body updateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Validate provided fields
	if body.Name != nil && (len(*body.Name) == 0 || len(*body.Name) > 100) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "name must be 1-100 characters")
		return
	}
	if body.Type != nil && !validProviderTypes[*body.Type] {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "type must be one of: ldap, oidc, oauth2, saml")
		return
	}
	if body.Config != nil {
		var configObj map[string]any
		if err := json.Unmarshal(*body.Config, &configObj); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "config must be a valid JSON object")
			return
		}
	}

	// OIDC-specific: enforce single-active and encrypt clientSecret on update.
	cfgForStore := body.Config
	isOIDCUpdate := (body.Type != nil && *body.Type == "oidc")
	if isOIDCUpdate {
		// We only know the existing type when body.Type is unset — for that
		// branch the conflict check is skipped (toggling is_active on a row
		// whose type is unchanged can't create a new conflict that wasn't
		// already there). For explicit type=oidc PATCHes we re-check.
		wantActive := body.IsActive == nil || *body.IsActive
		if wantActive && h.checkOIDCConflict != nil {
			conflict, err := h.checkOIDCConflict(r.Context(), id)
			if err != nil {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to check OIDC conflict")
				return
			}
			if conflict {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "another active OIDC provider already exists")
				return
			}
		}
		if body.Config != nil {
			prepared, badField, err := h.prepareOIDCConfig(*body.Config)
			if err != nil {
				httputil.WriteInternalErrorCtx(r.Context(), w, err)
				return
			}
			if badField != "" {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "OIDC config: "+badField)
				return
			}
			cfgForStore = &prepared
		}
	}

	// LDAP-specific: encrypt a new bindCredentials, or preserve the stored one
	// when the caller submitted the masked placeholder.
	if body.Type != nil && *body.Type == "ldap" && body.Config != nil {
		existing, getErr := h.store.GetAuthProvider(r.Context(), id)
		if getErr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to load auth provider")
			return
		}
		var existingCfg json.RawMessage
		if existing != nil {
			existingCfg = existing.Config
		}
		prepared, prepErr := h.prepareLDAPConfig(*body.Config, existingCfg)
		if prepErr != nil {
			httputil.WriteInternalErrorCtx(r.Context(), w, prepErr)
			return
		}
		cfgForStore = &prepared
	}

	updated, err := h.store.UpdateAuthProvider(r.Context(), id, AuthProviderUpdate{
		Type:     body.Type,
		Name:     body.Name,
		Config:   cfgForStore,
		IsActive: body.IsActive,
	})
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to update auth provider")
		return
	}
	if updated == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "auth provider not found")
		return
	}

	if h.invalidateOIDCCache != nil {
		h.invalidateOIDCCache(updated.ID)
	}

	// Audit log — never include the config (it carries encrypted secrets).
	_ = h.store.LogAuditAction(r.Context(), operatorID(r), "auth_provider.update", "auth_provider", updated.ID, map[string]any{"name": updated.Name, "type": updated.Type, "isActive": updated.IsActive})

	maskProviderConfig(updated)
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, updated)
}

// DeleteAuthProvider handles DELETE /api/admin/auth-providers/{id}.
func (h *Handler) DeleteAuthProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing id")
		return
	}

	if err := h.store.DeleteAuthProvider(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "auth provider not found")
			return
		}
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to delete auth provider")
		return
	}

	if h.invalidateOIDCCache != nil {
		h.invalidateOIDCCache(id)
	}

	_ = h.store.LogAuditAction(r.Context(), operatorID(r), "auth_provider.delete", "auth_provider", id, nil)

	w.WriteHeader(http.StatusNoContent)
}
