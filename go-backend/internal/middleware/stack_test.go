package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// buildTestStack assembles a middleware chain shaped like the server's
// real stack in go-backend/internal/app/server.go. Kept here (not in
// package app) so it can exercise the composition without needing DB /
// Redis / storage stubs.
func buildTestStack(t *testing.T, inner http.Handler) http.Handler {
	t.Helper()
	var h http.Handler = inner
	h = MaxBytesExcept(1024, func(r *http.Request) bool {
		return strings.HasPrefix(r.URL.Path, "/exempt")
	})(h)
	h = Logging(false)(h)
	h = RequestID(h)
	h = Recovery(h)
	return h
}

func TestStack_RequestIDPropagates(t *testing.T) {
	// Inner handler inspects the request context for the request id and
	// echoes it in the response body.
	h := buildTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RequestID middleware sets X-Request-Id on the inbound request header.
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			t.Error("X-Request-Id header missing on inbound request")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(id))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("response missing X-Request-Id header")
	}
	if rec.Body.Len() == 0 {
		t.Error("handler did not see the request id via the inbound header")
	}
}

func TestStack_PanicIsRecovered(t *testing.T) {
	h := buildTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	// Must NOT panic.
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 after recovery, got %d", rec.Code)
	}
}

func TestStack_MaxBytesApplied(t *testing.T) {
	// Handler attempts to read the body but does NOT write a response status.
	// This leaves statusRecorder.status == 0, so the MaxBytes middleware can
	// write the 413 after the handler returns.
	h := buildTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body) // overflow error ignored; no WriteHeader call
	}))

	// Body 2 KiB, cap 1 KiB → expect 413.
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, 2048)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 for oversized body, got %d", rec.Code)
	}
}

func TestStack_ExemptPathBypassesCap(t *testing.T) {
	var saw int
	h := buildTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			t.Errorf("body read failed on exempt path: %v", err)
		}
		saw = int(n)
		w.WriteHeader(http.StatusOK)
	}))

	// Post 2 KiB to the exempt path; handler should see all 2048 bytes.
	req := httptest.NewRequest(http.MethodPost, "/exempt/large-upload", bytes.NewReader(make([]byte, 2048)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("exempt path should pass, got %d", rec.Code)
	}
	if saw != 2048 {
		t.Errorf("exempt path body truncated to %d bytes, want 2048", saw)
	}
}

// TestStack_FlusherPreservedThroughStack verifies the whole chain does not
// hide http.Flusher from the terminal handler. SSE endpoints depend on this.
func TestStack_FlusherPreservedThroughStack(t *testing.T) {
	var sawFlusher bool
	h := buildTestStack(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawFlusher = w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !sawFlusher {
		t.Fatal("http.Flusher lost through middleware stack — SSE would break")
	}
}
