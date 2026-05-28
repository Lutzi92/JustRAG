// Package memory exposes user-scoped CRUD over the caller's long-term
// memory rows: list, delete one, bulk clear, and JSON export. These back
// the GDPR "My Memory" drawer (Art. 15 access, Art. 17 erasure, Art. 20
// portability) and work regardless of whether chat_longmem_enabled is on,
// so users can always view/clear prior data.
package memory

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/longmem"
)

// Store is the minimal longmem surface the handlers need.
type Store interface {
	ListByUser(ctx context.Context, userID string) ([]longmem.Memory, error)
	DeleteByID(ctx context.Context, userID string, id int64) error
	DeleteByUser(ctx context.Context, userID string) error
}

// Handler holds the Store dependency.
type Handler struct{ store Store }

// NewHandler creates a Handler.
func NewHandler(store Store) *Handler { return &Handler{store: store} }

// memoryDTO is the JSON shape returned to the user. kbId is "" for facts
// global to the user across KBs.
type memoryDTO struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	KbID      string    `json:"kbId"`
	Salience  float64   `json:"salience"`
	CreatedAt time.Time `json:"createdAt"`
}

func toDTO(rows []longmem.Memory) []memoryDTO {
	out := make([]memoryDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, memoryDTO{
			ID: m.ID, Kind: m.Kind, Content: m.Content,
			KbID: m.KbID, Salience: m.Salience, CreatedAt: m.CreatedAt,
		})
	}
	return out
}

// List handles GET /api/user/memory.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}
	rows, err := h.store.ListByUser(r.Context(), user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to load memory")
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, toDTO(rows))
}

// DeleteOne handles DELETE /api/user/memory/{id}.
func (h *Handler) DeleteOne(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.DeleteByID(r.Context(), user.ID, id); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to delete memory")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteAll handles DELETE /api/user/memory.
func (h *Handler) DeleteAll(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}
	if err := h.store.DeleteByUser(r.Context(), user.ID); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to clear memory")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Export handles GET /api/user/memory/export — a JSON download of the
// user's live memories (GDPR Art. 20 portability).
func (h *Handler) Export(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}
	rows, err := h.store.ListByUser(r.Context(), user.ID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to export memory")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="my-memory.json"`)
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, toDTO(rows))
}
