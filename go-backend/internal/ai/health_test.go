package ai_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
)

// ---------------------------------------------------------------------------
// Minimal in-memory ConfigStore for tests
// ---------------------------------------------------------------------------

type stubStore struct {
	provider *ai.AIProviderInfo
	models   []ai.AIModelInfo
}

func (s *stubStore) GetActiveAIProvider(_ context.Context) (*ai.AIProviderInfo, error) {
	return s.provider, nil
}

func (s *stubStore) GetAIProviderByID(_ context.Context, _ string) (*ai.AIProviderInfo, error) {
	return nil, nil
}

func (s *stubStore) GetAIModelsByProvider(_ context.Context, _ string) ([]ai.AIModelInfo, error) {
	return s.models, nil
}

func (s *stubStore) GetKBModelOverrides(_ context.Context, _ string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCheckProviderHealth_Healthy(t *testing.T) {
	// Start a mock HTTP server that returns 200 for GET /models.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/models" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"object":"list","data":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	store := &stubStore{
		provider: &ai.AIProviderInfo{
			ID:      "p1",
			Name:    "TestProvider",
			APIKey:  "test-key",
			BaseURL: srv.URL,
		},
		models: []ai.AIModelInfo{
			{Name: "gpt-4o", IsReasoning: false},
		},
	}
	resolver := ai.NewConfigResolver(store)

	result, err := ai.CheckProviderHealth(context.Background(), resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "healthy" {
		t.Errorf("expected status healthy, got %q (error: %s)", result.Status, result.Error)
	}
	if result.ProviderName != "TestProvider" {
		t.Errorf("expected providerName TestProvider, got %q", result.ProviderName)
	}
	if result.Error != "" {
		t.Errorf("expected no error field, got %q", result.Error)
	}
}

func TestCheckProviderHealth_NoActiveConfig(t *testing.T) {
	// Store returns nil provider → no active AI provider configured.
	store := &stubStore{provider: nil}
	resolver := ai.NewConfigResolver(store)

	result, err := ai.CheckProviderHealth(context.Background(), resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "unhealthy" {
		t.Errorf("expected status unhealthy, got %q", result.Status)
	}
	if result.Error != "Kein aktiver KI-Anbieter konfiguriert" {
		t.Errorf("unexpected error message: %q", result.Error)
	}
}

func TestCheckProviderHealth_Unauthorized(t *testing.T) {
	// Mock server returns 401 for /models.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	defer srv.Close()

	store := &stubStore{
		provider: &ai.AIProviderInfo{
			ID:      "p2",
			Name:    "BadKeyProvider",
			APIKey:  "wrong-key",
			BaseURL: srv.URL,
		},
		models: []ai.AIModelInfo{
			{Name: "gpt-4o"},
		},
	}
	resolver := ai.NewConfigResolver(store)

	result, err := ai.CheckProviderHealth(context.Background(), resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "unhealthy" {
		t.Errorf("expected status unhealthy, got %q", result.Status)
	}
	if result.ProviderName != "BadKeyProvider" {
		t.Errorf("expected providerName BadKeyProvider, got %q", result.ProviderName)
	}
	if result.Error == "" {
		t.Error("expected non-empty error field")
	}
}
