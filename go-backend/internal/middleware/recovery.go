package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/justrag/go-backend/internal/httputil"
)

func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			err := recover()
			if err == nil {
				return
			}
			// net/http uses panic(http.ErrAbortHandler) to abort a handler
			// mid-stream (e.g. on client disconnect during SSE). Re-panic so
			// the server framework tears the connection down cleanly instead
			// of logging a spurious 500 and attempting a write-after-close.
			if err == http.ErrAbortHandler {
				panic(err)
			}
			slog.Error("panic recovered",
				"error", err,
				"stack", string(debug.Stack()),
				"method", r.Method,
				"path", r.URL.Path,
			)
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "Internal Server Error")
		}()
		next.ServeHTTP(w, r)
	})
}
