package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
)

// ---------------------------------------------------------------------------
// Mock ConfigStore for embedding tests
// ---------------------------------------------------------------------------

type embeddingMockStore struct {
	baseURL string
}

func (s *embeddingMockStore) GetActiveAIProvider(_ context.Context) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{
		ID:      "prov-1",
		Name:    "test-provider",
		APIKey:  "test-key",
		BaseURL: s.baseURL,
	}, nil
}

func (s *embeddingMockStore) GetAIProviderByID(_ context.Context, _ string) (*ai.AIProviderInfo, error) {
	return nil, nil
}

func (s *embeddingMockStore) GetAIModelsByProvider(_ context.Context, _ string) ([]ai.AIModelInfo, error) {
	return []ai.AIModelInfo{
		{Name: "text-embedding-3-small", IsEmbedding: true},
	}, nil
}

func (s *embeddingMockStore) GetKBModelOverrides(_ context.Context, _ string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

// newEmbeddingResolver creates a ConfigResolver backed by embeddingMockStore
// pointing at the given base URL (typically an httptest.Server URL).
func newEmbeddingResolver(baseURL string) *ai.ConfigResolver {
	return ai.NewConfigResolver(&embeddingMockStore{baseURL: baseURL})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// embeddingResponse builds a JSON embedding response body.
func embeddingResponse(data []map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"data": data})
	return b
}

func validEmbeddingData(index int, vec []float64) map[string]any {
	return map[string]any{"index": index, "embedding": vec}
}

// ---------------------------------------------------------------------------
// GenerateEmbedding (single) tests
// ---------------------------------------------------------------------------

func TestGenerateEmbedding_ValidVector(t *testing.T) {
	want := []float64{0.1, 0.2, 0.3}

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, want),
		}))
	})

	resolver := newEmbeddingResolver(srv.URL)
	got, err := ai.GenerateEmbedding(context.Background(), resolver, "hello world", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected embedding length %d, got %d", len(want), len(got))
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("embedding[%d]: want %f, got %f", i, v, got[i])
		}
	}
}

func TestGenerateEmbedding_ZeroVectorReturnsError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, []float64{0.0, 0.0, 0.0}),
		}))
	})

	resolver := newEmbeddingResolver(srv.URL)
	_, err := ai.GenerateEmbedding(context.Background(), resolver, "hello world", "", nil)
	if err == nil {
		t.Fatal("expected ZeroVectorError, got nil")
	}

	var zve *ai.ZeroVectorError
	if !errors.As(err, &zve) {
		t.Fatalf("expected *ai.ZeroVectorError, got %T: %v", err, err)
	}
	if zve.Index != 0 {
		t.Errorf("expected Index 0, got %d", zve.Index)
	}
	if zve.Model == "" {
		t.Error("ZeroVectorError.Model should not be empty")
	}
}

// ---------------------------------------------------------------------------
// GenerateEmbeddings (batch) tests
// ---------------------------------------------------------------------------

func TestGenerateEmbeddings_ReturnsCorrectCount(t *testing.T) {
	texts := []string{"alpha", "beta", "gamma"}

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, []float64{0.1, 0.2}),
			validEmbeddingData(1, []float64{0.3, 0.4}),
			validEmbeddingData(2, []float64{0.5, 0.6}),
		}))
	})

	resolver := newEmbeddingResolver(srv.URL)
	got, err := ai.GenerateEmbeddings(context.Background(), resolver, texts, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(texts) {
		t.Fatalf("expected %d embeddings, got %d", len(texts), len(got))
	}
	// Verify order is preserved.
	if got[0][0] != 0.1 {
		t.Errorf("expected got[0][0] == 0.1, got %f", got[0][0])
	}
	if got[2][0] != 0.5 {
		t.Errorf("expected got[2][0] == 0.5, got %f", got[2][0])
	}
}

func TestGenerateEmbeddings_ZeroVectorInBatchReturnsErrorWithoutRetry(t *testing.T) {
	var callCount atomic.Int32

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Second embedding is a zero vector.
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, []float64{0.1, 0.2}),
			validEmbeddingData(1, []float64{0.0, 0.0}),
		}))
	})

	resolver := newEmbeddingResolver(srv.URL)
	_, err := ai.GenerateEmbeddings(context.Background(), resolver, []string{"a", "b"}, "", nil)
	if err == nil {
		t.Fatal("expected ZeroVectorError, got nil")
	}

	var zve *ai.ZeroVectorError
	if !errors.As(err, &zve) {
		t.Fatalf("expected *ai.ZeroVectorError, got %T: %v", err, err)
	}
	if zve.Index != 1 {
		t.Errorf("expected Index 1, got %d", zve.Index)
	}

	// Must NOT have retried.
	if n := callCount.Load(); n != 1 {
		t.Errorf("expected exactly 1 API call (no retry), got %d", n)
	}
}

