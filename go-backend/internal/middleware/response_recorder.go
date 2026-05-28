package middleware

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
)

// responseRecorder is a thin http.ResponseWriter wrapper that captures the
// status code written by downstream handlers. A zero statusCode means
// "handler did not call WriteHeader or Write" — consumers (logging, maxbytes)
// distinguish that case from a real status code.
//
// SSE handlers reach the underlying http.ResponseController via Unwrap; the
// Flusher and Hijacker interfaces are also delegated explicitly so callers
// that type-assert them (WebSocket upgraders, raw-TCP handlers) don't fail
// silently when this wrapper sits between them and the real ResponseWriter.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

// rwUnwrapper is the interface http.NewResponseController walks via Unwrap.
type rwUnwrapper interface {
	Unwrap() http.ResponseWriter
}

var _ rwUnwrapper = (*responseRecorder)(nil)

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// Hijack delegates to the underlying ResponseWriter when it implements
// http.Hijacker. Required for WebSocket upgrades and any middleware that
// type-asserts http.Hijacker directly rather than going through
// http.ResponseController. Mirrors metricsWriter.Hijack.
func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}
