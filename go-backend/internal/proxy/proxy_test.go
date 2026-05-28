package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/proxy"
)

func TestProxy_ForwardsRequests(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "nodejs")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"source":"node"}`))
	}))
	defer backend.Close()

	p, err := proxy.New(backend.URL)
	if err != nil {
		t.Fatalf("proxy.New: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/kb", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	body, _ := io.ReadAll(rec.Body)
	if string(body) != `{"source":"node"}` {
		t.Errorf("unexpected body: %s", string(body))
	}

	if rec.Header().Get("X-Backend") != "nodejs" {
		t.Error("expected X-Backend header from Node.js")
	}
}

func TestProxy_PreservesPath(t *testing.T) {
	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	p, err := proxy.New(backend.URL)
	if err != nil {
		t.Fatalf("proxy.New: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/kb/123/chat", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if gotPath != "/api/kb/123/chat" {
		t.Errorf("expected path /api/kb/123/chat, got %s", gotPath)
	}
}

func TestProxy_PreservesQueryString(t *testing.T) {
	var gotQuery string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	p, err := proxy.New(backend.URL)
	if err != nil {
		t.Fatalf("proxy.New: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/kb?page=2&limit=10", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if gotQuery != "page=2&limit=10" {
		t.Errorf("expected query page=2&limit=10, got %s", gotQuery)
	}
}
