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
			// Cap 12 = the five base key/value pairs (10) plus the optional
			// user_id pair (2). Pre-sizing avoids the grow-and-copy that a
			// cap-10 composite literal triggers on every authenticated request.
			attrs := make([]any, 0, 12)
			attrs = append(attrs,
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", requestid.FromContext(ctx),
			)
			if userCap.UserID != "" {
				attrs = append(attrs, "user_id", userCap.UserID)
			}
			slog.Info("request", attrs...)
		})
	}
}