func TestGenerateEmbeddings_TransientErrorRetriesAndSucceeds(t *testing.T) {
	var callCount atomic.Int32

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n < 3 {
			// First two attempts fail with a 500.
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"transient failure"}`))
			return
		}
		// Third attempt succeeds.
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, []float64{0.7, 0.8, 0.9}),
		}))
	})

	resolver := newEmbeddingResolver(srv.URL)
	// Use a context with a generous deadline so backoff sleeps (skipped in
	// test via short durations) don't time out. The actual sleep is replaced
	// by the test completing quickly because the mock server responds instantly.
	got, err := ai.GenerateEmbeddings(context.Background(), resolver, []string{"text"}, "", nil)
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(got))
	}
	if got[0][0] != 0.7 {
		t.Errorf("expected got[0][0] == 0.7, got %f", got[0][0])
	}
	if n := callCount.Load(); n != 3 {
		t.Errorf("expected 3 API calls (2 failures + 1 success), got %d", n)
	}
}

// ---------------------------------------------------------------------------
// BuildQueryInput / EncodeQuery tests (Qwen3-Embedding instruction prefix)
// ---------------------------------------------------------------------------

func TestBuildQueryInput_EmptyInstructionPassesThrough(t *testing.T) {
	got := ai.BuildQueryInput("hello world", "")
	if got != "hello world" {
		t.Errorf("empty instruction should pass query through unchanged, got %q", got)
	}
}

func TestBuildQueryInput_AppliesQwen3Template(t *testing.T) {
	got := ai.BuildQueryInput("Wer ist Koordinator?", "Given a web search query, retrieve relevant passages that answer the query")
	want := "Instruct: Given a web search query, retrieve relevant passages that answer the query\nQuery: Wer ist Koordinator?"
	if got != want {
		t.Errorf("unexpected template:\n  got:  %q\n  want: %q", got, want)
	}
}

// TestEncodeQuery_SendsWrappedInputToEmbeddingServer verifies that the
// instruction prefix survives all the way to the HTTP request body — this is
// the contract that lets Qwen3-Embedding apply its trained query-side encoding.
// Cache correctness falls out for free: the cache keys on this wrapped string,
// so query-side and document-side encodings of the same text stay separate.
func TestEncodeQuery_SendsWrappedInputToEmbeddingServer(t *testing.T) {
	var gotInput string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// GenerateEmbedding now delegates to the batch path which sends
		// input as []string rather than a bare string.
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if len(req.Input) > 0 {
			gotInput = req.Input[0]
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, []float64{0.1, 0.2, 0.3}),
		}))
	})

	resolver := newEmbeddingResolver(srv.URL)
	_, err := ai.EncodeQuery(context.Background(), resolver, "Wer ist Koordinator?", "Given a web search query, retrieve relevant passages that answer the query", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasPrefix(gotInput, "Instruct: ") {
		t.Errorf("embedding server received non-wrapped input: %q", gotInput)
	}
	if !strings.Contains(gotInput, "\nQuery: Wer ist Koordinator?") {
		t.Errorf("wrapped input missing Query suffix: %q", gotInput)
	}
}

func TestEncodeQuery_EmptyInstructionSendsBareQuery(t *testing.T) {
	var gotInput string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// GenerateEmbedding now delegates to the batch path which sends
		// input as []string rather than a bare string.
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if len(req.Input) > 0 {
			gotInput = req.Input[0]
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, []float64{0.1, 0.2, 0.3}),
		}))
	})

	resolver := newEmbeddingResolver(srv.URL)
	_, err := ai.EncodeQuery(context.Background(), resolver, "hello world", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotInput != "hello world" {
		t.Errorf("empty instruction should send bare query, got %q", gotInput)
	}
}

func TestGenerateEmbeddings_AllRetriesFailReturnsError(t *testing.T) {
	var callCount atomic.Int32

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"service unavailable"}`))
	})

	resolver := newEmbeddingResolver(srv.URL)
	_, err := ai.GenerateEmbeddings(context.Background(), resolver, []string{"text"}, "", nil)
	if err == nil {
		t.Fatal("expected error after all retries fail, got nil")
	}

	if n := callCount.Load(); n != 3 {
		t.Errorf("expected exactly 3 API calls, got %d", n)
	}
}

// TestGenerateEmbedding_RetriesTransient verifies that a transient server error
// on the first attempt is retried by GenerateEmbedding via the batch path.
// The server returns 500 on the first request and a valid embedding on the second.
// Expect: err == nil, correct vector returned, server received exactly 2 requests.
// NOTE: the batch retry backoff is 2s between attempts, so this test takes ~2s.
func TestGenerateEmbedding_RetriesTransient(t *testing.T) {
	var callCount atomic.Int32
	want := []float64{0.11, 0.22, 0.33}

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			// First request: transient 500
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"transient failure"}`))
			return
		}
		// Second request: valid response
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, want),
		}))
	})

	resolver := newEmbeddingResolver(srv.URL)
	got, err := ai.GenerateEmbedding(context.Background(), resolver, "hello", "kb-1", nil)
	if err != nil {
		t.Fatalf("expected no error after retry, got: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected vector length %d, got %d", len(want), len(got))
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("embedding[%d]: want %f, got %f", i, v, got[i])
		}
	}
	if n := callCount.Load(); n != 2 {
		t.Errorf("expected exactly 2 server requests (1 fail + 1 retry), got %d", n)
	}
}
