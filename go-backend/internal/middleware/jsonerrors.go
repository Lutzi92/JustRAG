package middleware

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// JSONAPIErrors rewrites plain-text 404/405 responses under the given path
// prefixes into the project-wide JSON error envelope {"error": "..."}.
//
// ServeMux emits text/plain defaults for unmatched routes (404) and
// method-mismatched routes (405), and a few handlers call http.NotFound /
// http.Error directly — every other error on the API surface is the JSON
// envelope, so clients that unconditionally json.Unmarshal error bodies
// break on exactly these two statuses. Handlers that already responded with
// application/json pass through untouched, as does every non-matching path
// (the SPA and static-file routes keep their plain-text 404s).
//
// A handler-written text body (e.g. the message passed to http.Error) is
// preserved as the envelope's error string; an empty body falls back to
// http.StatusText.
func JSONAPIErrors(prefixes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			matched := false
			for _, p := range prefixes {
				if strings.HasPrefix(r.URL.Path, p) {
					matched = true
					break
				}
			}
			if !matched {
				next.ServeHTTP(w, r)
				return
			}
			jw := &jsonErrorWriter{ResponseWriter: w}
			next.ServeHTTP(jw, r)
			jw.finish()
		})
	}
}

// jsonErrorWriter intercepts WriteHeader(404|405) calls that carry a
// non-JSON Content-Type, buffers the plain-text body the handler writes
// afterwards, and re-emits it as the JSON error envelope once the handler
// returns (finish). All other responses pass through unmodified. Interface
// delegation (Flusher, Hijacker, Unwrap) mirrors responseRecorder.
type jsonErrorWriter struct {
	http.ResponseWriter
	intercepted bool
	status      int
	body        bytes.Buffer
}

var _ rwUnwrapper = (*jsonErrorWriter)(nil)

func (w *jsonErrorWriter) WriteHeader(code int) {
	if (code == http.StatusNotFound || code == http.StatusMethodNotAllowed) &&
		!strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		w.intercepted = true
		w.status = code
		h := w.Header()
		h.Set("Content-Type", "application/json")
		// http.Error pre-sets text/plain; Content-Length (if any) no longer
		// matches the rewritten body.
		h.Del("Content-Length")
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *jsonErrorWriter) Write(b []byte) (int, error) {
	if w.intercepted {
		return w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// finish emits the buffered plain-text body as the JSON envelope. Called by
// the middleware after the handler returns.
func (w *jsonErrorWriter) finish() {
	if !w.intercepted {
		return
	}
	msg := strings.TrimSpace(w.body.String())
	if msg == "" {
		msg = http.StatusText(w.status)
	}
	payload, err := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: msg})
	if err != nil {
		return
	}
	_, _ = w.ResponseWriter.Write(append(payload, '\n'))
}

func (w *jsonErrorWriter) Flush() {
	// Buffered error bodies are flushed in finish; flushing mid-intercept
	// would interleave partial text with the JSON envelope.
	if w.intercepted {
		return
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *jsonErrorWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Hijack delegates to the underlying ResponseWriter when it implements
// http.Hijacker. Mirrors responseRecorder.Hijack.
func (w *jsonErrorWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}
