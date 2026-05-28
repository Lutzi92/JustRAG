package middleware

import (
	"log/slog"
	"net/http"

	"github.com/justrag/go-backend/internal/requestid"
)

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prefer client-supplied header if present; otherwise generate.
		ctx := r.Context()
		id := r.Header.Get("X-Request-Id")
		if id != "" {
			ctx = requestid.NewContext(ctx, id)
		} else {
			var err error
			ctx, id, err = requestid.EnsureContext(ctx)
			if err != nil {
				// crypto/rand.Read failed — the OS entropy pool is gone. Surface
				// a 500 instead of panicking out of the handler goroutine.
				slog.Error("requestid: entropy unavailable; responding 500", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			r.Header.Set("X-Request-Id", id)
		}
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r)
	})
}
