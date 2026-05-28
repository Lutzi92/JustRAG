// Package auditlogs provides the handler for the admin audit log endpoint.
package auditlogs

import (
	"context"
	"github.com/justrag/go-backend/internal/httputil"
	"net/http"
	"strconv"
	"time"
)

// AuditLogRow is the shape returned to API consumers.
type AuditLogRow struct {
	ID         string    `json:"id"         db:"id"`
	OperatorID *string   `json:"operatorId" db:"operator_id"`
	Action     string    `json:"action"     db:"action"`
	TargetType string    `json:"targetType" db:"target_type"`
	TargetID   *string   `json:"targetId"   db:"target_id"`
	Diff       any       `json:"diff"       db:"diff"`
	CreatedAt  time.Time `json:"createdAt"  db:"created_at"`
}

// AuditLogFilters holds the parsed query parameters for GetAuditLogs.
type AuditLogFilters struct {
	OperatorID string
	Action     string
	TargetType string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// Store is the database interface required by Handler.
type Store interface {
	GetAuditLogs(ctx context.Context, filters AuditLogFilters) ([]AuditLogRow, error)
}

// Handler holds the Store dependency for the audit log endpoint.
type Handler struct {
	store Store
}

// NewHandler creates a Handler using the given Store implementation.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// parseFilters reads the recognised query parameters from r and returns an
// AuditLogFilters.  Invalid or absent values fall back to safe defaults.
func parseFilters(r *http.Request) AuditLogFilters {
	q := r.URL.Query()

	f := AuditLogFilters{
		OperatorID: q.Get("operatorId"),
		Action:     q.Get("action"),
		TargetType: q.Get("targetType"),
		Limit:      100,
		Offset:     0,
	}

	if raw := q.Get("from"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			f.From = &t
		}
	}
	if raw := q.Get("to"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			f.To = &t
		}
	}
	if raw := q.Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			f.Limit = v
		}
	}
	if raw := q.Get("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			f.Offset = v
		}
	}

	return f
}

// GetAuditLogs handles GET /api/admin/audit-logs.
// Access control (superadmin only) is enforced by middleware registered in main.go.
func (h *Handler) GetAuditLogs(w http.ResponseWriter, r *http.Request) {
	filters := parseFilters(r)

	rows, err := h.store.GetAuditLogs(r.Context(), filters)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "failed to fetch audit logs"})
		return
	}

	// Return an empty JSON array rather than null when there are no results.
	if rows == nil {
		rows = []AuditLogRow{}
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, rows)
}
