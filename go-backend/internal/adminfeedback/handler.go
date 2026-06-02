// Package adminfeedback serves the admin feedback-review endpoint.
// It exposes the most net-negative chunks across all KBs (or scoped to one)
// so operators can identify retrieval content that users repeatedly downvote.
//
// Read path: GET /api/admin/feedback/chunks?kb_id=<id>&limit=<n>
package adminfeedback

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/justrag/go-backend/internal/feedback"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

// Store is the read surface the handler needs (implemented by feedback.Reader).
type Store interface {
	TopNegative(ctx context.Context, kbID string, limit int) ([]feedback.NegativeChunk, error)
}

// Handler serves the admin feedback-review endpoint.
type Handler struct{ store Store }

// NewHandler builds the Handler.
func NewHandler(store Store) *Handler { return &Handler{store: store} }

// GetNegativeChunks serves GET /api/admin/feedback/chunks?kb_id=<id>&limit=<n>.
func (h *Handler) GetNegativeChunks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := strings.TrimSpace(r.URL.Query().Get("kb_id"))
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	rows, err := h.store.TopNegative(ctx, kbID, limit)
	if err != nil {
		logctx.From(ctx).Error("admin.feedback: top negative", "error", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "feedback query failed")
		return
	}
	if rows == nil {
		rows = []feedback.NegativeChunk{}
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, rows)
}
