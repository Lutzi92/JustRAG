package publicconfigs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/publicconfigs"
)

var _ publicconfigs.Store = (*mockStore)(nil)

// mockStore implements publicconfigs.Store for testing.
type mockStore struct {
	providers []publicconfigs.AIProvider
	models    []publicconfigs.AIModel
	err       error
}

func (m *mockStore) GetAIProviders(ctx context.Context) ([]publicconfigs.AIProvider, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.providers, nil
}

func (m *mockStore) GetAIModelsByProviderIDs(ctx context.Context, ids []string) ([]publicconfigs.AIModel, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.models, nil
}

// authenticatedRequest returns a request with a valid user in context.
func authenticatedRequest(t *testing.T, method, target string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	claims := &auth.Claims{ID: "user-1", Username: "tester", Role: "user"}
	ctx := auth.WithUser(req.Context(), claims)
	return req.WithContext(ctx)
}

func TestGetConfigs_WithProviders(t *testing.T) {
	store := &mockStore{
		providers: []publicconfigs.AIProvider{
			{ID: "prov-1", Name: "OpenAI", Provider: "openai", IsActive: true},
		},
		models: []publicconfigs.AIModel{
			{ID: "m1", ProviderID: "prov-1", Name: "gpt-4"},
			{ID: "m2", ProviderID: "prov-1", Name: "gpt-3.5-turbo"},
			{ID: "m3", ProviderID: "prov-1", Name: "text-embedding-3-large", IsEmbedding: true},
			{ID: "m4", ProviderID: "prov-1", Name: "o1-preview", IsReasoning: true},
			{ID: "m5", ProviderID: "prov-1", Name: "rerank-v3", IsRerank: true},
			{ID: "m6", ProviderID: "prov-1", Name: "tts-1", IsTts: true},
			{ID: "m7", ProviderID: "prov-1", Name: "whisper-1", IsStt: true},
		},
	}

	h := publicconfigs.NewHandler(store)
	req := authenticatedRequest(t, "GET", "/api/public/configs")
	rec := httptest.NewRecorder()
	h.GetConfigs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result []map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(result))
	}

	pr := result[0]

	if pr["id"] != "prov-1" {
		t.Errorf("expected id prov-1, got %v", pr["id"])
	}
	if pr["name"] != "OpenAI" {
		t.Errorf("expected name OpenAI, got %v", pr["name"])
	}
	if pr["provider"] != "openai" {
		t.Errorf("expected provider openai, got %v", pr["provider"])
	}
	if pr["is_active"] != true {
		t.Errorf("expected is_active true, got %v", pr["is_active"])
	}

	assertStringSlice(t, "chat_models", pr["chat_models"], []string{"gpt-4", "gpt-3.5-turbo", "o1-preview"})
	assertStringSlice(t, "embedding_models", pr["embedding_models"], []string{"text-embedding-3-large"})
	assertStringSlice(t, "rerank_models", pr["rerank_models"], []string{"rerank-v3"})
	assertStringSlice(t, "tts_models", pr["tts_models"], []string{"tts-1"})
	assertStringSlice(t, "reasoning_models", pr["reasoning_models"], []string{"o1-preview"})

	// whisper-1 (IsStt) must not appear anywhere
	chatModels := toStringSlice(pr["chat_models"])
	for _, m := range chatModels {
		if m == "whisper-1" {
			t.Error("whisper-1 (stt) must not appear in chat_models")
		}
	}
}

func TestGetConfigs_EmptyProviders(t *testing.T) {
	store := &mockStore{
		providers: []publicconfigs.AIProvider{},
		models:    []publicconfigs.AIModel{},
	}

	h := publicconfigs.NewHandler(store)
	req := authenticatedRequest(t, "GET", "/api/public/configs")
	rec := httptest.NewRecorder()
	h.GetConfigs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Must be a JSON array, not null.
	body := rec.Body.String()
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if raw[0] != '[' {
		t.Errorf("expected JSON array, got: %s", body)
	}

	var result []interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("could not decode as array: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty array, got %d items", len(result))
	}
}

func TestGetConfigs_Unauthenticated(t *testing.T) {
	store := &mockStore{}
	h := publicconfigs.NewHandler(store)

	req := httptest.NewRequest("GET", "/api/public/configs", nil)
	rec := httptest.NewRecorder()
	h.GetConfigs(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// --- helpers ---

func toStringSlice(v interface{}) []string {
	raw, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, len(raw))
	for i, s := range raw {
		out[i], _ = s.(string)
	}
	return out
}

func assertStringSlice(t *testing.T, field string, got interface{}, want []string) {
	t.Helper()
	gotSlice := toStringSlice(got)
	if len(gotSlice) != len(want) {
		t.Errorf("%s: expected %v, got %v", field, want, gotSlice)
		return
	}
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for _, g := range gotSlice {
		if !wantSet[g] {
			t.Errorf("%s: unexpected value %q (want %v)", field, g, want)
		}
	}
}
