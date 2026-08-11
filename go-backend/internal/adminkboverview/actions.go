package adminkboverview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

// KBMeta is the minimal knowledge-base record the mutating admin actions need:
// enough to decide global-vs-personal dispatch and to record a useful audit diff.
type KBMeta struct {
	ID       string  `db:"id"`
	Name     string  `db:"name"`
	IsGlobal bool    `db:"is_global"`
	OwnerID  *string `db:"owner_id"` // NULL for global KBs and for orphaned personal KBs
}

// OwnerInfo identifies a transfer target. DisplayName mirrors the overview's
// owner_name expression so the response and the table agree on the label.
type OwnerInfo struct {
	ID          string `db:"id"`
	Username    string `db:"username"`
	DisplayName string `db:"display_name"`
}

// TransferOwnerResponse is the JSON returned by a successful owner transfer.
type TransferOwnerResponse struct {
	ID            string `json:"id"`
	OwnerID       string `json:"ownerId"`
	OwnerName     string `json:"ownerName"`
	OwnerUsername string `json:"ownerUsername"`
}

// ActionStore is the persistence interface for the mutating admin actions.
// GetKBMeta and GetOwnerInfo return (nil, nil) when no row matches.
type ActionStore interface {
	GetKBMeta(ctx context.Context, kbID string) (*KBMeta, error)
	GetOwnerInfo(ctx context.Context, userID string) (*OwnerInfo, error)
	TransferKBOwner(ctx context.Context, kbID, newOwnerID string, prevOwnerID *string) error
	LogAuditAction(ctx context.Context, operatorID, action, targetType, targetID string, diff any) error
}

// CascadeDeleter removes a knowledge base and every asset that hangs off it
// (vector chunks, storage objects, tabular tables, DB rows). Satisfied by
// *cascade.Deleter.
type CascadeDeleter interface {
	DeleteKB(ctx context.Context, kbID string) error
	DeleteGlobalKB(ctx context.Context, kbID string) error
}

// operatorID returns the acting superadmin's user id for audit logging, or ""
// when the request somehow carries no claims (the superadmin chain prevents it).
func operatorID(r *http.Request) string {
	claims := auth.UserFromContext(r.Context())
	if claims == nil {
		return ""
	}
	return claims.ID
}

// ---------------------------------------------------------------------------
// Endpoint handlers — superadmin KB actions
// ---------------------------------------------------------------------------

// kbIDFromPath reads {id} and rejects anything that is not a UUID, so a
// malformed id becomes a 400 instead of a Postgres 22P02 surfacing as a 500.
// Returns "" after writing the error response.
func kbIDFromPath(w http.ResponseWriter, r *http.Request) string {
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "missing KB id")
		return ""
	}
	if _, err := uuid.Parse(id); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid KB id")
		return ""
	}
	return id
}

// DeleteKB handles DELETE /api/admin/kbs/{id} (superadmin only).
//
// Deletion is irreversible and cascades across the main DB, the vector DB, and
// object storage. Global KBs take the DeleteGlobalKB arm, which additionally
// clears global-editor rows.
func (h *Handler) DeleteKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := kbIDFromPath(w, r)
	if id == "" {
		return
	}

	kb, err := h.store.GetKBMeta(ctx, id)
	if err != nil {
		logctx.From(ctx).Error("adminkboverview.delete_kb: lookup failed", "error", err, "kb_id", id)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	if kb == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "knowledge base not found")
		return
	}

	if kb.IsGlobal {
		err = h.deleter.DeleteGlobalKB(ctx, id)
	} else {
		err = h.deleter.DeleteKB(ctx, id)
	}
	if err != nil {
		logctx.From(ctx).Error("adminkboverview.delete_kb: cascade failed", "error", err, "kb_id", id, "is_global", kb.IsGlobal)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to delete knowledge base")
		return
	}

	_ = h.store.LogAuditAction(ctx, operatorID(r), "kb.admin_delete", "knowledge_base", id,
		map[string]any{"name": kb.Name, "isGlobal": kb.IsGlobal})

	w.WriteHeader(http.StatusNoContent)
}

// transferOwnerRequest is the parsed JSON body for PATCH /api/admin/kbs/{id}/owner.
type transferOwnerRequest struct {
	UserID string `json:"userId"`
}

// TransferOwner handles PATCH /api/admin/kbs/{id}/owner (superadmin only).
//
// The previous owner is demoted to an 'edit' share rather than losing access,
// so a transfer is recoverable. Global KBs are ownerless by design and are
// rejected; their editor list lives in the Global-KBs tab.
//
// Read-check-write under READ COMMITTED is deliberate: this is a manual,
// single-operator action, so the cost of Serializable retries buys nothing.
func (h *Handler) TransferOwner(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := kbIDFromPath(w, r)
	if id == "" {
		return
	}

	var body transferOwnerRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.UserID = strings.TrimSpace(body.UserID)
	if body.UserID == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "missing userId")
		return
	}
	if _, err := uuid.Parse(body.UserID); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid userId")
		return
	}

	kb, err := h.store.GetKBMeta(ctx, id)
	if err != nil {
		logctx.From(ctx).Error("adminkboverview.transfer_owner: kb lookup failed", "error", err, "kb_id", id)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	if kb == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "knowledge base not found")
		return
	}
	if kb.IsGlobal {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "global knowledge bases have no owner")
		return
	}
	if kb.OwnerID != nil && *kb.OwnerID == body.UserID {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "user is already the owner")
		return
	}

	owner, err := h.store.GetOwnerInfo(ctx, body.UserID)
	if err != nil {
		logctx.From(ctx).Error("adminkboverview.transfer_owner: user lookup failed", "error", err)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	if owner == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "user not found")
		return
	}

	if err := h.store.TransferKBOwner(ctx, id, owner.ID, kb.OwnerID); err != nil {
		if errors.Is(err, ErrKBNotFound) {
			// Deleted by another operator between our lookup and the transfer
			// transaction; no audit row for a transfer that never happened.
			httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "knowledge base not found")
			return
		}
		logctx.From(ctx).Error("adminkboverview.transfer_owner: transfer failed", "error", err, "kb_id", id)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to transfer ownership")
		return
	}

	var from any
	if kb.OwnerID != nil {
		from = *kb.OwnerID
	}
	_ = h.store.LogAuditAction(ctx, operatorID(r), "kb.owner_change", "knowledge_base", id,
		map[string]any{"from": from, "to": owner.ID, "name": kb.Name})

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, TransferOwnerResponse{
		ID:            id,
		OwnerID:       owner.ID,
		OwnerName:     owner.DisplayName,
		OwnerUsername: owner.Username,
	})
}
