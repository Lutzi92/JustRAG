package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/requestid"
)

func TestRequestID_AttachesToContext(t *testing.T) {
	var got string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = requestid.FromContext(r.Context())
	})
	srv := RequestID(handler)
	req := httptest.NewRequest("GET", "/", nil)
	srv.ServeHTTP(httptest.NewRecorder(), req)
	if got == "" {
		t.Fatal("expected request id in context, got empty")
	}
}

func TestRequestID_PreservesIncomingHeader(t *testing.T) {
	var got string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = requestid.FromContext(r.Context())
	})
	srv := RequestID(handler)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-Id", "client-supplied")
	srv.ServeHTTP(httptest.NewRecorder(), req)
	if got != "client-supplied" {
		t.Fatalf("expected client-supplied, got %q", got)
	}
}

func TestRequestID_ReturnsHeaderInResponse(t *testing.T) {
	srv := RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("expected X-Request-Id response header")
	}
}
