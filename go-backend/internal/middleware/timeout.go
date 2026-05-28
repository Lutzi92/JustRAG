package middleware

import (
	"net/http"
	"time"
)

// Timeout wraps handlers with a context deadline. It uses http.TimeoutHandler
// which returns 503 if the handler does not complete within the given duration.
// This is the per-handler safety net since the server's WriteTimeout is disabled
// to support SSE streaming. SSE endpoints should NOT be wrapped with this
// middleware — they manage their own lifetimes via context cancellation.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, d, `{"error":"request timeout"}`)
	}
}
