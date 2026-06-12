package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newCompression(t *testing.T) func(http.Handler) http.Handler {
	t.Helper()
	mw, err := Compression()
	if err != nil {
		t.Fatalf("Compression(): %v", err)
	}
	return mw
}

func TestCompressionGzipsLargeJSON(t *testing.T) {
	payload := `{"data":"` + strings.Repeat("abcdefgh", 1024) + `"}`
	h := newCompression(t)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if string(body) != payload {
		t.Fatalf("decompressed body does not round-trip (len %d vs %d)", len(body), len(payload))
	}
	if rec.Body.Len() >= len(payload) {
		t.Fatalf("compressed size %d not smaller than original %d", rec.Body.Len(), len(payload))
	}
}

func TestCompressionSkipsSmallResponses(t *testing.T) {
	h := newCompression(t)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty for sub-min-size body", got)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestCompressionSkipsWithoutAcceptEncoding(t *testing.T) {
	payload := strings.Repeat("x", 4096)
	h := newCompression(t)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty without Accept-Encoding", got)
	}
	if rec.Body.String() != payload {
		t.Fatalf("body mismatch")
	}
}

// TestCompressionExemptsSSE asserts the load-bearing property for streaming
// chat: an Accept-Encoding-gzip client on a text/event-stream response gets
// plain passthrough (no Content-Encoding), and per-event Flush still reaches
// the client incrementally rather than being buffered to stream end.
func TestCompressionExemptsSSE(t *testing.T) {
	// Events larger than gzhttp's min size so the exemption is proven by the
	// content-type filter, not by the small-response shortcut.
	event := "data: " + strings.Repeat("e", 2048) + "\n\n"
	h := newCompression(t)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter lost http.Flusher through compression middleware")
			return
		}
		for range 3 {
			_, _ = io.WriteString(w, event)
			f.Flush()
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/chats/1/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty for text/event-stream", got)
	}
	if !rec.Flushed {
		t.Fatal("Flush did not propagate to the underlying ResponseWriter")
	}
	if want := strings.Repeat(event, 3); rec.Body.String() != want {
		t.Fatalf("SSE body altered by middleware (len %d vs %d)", rec.Body.Len(), len(want))
	}
}

// TestCompressionUnwrap guards the SSE write-deadline opt-out: the handlers
// use http.NewResponseController(w).SetWriteDeadline, which requires the
// wrapped writer to expose Unwrap() down to the real connection.
func TestCompressionUnwrap(t *testing.T) {
	h := newCompression(t)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			t.Error("compression writer does not implement Unwrap()")
			return
		}
		if u.Unwrap() == nil {
			t.Error("Unwrap() returned nil")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(httptest.NewRecorder(), req)
}
