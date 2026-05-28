package apidocs_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/apidocs"
)

func TestServeSpec(t *testing.T) {
	h := apidocs.NewHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	w := httptest.NewRecorder()

	h.ServeSpec(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"openapi"`) {
		t.Fatal("response does not contain openapi field")
	}
	if !strings.Contains(body, `"JustRAG Public API"`) {
		t.Fatal("response does not contain expected title")
	}
}

func TestServeDocs(t *testing.T) {
	h := apidocs.NewHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/docs", nil)
	w := httptest.NewRecorder()

	h.ServeDocs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("expected text/html; charset=utf-8, got %s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "scalar") {
		t.Fatal("response does not contain scalar reference")
	}
	if !strings.Contains(body, "/api/v1/openapi.json") {
		t.Fatal("response does not contain spec URL")
	}
}
