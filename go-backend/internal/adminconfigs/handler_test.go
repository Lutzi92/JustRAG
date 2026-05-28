package adminconfigs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/adminconfigs"
	"github.com/justrag/go-backend/internal/ai"
)

// ---------------------------------------------------------------------------
// mockStore is a test double for adminconfigs.Store.
// ---------------------------------------------------------------------------

var _ adminconfigs.Store = (*mockStore)(nil)

type mockStore struct {
	configs     []adminconfigs.AIConfigResponse
	created     *adminconfigs.AIConfigResponse
	updated     *adminconfigs.AIConfigResponse
	listErr     error
	createErr   error
	updateErr   error
	deleteErr   error
	activateErr error
	auditErr    error

	// inspection fields
	lastDeleteID    string
	lastActivateID  string
	lastAuditAction string
}

func (m *mockStore) ListAIConfigs(_ context.Context) ([]adminconfigs.AIConfigResponse, error) {
	return m.configs, m.listErr
}

func (m *mockStore) GetAIConfigByID(_ context.Context, _ string) (*adminconfigs.AIConfigResponse, error) {
	if len(m.configs) == 0 {
		return nil, nil
	}
	c := m.configs[0]
	return &c, nil
}

func (m *mockStore) GetAIProviderByID(_ context.Context, _ string) (*ai.AIProviderInfo, error) {
	if len(m.configs) == 0 {
		return nil, nil
	}
	c := m.configs[0]
	return &ai.AIProviderInfo{
		ID:      c.ID,
		Name:    c.Name,
		APIKey:  "sk-real-key",
		BaseURL: "https://api.openai.com/v1/",
	}, nil
}

func (m *mockStore) CreateAIConfig(_ context.Context, _ adminconfigs.CreateAIConfigInput) (*adminconfigs.AIConfigResponse, error) {
	return m.created, m.createErr
}

func (m *mockStore) UpdateAIConfig(_ context.Context, _ string, _ adminconfigs.UpdateAIConfigInput) (*adminconfigs.AIConfigResponse, error) {
	return m.updated, m.updateErr
}

func (m *mockStore) DeleteAIConfig(_ context.Context, id string) error {
	m.lastDeleteID = id
	return m.deleteErr
}

func (m *mockStore) ActivateAIConfig(_ context.Context, id string) error {
	m.lastActivateID = id
	return m.activateErr
}

