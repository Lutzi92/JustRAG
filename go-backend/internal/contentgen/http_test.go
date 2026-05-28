package contentgen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/contentgen"
	"github.com/justrag/go-backend/internal/gencontent"
	"github.com/justrag/go-backend/internal/vector"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	testKBID   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	testUserID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var (
	_ contentgen.Store         = (*mockStore)(nil)
	_ contentgen.Searcher      = (*mockSearcher)(nil)
	_ ai.ConfigStore           = (*mockConfigStore)(nil)
	_ contentgen.AsynqEnqueuer = (*mockAsynqClient)(nil)
)

type mockStore struct {
	created *gencontent.GenContentRow
	err     error
}

func (m *mockStore) CreateGeneratedContent(_ context.Context, kbID, userID, contentType, title string, content any) (*gencontent.GenContentRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.created != nil {
		return m.created, nil
	}
	return &gencontent.GenContentRow{
		ID:        "cccccccc-cccc-cccc-cccc-cccccccccccc",
		KbID:      kbID,
		UserID:    userID,
		Type:      contentType,
		Title:     title,
		Content:   content,
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

// ---------------------------------------------------------------------------
// Mock searcher
// ---------------------------------------------------------------------------

type mockSearcher struct {
	chunks []vector.SearchChunk
	err    error
}

func (m *mockSearcher) Search(_ context.Context, _, _ string, _ int, _ vector.SearchOptions) (*vector.SearchResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &vector.SearchResult{Chunks: m.chunks}, nil
}

// defaultSearcher returns a mock searcher with one sample chunk.
func defaultSearcher() *mockSearcher {
	return &mockSearcher{
		chunks: []vector.SearchChunk{
			{
				ID:       "chunk-1",
				Content:  "This is test content about the topic.",
				FileID:   "file-1",
				FileName: "test.pdf",
				Score:    0.9,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Mock AI config store (for building a ConfigResolver)
// ---------------------------------------------------------------------------

type mockConfigStore struct {
	baseURL string
	model   string
}

func (m *mockConfigStore) GetActiveAIProvider(_ context.Context) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{
		ID:      "prov-test",
		Name:    "Test",
		APIKey:  "test-key",
		BaseURL: m.baseURL,
	}, nil
}

func (m *mockConfigStore) GetAIProviderByID(_ context.Context, _ string) (*ai.AIProviderInfo, error) {
	return nil, nil
}

func (m *mockConfigStore) GetAIModelsByProvider(_ context.Context, _ string) ([]ai.AIModelInfo, error) {
	return []ai.AIModelInfo{{Name: m.model}}, nil
}

func (m *mockConfigStore) GetKBModelOverrides(_ context.Context, _ string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// LLM test server helpers
// ---------------------------------------------------------------------------

// buildLLMServer creates an httptest server that returns content as a chat
// completion response. It handles POST /chat/completions.
func buildLLMServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
		}
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// resolverFor creates a ConfigResolver pointing at the given test server.
func resolverFor(srv *httptest.Server) *ai.ConfigResolver {
	cs := &mockConfigStore{baseURL: srv.URL, model: "gpt-test"}
	return ai.NewConfigResolver(cs)
}

// ---------------------------------------------------------------------------
// Request helpers
// ---------------------------------------------------------------------------

func withAuth(r *http.Request) *http.Request {
	claims := &auth.Claims{ID: testUserID, Role: "user"}
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

func postJSON(t *testing.T, path string, body any) *http.Request {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.SetPathValue("id", testKBID)
	return withAuth(req)
}

// ---------------------------------------------------------------------------
// Tests: GenerateCards
// ---------------------------------------------------------------------------

func TestGenerateCards_Returns200(t *testing.T) {
	llmContent := `[{"front": "What is X?", "back": "X is Y."}]`
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/cards", map[string]string{"topic": "test topic"})
	rec := httptest.NewRecorder()
	h.GenerateCards(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp gencontent.GenContentRow
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Type != "flashcards" {
		t.Errorf("expected type flashcards, got %s", resp.Type)
	}
}

func TestGenerateCards_MissingTopic_Returns400(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/cards", map[string]string{"topic": ""})
	rec := httptest.NewRecorder()
	h.GenerateCards(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGenerateCards_NoAuth_Returns401(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)

	body, _ := json.Marshal(map[string]string{"topic": "test"})
	req := httptest.NewRequest(http.MethodPost, "/api/kb/"+testKBID+"/generate/cards", bytes.NewReader(body))
	req.SetPathValue("id", testKBID)
	// no auth injected
	rec := httptest.NewRecorder()
	h.GenerateCards(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: GenerateAnalysis
// ---------------------------------------------------------------------------

func TestGenerateAnalysis_Returns200(t *testing.T) {
	llmContent := "## Analysis\n\nThis is a detailed analysis."
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/analysis", map[string]string{"topic": "test analysis"})
	rec := httptest.NewRecorder()
	h.GenerateAnalysis(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp gencontent.GenContentRow
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Type != "analysis" {
		t.Errorf("expected type analysis, got %s", resp.Type)
	}
}

func TestGenerateAnalysis_MissingTopic_Returns400(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/analysis", map[string]string{})
	rec := httptest.NewRecorder()
	h.GenerateAnalysis(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: GenerateAbstract
// ---------------------------------------------------------------------------

func TestGenerateAbstract_Returns200(t *testing.T) {
	llmContent := "Background: ... Methods: ... Results: ... Conclusions: ..."
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/abstract", map[string]string{
		"fileId":       "file-1",
		"abstractType": "academic",
		"language":     "en",
	})
	rec := httptest.NewRecorder()
	h.GenerateAbstract(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp gencontent.GenContentRow
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Type != "abstract" {
		t.Errorf("expected type abstract, got %s", resp.Type)
	}
}

func TestGenerateAbstract_MissingFileID_Returns400(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/abstract", map[string]string{
		"abstractType": "academic",
	})
	rec := httptest.NewRecorder()
	h.GenerateAbstract(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: GenerateChart
// ---------------------------------------------------------------------------

func TestGenerateChart_Returns501(t *testing.T) {
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/chart", map[string]string{"topic": "test"})
	rec := httptest.NewRecorder()
	h.GenerateChart(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["error"] == "" {
		t.Error("expected non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// Tests: GeneratePresentation
// ---------------------------------------------------------------------------

func TestGeneratePresentation_Returns200(t *testing.T) {
	llmContent := `{"title":"Test Presentation","slides":[{"title":"Slide 1","content":["Point A"],"speakerNotes":"..."}]}`
	srv := buildLLMServer(t, llmContent)

	h := contentgen.NewHandler(&mockStore{}, resolverFor(srv), defaultSearcher(), nil, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/presentation", map[string]string{"topic": "test pres"})
	rec := httptest.NewRecorder()
	h.GeneratePresentation(rec, req)

	// Clean up generated PPTX file (written under data/)
	t.Cleanup(func() { os.RemoveAll("data") })

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp gencontent.GenContentRow
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Type != "presentation" {
		t.Errorf("expected type presentation, got %s", resp.Type)
	}
}

// ---------------------------------------------------------------------------
// Tests: GeneratePodcast
// ---------------------------------------------------------------------------

// mockAsynqClient satisfies contentgen.AsynqEnqueuer for tests.
type mockAsynqClient struct {
	lastPayload []byte
}

func (m *mockAsynqClient) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	m.lastPayload = task.Payload()
	return &asynq.TaskInfo{ID: "test-job-id"}, nil
}

func TestGeneratePodcast_Returns202(t *testing.T) {
	ac := &mockAsynqClient{}
	h := contentgen.NewHandler(&mockStore{}, nil, defaultSearcher(), ac, nil, nil)

	req := postJSON(t, "/api/kb/"+testKBID+"/generate/podcast", map[string]string{"topic": "test podcast"})
	rec := httptest.NewRecorder()
	h.GeneratePodcast(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["jobId"] == "" {
		t.Error("expected non-empty jobId")
	}
}
