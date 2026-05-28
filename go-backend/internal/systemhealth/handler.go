package systemhealth

import (
	"net/http"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

// Handler holds the service and exposes HTTP handler methods.
type Handler struct {
	svc        *Service
	aiResolver *ai.ConfigResolver
}

// NewHandler creates a Handler backed by svc and aiResolver.
func NewHandler(svc *Service, aiResolver *ai.ConfigResolver) *Handler {
	return &Handler{svc: svc, aiResolver: aiResolver}
}

// GetLiveMetrics handles GET /api/system-health/live.
func (h *Handler) GetLiveMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := h.svc.GetLiveMetrics(r.Context())
	if err != nil {
		logctx.From(r.Context()).Error("systemhealth.get_live_metrics", "error", err)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, metrics)
}

// GetHistoricalMetrics handles GET /api/system-health/history.
//
// Query params:
//   - metric (required)
//   - from   (optional, RFC3339, default: 24 h ago)
//   - to     (optional, RFC3339, default: now)
func (h *Handler) GetHistoricalMetrics(w http.ResponseWriter, r *http.Request) {
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "metric query parameter is required")
		return
	}

	now := time.Now().UTC()
	from := now.Add(-24 * time.Hour)
	to := now

	if rawFrom := r.URL.Query().Get("from"); rawFrom != "" {
		parsed, err := time.Parse(time.RFC3339, rawFrom)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "from must be RFC3339")
			return
		}
		from = parsed
	}

	if rawTo := r.URL.Query().Get("to"); rawTo != "" {
		parsed, err := time.Parse(time.RFC3339, rawTo)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "to must be RFC3339")
			return
		}
		to = parsed
	}

	result, err := h.svc.GetHistoricalMetrics(r.Context(), metric, from, to)
	if err != nil {
		logctx.From(r.Context()).Error("systemhealth.get_historical_metrics", "error", err, "metric", metric)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, result)
}

// GetSubsystems handles GET /api/system-health/subsystems.
func (h *Handler) GetSubsystems(w http.ResponseWriter, r *http.Request) {
	subsystems := h.svc.GetSubsystems(r.Context())
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, subsystems)
}

// CheckAIHealth handles POST /api/system-health/ai-check.
func (h *Handler) CheckAIHealth(w http.ResponseWriter, r *http.Request) {
	result, _ := ai.CheckProviderHealth(r.Context(), h.aiResolver)
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, result)
}
