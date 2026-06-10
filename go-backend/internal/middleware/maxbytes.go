package middleware

import (
	"errors"
	"io"
	"net/http"
)

// MaxBytes wraps handlers so request bodies larger than n bytes are rejected
// with HTTP 413 via http.MaxBytesReader. Use on routes that accept JSON or
// form input. File-upload routes should continue to use per-handler limits
// that know their own cap.
func MaxBytes(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limited := http.MaxBytesReader(w, r.Body, n)
			tr := &trackingReader{ReadCloser: limited}
			r.Body = tr

			rw := &responseRecorder{ResponseWriter: w}
			next.ServeHTTP(rw, r)

			// If an overflow occurred and the handler didn't already write a
			// response, send HTTP 413. statusCode == 0 is responseRecorder's
			// signal for "handler wrote nothing" — both WriteHeader and Write
			// set it to a non-zero value.
			if tr.overflowed && rw.statusCode == 0 {
				http.Error(rw, "request body too large", http.StatusRequestEntityTooLarge)
			}
		})
	}
}

// MaxBytesExcept is like MaxBytes but leaves requests for which exempt
// returns true untouched (e.g. file-upload routes that set their own
// larger cap internally). A predicate rather than a path-prefix list:
// upload routes like "POST /api/kb/{id}/files" have the variable segment
// mid-path, and a prefix broad enough to cover them would also exempt
// every sibling route from the cap.
func MaxBytesExcept(n int64, exempt func(*http.Request) bool) func(http.Handler) http.Handler {
	inner := MaxBytes(n)
	return func(next http.Handler) http.Handler {
		wrapped := inner(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if exempt(r) {
				next.ServeHTTP(w, r)
				return
			}
			wrapped.ServeHTTP(w, r)
		})
	}
}

// trackingReader wraps a MaxBytesReader to record whether an overflow error
// was returned during reading.
type trackingReader struct {
	io.ReadCloser
	overflowed bool
}

func (t *trackingReader) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			t.overflowed = true
		}
	}
	return n, err
}
