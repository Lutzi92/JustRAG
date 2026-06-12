package middleware

import (
	"net/http"
	"time"
)

// Timeout wraps handlers with a context deadline. It uses http.TimeoutHandler
// which returns 503 if the handler does not complete within the given duration.
// The server's global WriteTimeout (120s) only drops the connection when it
// fires — the handler's context stays live and backend work (LLM calls, DB
// queries) keeps running. This middleware cancels the request context, so use
// it on long non-streaming routes with a deadline below WriteTimeout so the
// 503 can still reach the client. SSE endpoints must NOT be wrapped —
// http.TimeoutHandler buffers the response and removes Flusher support.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, d, `{"error":"request timeout"}`)
	}
}
