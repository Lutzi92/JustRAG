package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaxBytes_UnderLimit(t *testing.T) {
	h := MaxBytes(32)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("unexpected read error: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("hello"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestMaxBytes_OverLimit(t *testing.T) {
	h := MaxBytes(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err == nil {
			t.Error("expected body read to fail, got nil")
		}
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 64)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", rec.Code)
	}
}

func TestMaxBytes_PreservesFlusher(t *testing.T) {
	var sawFlusher bool
	h := MaxBytes(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawFlusher = w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	// httptest.ResponseRecorder implements http.Flusher, so if wrapping
	// loses it, this assertion fails.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !sawFlusher {
		t.Fatal("MaxBytes wrapped writer lost http.Flusher")
	}
}

func TestMaxBytes_HandlerWroteStatus_NotOverridden(t *testing.T) {
	h := MaxBytes(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handler writes 400 before attempting a large read.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.ReadAll(r.Body)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 64)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected handler's 400 to stand, got %d", rec.Code)
	}
}

func TestMaxBytesExcept_SkipsExemptPath(t *testing.T) {
	const limit = 8
	var receivedLen int
	h := MaxBytesExcept(limit, "/api/kb/", "/api/site-config/logo")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("unexpected read error on exempt path: %v", err)
		}
		receivedLen = len(body)
		w.WriteHeader(http.StatusOK)
	}))

	// Body is well over the limit — should be accepted on the exempt path.
	bigBody := strings.Repeat("x", 64)
	req := httptest.NewRequest(http.MethodPost, "/api/kb/123/files", strings.NewReader(bigBody))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 on exempt path, got %d", rec.Code)
	}
	if receivedLen != len(bigBody) {
		t.Errorf("expected handler to receive %d bytes, got %d", len(bigBody), receivedLen)
	}
}

func TestMaxBytesExcept_EnforcesOnNonExemptPath(t *testing.T) {
	const limit = 8
	h := MaxBytesExcept(limit, "/api/kb/", "/api/site-config/logo")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err == nil {
			t.Error("expected body read to fail on non-exempt path, got nil")
		}
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(strings.Repeat("x", 64)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 on non-exempt path, got %d", rec.Code)
	}
}
