package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A panicking handler must become a sanitized 500 rather than tearing down the
// process. The body must still be the JSON error envelope every client parses.
func TestRecovery_PanicBecomes500(t *testing.T) {
	t.Parallel()

	h := Recovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not JSON (%q): %v", rec.Body.String(), err)
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("response has no \"error\" key: %v", body)
	}
	// The panic value must never reach the client.
	if got := rec.Body.String(); strings.Contains(got, "boom") {
		t.Errorf("panic value leaked to client: %q", got)
	}
}

// net/http panics with ErrAbortHandler to abort a response mid-stream (an SSE
// client disconnecting is the common case here). Recovery must re-panic so the
// server tears the connection down, instead of logging a spurious 500 and
// attempting a write on a dead connection.
func TestRecovery_RepanicsErrAbortHandler(t *testing.T) {
	t.Parallel()

	h := Recovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		got := recover()
		if got == nil {
			t.Fatal("ErrAbortHandler was swallowed; want re-panic")
		}
		if got != http.ErrAbortHandler {
			t.Fatalf("re-panicked with %v, want http.ErrAbortHandler", got)
		}
	}()

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/sse", nil))
	t.Fatal("ServeHTTP returned normally; want panic")
}

// The happy path must be fully transparent: status, body and headers untouched.
func TestRecovery_PassesThroughWhenNoPanic(t *testing.T) {
	t.Parallel()

	h := Recovery(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Test", "kept")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fine", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
	if got := rec.Header().Get("X-Test"); got != "kept" {
		t.Errorf("X-Test = %q, want %q", got, "kept")
	}
}
