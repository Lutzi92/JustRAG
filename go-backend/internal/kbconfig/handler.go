package kbconfig

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/siteconfig"
)

// store is the persistence surface the handler needs (satisfied by *Store).
type store interface {
	ListKBOverrides(ctx context.Context, kbID string) (map[string]*string, error)
	UpsertBatch(ctx context.Context, kbID string, kv map[string]*string) error
	DeleteKey(ctx context.Context, kbID, key string) (bool, error)
}

// globalReader fetches global site_config values in one round-trip. Satisfied
// by *chat.PGStore (siteconfig.BatchReader).
type globalReader interface {
	GetSiteConfigValues(ctx context.Context, keys []string) (map[string]*string, error)
}

// Handler serves the per-KB settings endpoints.
type Handler struct {
	store  store
	global globalReader
}

// NewHandler constructs a settings Handler.
func NewHandler(s store, g globalReader) *Handler { return &Handler{store: s, global: g} }

// valueView is the per-key tri-state returned by GET.
type valueView struct {
	Override  *string `json:"override"`  // kb_site_configs value (nil = not overridden)
	Global    *string `json:"global"`    // global site_config value (nil = unset)
	Effective *string `json:"effective"` // override if present, else global
}

type getResponse struct {
	Registry []siteconfig.KBConfigField `json:"registry"`
	Values   map[string]valueView       `json:"values"`
}

// GetSettings handles GET /api/kb/{id}/settings.
func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")

	overrides, err := h.store.ListKBOverrides(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Error("kbconfig.get.list_overrides", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	fields := siteconfig.All()
	keys := make([]string, 0, len(fields))
	for _, f := range fields {
		keys = append(keys, f.Key)
	}
	globals, err := h.global.GetSiteConfigValues(ctx, keys)
	if err != nil {
		logctx.From(ctx).Error("kbconfig.get.global", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	values := make(map[string]valueView, len(fields))
	for _, f := range fields {
		ov, hasOverride := overrides[f.Key]
		g := globals[f.Key]
		eff := g
		if hasOverride {
			eff = ov
		}
		var ovPtr *string
		if hasOverride {
			ovPtr = ov
		}
		values[f.Key] = valueView{Override: ovPtr, Global: g, Effective: eff}
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, getResponse{Registry: fields, Values: values})
}

type putRequest struct {
	Configs map[string]string `json:"configs"`
}

// PutSettings handles PUT /api/kb/{id}/settings.
func (h *Handler) PutSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")

	var req putRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(req.Configs) == 0 {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "configs is required")
		return
	}

	// Validate ALL keys before writing ANY (atomic accept/reject).
	kv := make(map[string]*string, len(req.Configs))
	for k, v := range req.Configs {
		if err := siteconfig.Validate(k, v); err != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, err.Error())
			return
		}
		val := v
		kv[k] = &val
	}

	if err := h.store.UpsertBatch(ctx, kbID, kv); err != nil {
		logctx.From(ctx).Error("kbconfig.put.upsert", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, map[string]bool{"success": true})
}

// DeleteSetting handles DELETE /api/kb/{id}/settings/{key}.
func (h *Handler) DeleteSetting(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")
	key := r.PathValue("key")

	if !siteconfig.IsPerKB(key) {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "not a per-KB configurable key")
		return
	}
	if _, err := h.store.DeleteKey(ctx, kbID, key); err != nil {
		logctx.From(ctx).Error("kbconfig.delete", "error", err, "kb_id", kbID, "key", key)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
