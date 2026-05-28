package health

import (
	"context"
	"net/http"
	"time"

	"github.com/justrag/go-backend/internal/httputil"
)

type HealthChecker interface {
	CheckHealth(ctx context.Context) error
}

// Pinger is the narrow interface satisfied by clients whose only health
// signal is a Ping (e.g. redis.Client). Optional; pass nil to skip the check.
type Pinger interface {
	Ping(ctx context.Context) error
}

type Handler struct {
	db      HealthChecker
	redis   Pinger
	version string
}

// NewHandler wires the readiness probe to the database health checker.
// The redis dependency is optional — pass nil to skip the Redis check.
// When set, a Redis failure surfaces as 503 from /ready so auth-path
// outages are visible to load balancers and oncall.
func NewHandler(db HealthChecker, version string, opts ...HandlerOption) *Handler {
	h := &Handler{db: db, version: version}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// HandlerOption configures optional dependencies on Handler.
type HandlerOption func(*Handler)

// WithRedis attaches a Redis pinger to the readiness probe.
func WithRedis(p Pinger) HandlerOption {
	return func(h *Handler) { h.redis = p }
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]string{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if err := h.db.CheckHealth(ctx); err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusServiceUnavailable, map[string]string{
			"status":    "not_ready",
			"database":  "disconnected",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		})
		return
	}

	if h.redis != nil {
		if err := h.redis.Ping(ctx); err != nil {
			httputil.WriteJSONCtx(r.Context(), w, http.StatusServiceUnavailable, map[string]string{
				"status":    "not_ready",
				"database":  "connected",
				"redis":     "disconnected",
				"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			})
			return
		}
	}

	body := map[string]string{
		"status":    "ready",
		"database":  "connected",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if h.redis != nil {
		body["redis"] = "connected"
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, body)
}

func (h *Handler) Version(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]string{
		"version": h.version,
	})
}