func (m *mockStore) LogAuditAction(_ context.Context, _, action, _, _ string, _ any) error {
	m.lastAuditAction = action
	return m.auditErr
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newHandler(store *mockStore) *adminconfigs.Handler {
	return adminconfigs.NewHandler(store, nil)
}

func newRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func sampleConfig() adminconfigs.AIConfigResponse {
	return adminconfigs.AIConfigResponse{
		ID:       "cfg-1",
		Name:     "OpenAI",
		Provider: "openai",
		APIKey:   "sk-1**************234",
		ChatModels: []adminconfigs.AIModelRow{
			{ID: "m-1", Name: "gpt-4", IsReasoning: false},
		},
		EmbeddingModels: []adminconfigs.AIModelRow{},
		ReasoningModels: []adminconfigs.AIModelRow{},
		RerankModels:    []adminconfigs.AIModelRow{},
		TtsModels:       []adminconfigs.AIModelRow{},
		SttModels:       []adminconfigs.AIModelRow{},
		IsActive:        true,
		CreatedAt:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// Tests: ListConfigs
// ---------------------------------------------------------------------------

func TestListConfigs_OK(t *testing.T) {
	cfg := sampleConfig()
	store := &mockStore{configs: []adminconfigs.AIConfigResponse{cfg}}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/configs", nil)
	rr := httptest.NewRecorder()
	h.ListConfigs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var result []adminconfigs.AIConfigResponse
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 config, got %d", len(result))
	}
	if result[0].ID != "cfg-1" {
		t.Errorf("expected id cfg-1, got %s", result[0].ID)
	}
	if result[0].APIKey != "sk-1**************234" {
		t.Errorf("expected masked key, got %s", result[0].APIKey)
	}
}

func TestListConfigs_Empty(t *testing.T) {
	store := &mockStore{configs: []adminconfigs.AIConfigResponse{}}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/configs", nil)
	rr := httptest.NewRecorder()
	h.ListConfigs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if len(body) < 2 || body[0] != '[' {
		t.Errorf("expected JSON array, got: %s", body)
	}
}

func TestListConfigs_StoreError(t *testing.T) {
	store := &mockStore{listErr: errors.New("db error")}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/configs", nil)
	rr := httptest.NewRecorder()
	h.ListConfigs(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: CreateConfig
// ---------------------------------------------------------------------------

func TestCreateConfig_OK(t *testing.T) {
	cfg := sampleConfig()
	store := &mockStore{created: &cfg}
	h := newHandler(store)

	body := map[string]any{
		"name":     "OpenAI",
		"provider": "openai",
		"api_key":  "sk-1234567890abcdefgh",
		"chat_models": []map[string]any{
			{"name": "gpt-4", "isReasoning": false},
		},
		"embedding_models": []map[string]any{},
		"rerank_models":    []map[string]any{},
		"tts_models":       []map[string]any{},
		"stt_models":       []map[string]any{},
	}

	req := newRequest(http.MethodPost, "/api/admin/configs", body)
	rr := httptest.NewRecorder()
	h.CreateConfig(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var result adminconfigs.AIConfigResponse
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if result.ID != "cfg-1" {
		t.Errorf("expected id cfg-1, got %s", result.ID)
	}
	if store.lastAuditAction != "config.create" {
		t.Errorf("expected audit action config.create, got %s", store.lastAuditAction)
	}
}

func TestCreateConfig_MissingFields(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store)

	body := map[string]any{
		"name": "OpenAI",
		// missing provider and api_key
	}

	req := newRequest(http.MethodPost, "/api/admin/configs", body)
	rr := httptest.NewRecorder()
	h.CreateConfig(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestCreateConfig_InvalidJSON(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/configs", bytes.NewBufferString("not json"))
	rr := httptest.NewRecorder()
	h.CreateConfig(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestCreateConfig_StoreError(t *testing.T) {
	store := &mockStore{createErr: errors.New("db error")}
	h := newHandler(store)

	body := map[string]any{
		"name":     "OpenAI",
		"provider": "openai",
		"api_key":  "sk-1234567890abcdef",
	}

	req := newRequest(http.MethodPost, "/api/admin/configs", body)
	rr := httptest.NewRecorder()
	h.CreateConfig(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: DeleteConfig
// ---------------------------------------------------------------------------

func TestDeleteConfig_OK(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/configs/cfg-1", nil)
	req.SetPathValue("id", "cfg-1")
	rr := httptest.NewRecorder()
	h.DeleteConfig(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if store.lastDeleteID != "cfg-1" {
		t.Errorf("expected DeleteAIConfig called with cfg-1, got %s", store.lastDeleteID)
	}
	if store.lastAuditAction != "config.delete" {
		t.Errorf("expected audit action config.delete, got %s", store.lastAuditAction)
	}
}

func TestDeleteConfig_StoreError(t *testing.T) {
	store := &mockStore{deleteErr: errors.New("db error")}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/configs/cfg-1", nil)
	req.SetPathValue("id", "cfg-1")
	rr := httptest.NewRecorder()
	h.DeleteConfig(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: ActivateConfig
// ---------------------------------------------------------------------------

func TestActivateConfig_OK(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/configs/cfg-1/activate", nil)
	req.SetPathValue("id", "cfg-1")
	rr := httptest.NewRecorder()
	h.ActivateConfig(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var result map[string]bool
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !result["success"] {
		t.Errorf("expected success=true, got %v", result)
	}
	if store.lastActivateID != "cfg-1" {
		t.Errorf("expected ActivateAIConfig called with cfg-1, got %s", store.lastActivateID)
	}
	if store.lastAuditAction != "config.activate" {
		t.Errorf("expected audit action config.activate, got %s", store.lastAuditAction)
	}
}

func TestActivateConfig_StoreError(t *testing.T) {
	store := &mockStore{activateErr: errors.New("db error")}
	h := newHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/configs/cfg-1/activate", nil)
	req.SetPathValue("id", "cfg-1")
	rr := httptest.NewRecorder()
	h.ActivateConfig(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: UpdateConfig
// ---------------------------------------------------------------------------

func TestUpdateConfig_OK(t *testing.T) {
	cfg := sampleConfig()
	store := &mockStore{updated: &cfg}
	h := newHandler(store)

	newName := "OpenAI Updated"
	body := map[string]any{
		"name": newName,
	}

	req := newRequest(http.MethodPatch, "/api/admin/configs/cfg-1", body)
	req.SetPathValue("id", "cfg-1")
	rr := httptest.NewRecorder()
	h.UpdateConfig(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.lastAuditAction != "config.update" {
		t.Errorf("expected audit action config.update, got %s", store.lastAuditAction)
	}
}

func TestUpdateConfig_NotFound(t *testing.T) {
	store := &mockStore{updated: nil}
	h := newHandler(store)

	body := map[string]any{"name": "New Name"}

	req := newRequest(http.MethodPatch, "/api/admin/configs/nonexistent", body)
	req.SetPathValue("id", "nonexistent")
	rr := httptest.NewRecorder()
	h.UpdateConfig(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}
