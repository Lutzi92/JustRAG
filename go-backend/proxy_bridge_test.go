// Proxy-bridge smoke tests for the legacy Node.js fall-through path: wires
// the Go server's HTTP mux to httptest-mocked health and proxy backends and
// verifies the health / version / /api/* routing. These tests do NOT exercise
// Postgres, pgvector, Redis, the worker queue, or the RAG pipeline — they
// only cover the cross-package router wiring at the top level. Real
// integration coverage for the DB-backed layers is in the package-level
// tests that run against the live test containers in CI.
//
// Previously tagged `integration` and named integration_test.go, which gave
// the misleading impression that this file exercised the whole stack. The
// rename + tag-drop is intentional: these tests now run with the rest of the
// unit suite under `go test ./...`.

package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/health"
	"github.com/justrag/go-backend/internal/proxy"
)

type mockHealthDB struct{ healthy bool }

func (m *mockHealthDB) CheckHealth(_ context.Context) error {
	if !m.healthy {
		return fmt.Errorf("unhealthy")
	}
	return nil
}

func setupRouter(t *testing.T) *http.ServeMux {
	t.Helper()

	nodeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"source": "nodejs", "path": r.URL.Path})
	}))
	t.Cleanup(nodeBackend.Close)

	healthHandler := health.NewHandler(&mockHealthDB{healthy: true}, "test")
	legacyProxy, err := proxy.New(nodeBackend.URL)
	if err != nil {
		t.Fatalf("proxy.New: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler.Health)
	mux.HandleFunc("GET /ready", healthHandler.Ready)
	mux.HandleFunc("GET /version", healthHandler.Version)
	mux.Handle("/", legacyProxy)

	return mux
}

func TestProxyBridge_HealthEndpoint(t *testing.T) {
	t.Parallel()
	mux := setupRouter(t)

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v (raw=%q)", err, rec.Body.String())
	}
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %s", body["status"])
	}
}

func TestProxyBridge_ProxyToNodeJS(t *testing.T) {
	t.Parallel()
	mux := setupRouter(t)

	req := httptest.NewRequest("GET", "/api/kb/123/files", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v (raw=%q)", err, rec.Body.String())
	}
	if body["source"] != "nodejs" {
		t.Errorf("expected proxied to nodejs, got %s", body["source"])
	}
	if body["path"] != "/api/kb/123/files" {
		t.Errorf("expected path /api/kb/123/files, got %s", body["path"])
	}
}

func TestProxyBridge_VersionEndpoint(t *testing.T) {
	t.Parallel()
	mux := setupRouter(t)

	req := httptest.NewRequest("GET", "/version", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("expected Cache-Control: no-store")
	}
}
