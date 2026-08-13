package kbvisibility

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

// AuditLogger records operator actions. Matches the interface already used by
// internal/adminglobalkbs and internal/adminkboverview.
type AuditLogger interface {
	LogAuditAction(ctx context.Context, operatorID, action, targetType, targetID string, diff any) error
}

// Handler serves the publish/unpublish admin endpoints. All three routes sit
// on adminChain: making a KB public grants every authenticated user view
// access to it, which is a system-level decision, not a per-KB one.
type Handler struct {
	store Store
	audit AuditLogger
}

// NewHandler creates a Handler over store, auditing through audit.
func NewHandler(store Store, audit AuditLogger) *Handler {
	return &Handler{store: store, audit: audit}
}

// Publish handles POST /api/admin/kb/{id}/publish.
func (h *Handler) Publish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")

	err := h.store.Publish(ctx, kbID)
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "knowledge base not found")
	case errors.Is(err, ErrAlreadyPublic):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, "knowledge base is already public")
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to publish: %w", err))
	default:
		h.log(ctx, "kb_publish", kbID, nil)
		w.WriteHeader(http.StatusNoContent)
	}
}

type unpublishRequest struct {
	NewOwnerID string `json:"newOwnerId"`
}

// Unpublish handles POST /api/admin/kb/{id}/unpublish. newOwnerId is
// mandatory: a private KB without an owner would be unreachable for everyone
// but superadmins, and picking silently would hand the KB to whoever happened
// to click the button.
func (h *Handler) Unpublish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")

	var body unpublishRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NewOwnerID == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest,
			"newOwnerId is required — pick one of the KB admins")
		return
	}

	err := h.store.Unpublish(ctx, kbID, body.NewOwnerID)
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "knowledge base not found")
	case errors.Is(err, ErrNotPublic):
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, "knowledge base is not public")
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to unpublish: %w", err))
	default:
		h.log(ctx, "kb_unpublish", kbID, map[string]string{"newOwnerId": body.NewOwnerID})
		w.WriteHeader(http.StatusNoContent)
	}
}

// UnpublishImpact handles GET /api/admin/kb/{id}/unpublish-impact — the
// numbers and owner candidates the confirmation dialog states up front.
func (h *Handler) UnpublishImpact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	impact, err := h.store.UnpublishImpact(ctx, r.PathValue("id"))
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to compute impact: %w", err))
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, impact)
}

// log records the action, treating an audit failure as non-fatal: the
// visibility change already committed and re-running it would fail with a
// state conflict.
func (h *Handler) log(ctx context.Context, action, kbID string, diff any) {
	var operator string
	if u := auth.UserFromContext(ctx); u != nil {
		operator = u.ID
	}
	if err := h.audit.LogAuditAction(ctx, operator, action, "knowledge_base", kbID, diff); err != nil {
		logctx.From(ctx).WarnContext(ctx, "kbvisibility: audit log failed",
			"error", err, "action", action, "kb_id", kbID)
	}
}
