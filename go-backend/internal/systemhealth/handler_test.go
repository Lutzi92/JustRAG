package systemhealth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/systemhealth"
)

var _ systemhealth.MetricsStore = (*mockStore)(nil)

// mockStore implements MetricsStore for unit tests.
type mockStore struct {
	activeUsers     int
	processingFiles int
	totalKBs        int
	totalFiles      int
	totalStorage    int64
	totalUsers      int
	historicalData  []systemhealth.HistoricalPoint
}

func (m *mockStore) GetActiveUsersCount(_ context.Context) (int, error) {
	return m.activeUsers, nil
}
func (m *mockStore) GetProcessingFilesCount(_ context.Context) (int, error) {
	return m.processingFiles, nil
}
func (m *mockStore) GetTotalKBsCount(_ context.Context) (int, error) {
	return m.totalKBs, nil
}
func (m *mockStore) GetTotalFilesCount(_ context.Context) (int, error) {
	return m.totalFiles, nil
}
func (m *mockStore) GetTotalStorageBytes(_ context.Context) (int64, error) {
	return m.totalStorage, nil
}
func (m *mockStore) GetTotalUsersCount(_ context.Context) (int, error) {
	return m.totalUsers, nil
}
func (m *mockStore) GetHistoricalMetrics(_ context.Context, _ string, _, _ time.Time) ([]systemhealth.HistoricalPoint, error) {
	return m.historicalData, nil
}

// noConfigStore implements ai.ConfigStore with no active providers.
type noConfigStore struct{}

func (s *noConfigStore) GetActiveAIProvider(_ context.Context) (*ai.AIProviderInfo, error) {
	return nil, nil
}
func (s *noConfigStore) GetAIProviderByID(_ context.Context, _ string) (*ai.AIProviderInfo, error) {
	return nil, nil
}
func (s *noConfigStore) GetAIModelsByProvider(_ context.Context, _ string) ([]ai.AIModelInfo, error) {
	return nil, nil
}
func (s *noConfigStore) GetKBModelOverrides(_ context.Context, _ string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

// newTestHandler creates a Handler with nil DB/Redis connections (graceful degradation).
func newTestHandler(store systemhealth.MetricsStore) *systemhealth.Handler {
	svc := systemhealth.NewService(store, nil, nil, nil, nil, false)
	return systemhealth.NewHandler(svc, nil)
}

// TestGetLiveMetrics_Returns200WithCorrectValues verifies that the live metrics
// endpoint returns HTTP 200 and the values from the store.
func TestGetLiveMetrics_Returns200WithCorrectValues(t *testing.T) {
	store := &mockStore{
		activeUsers:     3,
		processingFiles: 7,
		totalKBs:        12,
		totalFiles:      99,
		totalStorage:    1024 * 1024 * 500,
		totalUsers:      42,
	}

	h := newTestHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/system-health/live", nil)
	rec := httptest.NewRecorder()

	h.GetLiveMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body systemhealth.LiveMetrics
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if body.ActiveUsers != store.activeUsers {
		t.Errorf("activeUsers: expected %d, got %d", store.activeUsers, body.ActiveUsers)
	}
	if body.ProcessingFiles != store.processingFiles {
		t.Errorf("processingFiles: expected %d, got %d", store.processingFiles, body.ProcessingFiles)
	}
	if body.TotalKnowledgeBases != store.totalKBs {
		t.Errorf("totalKnowledgeBases: expected %d, got %d", store.totalKBs, body.TotalKnowledgeBases)
	}
	if body.TotalFiles != store.totalFiles {
		t.Errorf("totalFiles: expected %d, got %d", store.totalFiles, body.TotalFiles)
	}
	if body.TotalStorageBytes != store.totalStorage {
		t.Errorf("totalStorageBytes: expected %d, got %d", store.totalStorage, body.TotalStorageBytes)
	}
	if body.TotalUsers != store.totalUsers {
		t.Errorf("totalUsers: expected %d, got %d", store.totalUsers, body.TotalUsers)
	}
	if body.Timestamp == "" {
		t.Error("expected Timestamp to be non-empty")
	}
	if body.QueueStats == nil {
		t.Error("expected QueueStats to be present")
	}
	if body.SubsystemHealth == nil {
		t.Error("expected SubsystemHealth to be present")
	}
}

// TestGetHistoricalMetrics_MissingMetric_Returns400 verifies that omitting the
// required metric query param yields a 400 response.
func TestGetHistoricalMetrics_MissingMetric_Returns400(t *testing.T) {
	h := newTestHandler(&mockStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/system-health/history", nil)
	rec := httptest.NewRecorder()

	h.GetHistoricalMetrics(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	if body["error"] == "" {
		t.Error("expected error message in response body")
	}
}

// TestGetHistoricalMetrics_ValidMetric_Returns200WithData verifies that a valid
// metric param returns 200 and wraps the store data correctly.
func TestGetHistoricalMetrics_ValidMetric_Returns200WithData(t *testing.T) {
	points := []systemhealth.HistoricalPoint{
		{Timestamp: "2025-01-01T00:00:00Z", Value: 42.0},
		{Timestamp: "2025-01-01T01:00:00Z", Value: 55.5},
	}
	store := &mockStore{historicalData: points}

	h := newTestHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/system-health/history?metric=cpu_load_1m", nil)
	rec := httptest.NewRecorder()

	h.GetHistoricalMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body systemhealth.HistoricalMetrics
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Metric != "cpu_load_1m" {
		t.Errorf("metric: expected cpu_load_1m, got %s", body.Metric)
	}
	if len(body.Data) != len(points) {
		t.Errorf("data length: expected %d, got %d", len(points), len(body.Data))
	}
	if len(body.Data) > 0 && body.Data[0].Value != points[0].Value {
		t.Errorf("first data value: expected %f, got %f", points[0].Value, body.Data[0].Value)
	}
}

// TestCheckAIHealth_NoConfigReturnsUnhealthy verifies that the AI health check
// returns unhealthy when no AI config resolver is available.
func TestCheckAIHealth_NoConfigReturnsUnhealthy(t *testing.T) {
	// Create handler with a real (but empty) config resolver using a mock store
	// that returns no active provider.
	emptyStore := &noConfigStore{}
	resolver := ai.NewConfigResolver(emptyStore)
	svc := systemhealth.NewService(&mockStore{}, nil, nil, nil, nil, false)
	h := systemhealth.NewHandler(svc, resolver)

	req := httptest.NewRequest(http.MethodPost, "/api/system-health/ai-check", nil)
	rec := httptest.NewRecorder()

	h.CheckAIHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["status"] != "unhealthy" {
		t.Errorf("status: expected unhealthy, got %v", body["status"])
	}
	if body["error"] == "" {
		t.Error("expected error field to be non-empty")
	}
}
