package health_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/health"
)

var (
	_ health.HealthChecker = (*mockDB)(nil)
	_ health.Pinger        = (*mockRedis)(nil)
)

type mockDB struct {
	healthy bool
}

func (m *mockDB) CheckHealth(ctx context.Context) error {
	if !m.healthy {
		return fmt.Errorf("connection refused")
	}
	return nil
}

type mockRedis struct {
	healthy bool
}

func (m *mockRedis) Ping(ctx context.Context) error {
	if !m.healthy {
		return fmt.Errorf("redis: connection refused")
	}
	return nil
}

func TestHealthHandler(t *testing.T) {
	h := health.NewHandler(&mockDB{healthy: true}, "1.0.0")

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	h.Health(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %s", body["status"])
	}
	if body["timestamp"] == "" {
		t.Error("expected timestamp to be set")
	}
}

func TestReadyHandler_Healthy(t *testing.T) {
	h := health.NewHandler(&mockDB{healthy: true}, "1.0.0")

	req := httptest.NewRequest("GET", "/ready", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["status"] != "ready" {
		t.Errorf("expected status ready, got %s", body["status"])
	}
	if body["database"] != "connected" {
		t.Errorf("expected database connected, got %s", body["database"])
	}
}

func TestReadyHandler_Unhealthy(t *testing.T) {
	h := health.NewHandler(&mockDB{healthy: false}, "1.0.0")

	req := httptest.NewRequest("GET", "/ready", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}

	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["status"] != "not_ready" {
		t.Errorf("expected status not_ready, got %s", body["status"])
	}
}

func TestReadyHandler_RedisHealthy(t *testing.T) {
	h := health.NewHandler(&mockDB{healthy: true}, "1.0.0",
		health.WithRedis(&mockRedis{healthy: true}))

	req := httptest.NewRequest("GET", "/ready", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["redis"] != "connected" {
		t.Errorf("expected redis connected, got %q", body["redis"])
	}
}

func TestReadyHandler_RedisDown(t *testing.T) {
	h := health.NewHandler(&mockDB{healthy: true}, "1.0.0",
		health.WithRedis(&mockRedis{healthy: false}))

	req := httptest.NewRequest("GET", "/ready", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when Redis is down, got %d", rec.Code)
	}

	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["status"] != "not_ready" {
		t.Errorf("expected status not_ready, got %q", body["status"])
	}
	if body["database"] != "connected" {
		t.Errorf("expected database connected (the failure is Redis), got %q", body["database"])
	}
	if body["redis"] != "disconnected" {
		t.Errorf("expected redis disconnected, got %q", body["redis"])
	}
}

func TestVersionHandler(t *testing.T) {
	h := health.NewHandler(&mockDB{healthy: true}, "abc123")

	req := httptest.NewRequest("GET", "/version", nil)
	rec := httptest.NewRecorder()
	h.Version(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("expected Cache-Control no-store")
	}

	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if body["version"] != "abc123" {
		t.Errorf("expected version abc123, got %s", body["version"])
	}
}
