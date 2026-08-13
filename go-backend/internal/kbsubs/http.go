package kbsubs

import (
	"fmt"
	"net/http"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// Handler serves the subscription endpoints. Both sit on kbViewChain: rule 4
// of kbaccess.EffectiveRole already grants view on a published public KB, so
// the chain admits exactly the callers who may subscribe — plus members of
// private KBs, which the guard below rejects.
type Handler struct {
	store Store
}

// NewHandler creates a Handler over store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// Subscribe handles PUT /api/kb/{id}/subscription.
func (h *Handler) Subscribe(w http.ResponseWriter, r *http.Request) {
	h.setState(w, r, StateSubscribed)
}

// Unsubscribe handles DELETE /api/kb/{id}/subscription. It does NOT delete
// the user's chats: they keep access to the KB, and an admin flipping
// auto_subscribe must not be able to cost anyone their history through a tile
// they never asked for.
func (h *Handler) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	h.setState(w, r, StateOptedOut)
}

func (h *Handler) setState(w http.ResponseWriter, r *http.Request, state string) {
	ctx := r.Context()

	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}

	access := kbaccess.AccessFromContext(ctx)
	if access == nil || access.KB == nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("kbsubs: missing KB access result"))
		return
	}
	if !access.KB.IsGlobal || !access.KB.IsPublished {
		// Private und unveroeffentlichte KBs kennen kein Abo: dort entscheidet
		// die Mitgliedschaft. Eine Zeile hier waere tote Daten.
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict,
			"only published public knowledge bases can be subscribed to")
		return
	}

	if err := h.store.SetState(ctx, access.KB.ID, user.ID, state); err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, fmt.Errorf("failed to update subscription: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
