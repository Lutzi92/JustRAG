package kbcategories

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/justrag/go-backend/internal/httputil"
)

// Handler serves the category endpoints. The CRUD half is system-admin only
// (a shared taxonomy); assigning a KB to categories is a per-KB admin action
// and sits on kbAdminChain.
type Handler struct {
	store Store
}

// NewHandler creates a Handler over store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

type categoryRequest struct {
	Name      string `json:"name"`
	SortOrder int    `json:"sortOrder"`
}

// List handles GET /api/admin/kb-categories.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cats, err := h.store.List(ctx)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to list categories: %w", err))
		return
	}
	if cats == nil {
		cats = []Category{}
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, cats)
}

// Create handles POST /api/admin/kb-categories.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body categoryRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := normalizeName(body.Name)
	if name == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "name is required")
		return
	}

	cat, err := h.store.Create(ctx, name, body.SortOrder)
	switch {
	case errors.Is(err, ErrDuplicateName):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, "a category with that name already exists")
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to create category: %w", err))
	default:
		httputil.WriteJSONCtx(ctx, w, http.StatusCreated, cat)
	}
}

// Update handles PATCH /api/admin/kb-categories/{catId}.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body categoryRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := normalizeName(body.Name)
	if name == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "name is required")
		return
	}

	cat, err := h.store.Update(ctx, r.PathValue("catId"), name, body.SortOrder)
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "category not found")
	case errors.Is(err, ErrDuplicateName):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, "a category with that name already exists")
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to update category: %w", err))
	default:
		httputil.WriteJSONCtx(ctx, w, http.StatusOK, cat)
	}
}

// Delete handles DELETE /api/admin/kb-categories/{catId}.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	err := h.store.Delete(ctx, r.PathValue("catId"))
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "category not found")
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to delete category: %w", err))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// ListKBCategories handles GET /api/kb/{id}/categories.
func (h *Handler) ListKBCategories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cats, err := h.store.ListKBCategories(ctx, r.PathValue("id"))
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to list KB categories: %w", err))
		return
	}
	if cats == nil {
		cats = []Category{}
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, cats)
}

type setKBCategoriesRequest struct {
	CategoryIDs []string `json:"categoryIds"`
}

// SetKBCategories handles PUT /api/kb/{id}/categories — a full replacement of
// this KB's assignments.
func (h *Handler) SetKBCategories(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body setKBCategoriesRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.store.SetKBCategories(ctx, r.PathValue("id"), body.CategoryIDs); err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to set categories: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
