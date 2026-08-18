// HTTP layer for KB invite links. Routes are registered in
// internal/app/routes.go:
//
//	GET    /api/kb/{id}/invite-links             kbAdminChain
//	POST   /api/kb/{id}/invite-links             kbAdminChain
//	DELETE /api/kb/{id}/invite-links/{linkId}    kbAdminChain
//	POST   /api/invites/{token}/redeem           authenticated only + rate limit
//
// Redeem sits on the plain authenticated chain on purpose: the caller has no
// role on the KB yet, so RequireKBRole would reject them before they could
// join. It is POST-only so link previews in mail and chat clients cannot
// trigger a join by fetching the URL.

package kbinvites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/logctx"
)

// MaxLabelLen caps the human-readable link label.
const MaxLabelLen = 100

// AuditLogger records operator actions. Matches the interface used by
// internal/kbvisibility and internal/adminkboverview.
type AuditLogger interface {
	LogAuditAction(ctx context.Context, operatorID, action, targetType, targetID string, diff any) error
}

// Handler serves the invite-link endpoints.
type Handler struct {
	store Store
	audit AuditLogger
}

// NewHandler creates a Handler over store, auditing through audit.
func NewHandler(store Store, audit AuditLogger) *Handler {
	return &Handler{store: store, audit: audit}
}

type listLinksResponse struct {
	Links []Link `json:"links"`
}

// ListLinks handles GET /api/kb/{id}/invite-links.
func (h *Handler) ListLinks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	links, err := h.store.List(ctx, r.PathValue("id"))
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to list invite links: %w", err))
		return
	}
	if links == nil {
		links = []Link{}
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, listLinksResponse{Links: links})
}

type createLinkRequest struct {
	Role  string `json:"role"`
	Label string `json:"label"`
}

// CreateLink handles POST /api/kb/{id}/invite-links.
func (h *Handler) CreateLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body createLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Assignable rules out 'owner' and any unknown value in one step, exactly
	// as kbmembers.SetMemberRole does.
	if !kbaccess.Assignable(body.Role) {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest,
			`role must be "view", "edit" or "admin"`)
		return
	}
	if utf8.RuneCountInString(body.Label) > MaxLabelLen {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest,
			fmt.Sprintf("label must be at most %d characters", MaxLabelLen))
		return
	}

	var label *string
	if body.Label != "" {
		label = &body.Label
	}

	var createdBy string
	if u := auth.UserFromContext(ctx); u != nil {
		createdBy = u.ID
	}

	token, err := NewToken()
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	kbID := r.PathValue("id")
	link, err := h.store.Create(ctx, kbID, token, body.Role, label, createdBy)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to create invite link: %w", err))
		return
	}

	h.log(ctx, createdBy, "kb_invite_link_create", kbID, map[string]any{"role": body.Role})
	httputil.WriteJSONCtx(ctx, w, http.StatusCreated, link)
}

// DeleteLink handles DELETE /api/kb/{id}/invite-links/{linkId}.
func (h *Handler) DeleteLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID, linkID := r.PathValue("id"), r.PathValue("linkId")

	err := h.store.Delete(ctx, kbID, linkID)
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "invite link not found")
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to revoke invite link: %w", err))
	default:
		var operator string
		if u := auth.UserFromContext(ctx); u != nil {
			operator = u.ID
		}
		h.log(ctx, operator, "kb_invite_link_revoke", kbID, map[string]any{"linkId": linkID})
		w.WriteHeader(http.StatusNoContent)
	}
}

// Redeem handles POST /api/invites/{token}/redeem.
func (h *Handler) Redeem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	u := auth.UserFromContext(ctx)
	if u == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}

	res, err := h.store.Redeem(ctx, r.PathValue("token"), u.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		// Revoked and never-existed are deliberately indistinguishable.
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "invalid or revoked invite link")
	case err != nil:
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to redeem invite link: %w", err))
	default:
		h.log(ctx, u.ID, "kb_invite_link_redeem", res.KBID, map[string]any{"role": res.Role})
		httputil.WriteJSONCtx(ctx, w, http.StatusOK, res)
	}
}

// log records an audit entry without ever failing the request.
func (h *Handler) log(ctx context.Context, operatorID, action, kbID string, diff any) {
	if h.audit == nil || operatorID == "" {
		return
	}
	if err := h.audit.LogAuditAction(ctx, operatorID, action, "knowledge_base", kbID, diff); err != nil {
		logctx.From(ctx).Warn("audit log failed", "action", action, "error", err)
	}
}
