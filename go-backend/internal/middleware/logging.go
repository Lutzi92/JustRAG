package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/requestid"
)

// highFreqSuffixes are KB sub-paths that the frontend polls every ~300ms.
// Logging these would generate ~20 lines/sec per open KB tab.
var highFreqSuffixes = []string{
	"/files",
	"/chats",
	"/rss",
	"/confluence-sources",
	"/generated-content",
}

func isHighFrequencyPoll(r *http.Request) bool {
	if r.Method != "GET" {
		return false
	}
	p := r.URL.Path
	// Match /api/kb/{id}/{suffix} patterns
	if !strings.HasPrefix(p, "/api/kb/") && !strings.HasPrefix(p, "/api/confluence/") {
		return false
	}
	for _, s := range highFreqSuffixes {
		if strings.HasSuffix(p, s) {
			return true
		}
	}
	// Also suppress /api/confluence/connections polling
	if p == "/api/confluence/connections" {
		return true
	}
	return false
}

// Logging returns the access-log middleware. When verbose is false (the
// default) it suppresses health checks and high-frequency frontend polling
// endpoints, which otherwise emit ~20 lines/sec per open KB tab. Set verbose
// (via LOG_VERBOSE=true) during incident investigation to log every request.
func Logging(verbose bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip logging for health checks and high-frequency frontend polling
			// endpoints unless verbose logging is enabled. These generate ~6
			// requests every 300ms while a KB is open.
			if !verbose && (r.URL.Path == "/health" || r.URL.Path == "/ready" || isHighFrequencyPoll(r)) {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			wrapped := &responseRecorder{ResponseWriter: w}

			// The auth middleware runs deeper in the chain than this wrapper
			// and writes its user_id into a CHILD context the outer code
			// never sees. Attach a UserIDCapture so the inner auth call can
			// deposit the resolved user ID here, where the access log can
			// pick it up without rerunning the JWT parse.
			userCap := &logctx.UserIDCapture{}
			ctx := logctx.WithUserIDCapture(r.Context(), userCap)
			next.ServeHTTP(wrapped, r.WithContext(ctx))

			// A handler that returns without calling WriteHeader or Write
			// gets net/http's implicit 200; mirror that in the log line so
			// "status: 0" never appears.
			status := wrapped.statusCode
			if status == 0 {
				status = http.StatusOK
			}
			// Build the attrs on the stack and emit via LogAttrs: the typed
			// slog.Attr path takes no per-request []any allocation and boxes
			// no values, unlike the variadic slog.Info path. Index 5 is the
			// optional user_id, only included on authenticated requests.
			attrs := [6]slog.Attr{
				slog.String("method", r.Method),
				// redactSecretPath (metrics.go) masks the invite-link token
				// segment before it reaches the log line — the token is a
				// permanent, non-expiring credential, not just an id.
				slog.String("path", redactSecretPath(r.URL.Path)),
				slog.Int("status", status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.String("request_id", requestid.FromContext(ctx)),
			}
			n := 5
			if userCap.UserID != "" {
				attrs[5] = slog.String("user_id", userCap.UserID)
				n = 6
			}
			slog.LogAttrs(ctx, slog.LevelInfo, "request", attrs[:n]...)
		})
	}
}
