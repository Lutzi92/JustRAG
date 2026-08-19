package middleware

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
var numericRe = regexp.MustCompile(`/\d+(?:/|$)`)

// inviteRedeemRe matches POST /api/invites/{token}/redeem (registered in
// app/routes.go). The {token} segment is a permanent, non-expiring
// credential (kbinvites.NewToken) granting up to admin on a KB — unlike a
// UUID or numeric id it IS the secret, so it must never survive into a log
// line, a Prometheus label, or (via NormalizeRoute) an OTel span name.
// Anchored on both ends and requiring a literal "/redeem" suffix so a
// merely-similar path like "/api/invitesfoo/x" is never caught.
var inviteRedeemRe = regexp.MustCompile(`^(/api/invites/)[^/]+(/redeem)$`)

// joinLinkRe matches the SPA entry route /join/{token}, served by the
// static-file catch-all (setupStaticServing in app/routes.go) before any API
// route matches — it carries the same secret as inviteRedeemRe.
var joinLinkRe = regexp.MustCompile(`^/join/[^/]+$`)

// redactSecretPath replaces the token segment of invite-link paths with a
// literal {token} placeholder. It runs ahead of the UUID/numeric
// normalization below because a bare high-entropy token is not a UUID or a
// number — it would otherwise sail through unrecognized and unredacted.
func redactSecretPath(path string) string {
	if m := inviteRedeemRe.FindStringSubmatch(path); m != nil {
		return m[1] + "{token}" + m[2]
	}
	if joinLinkRe.MatchString(path) {
		return "/join/{token}"
	}
	return path
}

// routeCache memoizes redacted-path → normalized-route translations. It is
// keyed on the OUTPUT of redactSecretPath, not the raw request path: every
// invite/join request carries a distinct permanent secret token in its raw
// path, and keying on the raw path would retain each one as a sync.Map key
// for the process lifetime (up to routeCacheMax). Keying on the redacted
// path collapses every token into the same "/api/invites/{token}/redeem" /
// "/join/{token}" entry — no secret is retained, and repeated hits on a
// leaked link (the exact abuse scenario) still get a cache hit. For every
// ordinary path redactSecretPath is a no-op, so the cache key equals the raw
// path and behaviour is unchanged. The set of distinct keys is bounded by
// route templates (typically dozens), and the per-path regex passes are
// otherwise repeated on every request AND once more by the OTel span-name
// formatter. sync.Map is cheaper than a RWMutex for this read-mostly
// workload — once warmed up, every lookup is a single atomic load on the
// happy path.
var routeCache sync.Map // map[string]string

// routeCacheSize bounds routeCache. Legitimate traffic only ever produces
// route-template-shaped paths (dozens), but scanner probes (/wp-admin/x,
// /.env, …) mint a fresh non-UUID, non-numeric path per request; without a
// cap each probe would add a cache entry forever. Past the cap we still
// normalize — we just stop memoizing new paths.
var routeCacheSize atomic.Int64

const routeCacheMax = 4096

var (
	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "http_request_duration_seconds",
			Help:        "Duration of HTTP requests in seconds.",
			Buckets:     []float64{0.1, 0.5, 1, 2, 5, 10},
			ConstLabels: prometheus.Labels{"app": "justrag"},
		},
		[]string{"method", "route", "status_code"},
	)

	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "http_requests_total",
			Help:        "Total number of HTTP requests.",
			ConstLabels: prometheus.Labels{"app": "justrag"},
		},
		[]string{"method", "route", "status_code"},
	)
)

// normalizeRoute replaces UUID path segments with {id} to prevent
// high-cardinality label values in Prometheus metrics. Results are cached
// in routeCache because the set of request paths is bounded by route
// templates and this fires on every request from both the metrics
// middleware and the OTel span-name formatter.
func normalizeRoute(path string) string {
	// Redact BEFORE the cache lookup so the map is never keyed on a raw
	// secret token — see the routeCache doc comment.
	key := redactSecretPath(path)
	if v, ok := routeCache.Load(key); ok {
		return v.(string)
	}
	normalized := uuidRe.ReplaceAllString(key, "{id}")
	normalized = numericRe.ReplaceAllStringFunc(normalized, func(m string) string {
		if m[len(m)-1] == '/' {
			return "/{id}/"
		}
		return "/{id}"
	})
	if routeCacheSize.Load() < routeCacheMax {
		if _, loaded := routeCache.LoadOrStore(key, normalized); !loaded {
			routeCacheSize.Add(1)
		}
	}
	return normalized
}

// NormalizeRoute is an exported wrapper for use by other observability code
// (e.g., otelhttp span naming) so route names match Prometheus labels.
func NormalizeRoute(path string) string {
	return normalizeRoute(path)
}

// RedactSecretPath is an exported wrapper around redactSecretPath for use by
// other packages that log r.URL.Path outside this package (e.g., the dev
// proxy's error handler). Prefer this over hand-rolling a second redaction —
// it is the single definition of which paths carry the invite-link secret.
func RedactSecretPath(path string) string {
	return redactSecretPath(path)
}

// MetricsMiddleware records HTTP request duration and count as Prometheus metrics.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &metricsWriter{ResponseWriter: w, statusCode: http.StatusOK}

		defer func() {
			duration := time.Since(start).Seconds()
			status := strconv.Itoa(wrapped.statusCode)
			// Unmatched routes (scanner probes hitting 404/405) would mint a
			// new `route` label value per random path — unbounded Prometheus
			// label cardinality. Collapse them into one bucket; real routes
			// never 404 by path shape (they 404 by missing resource AFTER
			// UUID/numeric segments were normalized to {id}).
			var route string
			switch wrapped.statusCode {
			case http.StatusNotFound, http.StatusMethodNotAllowed:
				route = "unmatched"
			default:
				route = normalizeRoute(r.URL.Path)
			}

			httpRequestDuration.WithLabelValues(r.Method, route, status).Observe(duration)
			httpRequestsTotal.WithLabelValues(r.Method, route, status).Inc()
		}()

		next.ServeHTTP(wrapped, r)
	})
}

// MetricsHandler returns the Prometheus metrics HTTP handler.
func MetricsHandler() http.HandlerFunc {
	h := promhttp.Handler()
	return func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
	}
}

type metricsWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *metricsWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *metricsWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *metricsWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Hijack delegates to the underlying ResponseWriter when it implements
// http.Hijacker. Required for WebSocket upgrades and any middleware that
// type-asserts http.Hijacker directly rather than going through
// http.ResponseController.
func (w *metricsWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}
