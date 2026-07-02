package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/ai"
)

// dimsMockStore is a ConfigStore whose embedding model carries a configured
// dimensions value.
type dimsMockStore struct {
	baseURL string
	dims    int
}

func (s *dimsMockStore) GetActiveAIProvider(_ context.Context) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{ID: "prov-1", Name: "test", APIKey: "k", BaseURL: s.baseURL}, nil
}

func (s *dimsMockStore) GetAIProviderByID(_ context.Context, _ string) (*ai.AIProviderInfo, error) {
	return nil, nil
}

func (s *dimsMockStore) GetAIModelsByProvider(_ context.Context, _ string) ([]ai.AIModelInfo, error) {
	return []ai.AIModelInfo{
		{Name: "qwen3-embedding-8b", IsEmbedding: true, Dimensions: s.dims},
	}, nil
}

func (s *dimsMockStore) GetKBModelOverrides(_ context.Context, _ string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

// decodeEmbeddingBody parses the raw JSON request body into a generic map so
// tests can assert on the presence/absence of the dimensions field.
func decodeEmbeddingBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return body
}

// With a configured dimensions value, the embedding request must carry an
// OpenAI-style `dimensions` field.
func TestGenerateEmbeddings_SendsDimensions(t *testing.T) {
	var gotDims atomic.Value

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeEmbeddingBody(t, r)
		gotDims.Store(body["dimensions"])
		vec := make([]float64, 8)
		for i := range vec {
			vec[i] = 0.1
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{validEmbeddingData(0, vec)}))
	})

	resolver := ai.NewConfigResolver(&dimsMockStore{baseURL: srv.URL, dims: 8})
	got, err := ai.GenerateEmbeddings(context.Background(), resolver, []string{"hello"}, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0]) != 8 {
		t.Fatalf("expected one 8-dim embedding, got %v", got)
	}

	d, ok := gotDims.Load().(float64) // JSON numbers decode to float64
	if !ok {
		t.Fatalf("expected dimensions field in request body, got %v", gotDims.Load())
	}
	if int(d) != 8 {
		t.Errorf("expected dimensions 8 in request body, got %v", d)
	}
}

// With dimensions unset (0 = auto), the request body must NOT contain a
// dimensions field — providers that don't support it would 400.
func TestGenerateEmbeddings_OmitsDimensionsWhenUnset(t *testing.T) {
	var hadDims atomic.Bool

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeEmbeddingBody(t, r)
		_, present := body["dimensions"]
		hadDims.Store(present)
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, []float64{0.1, 0.2, 0.3}),
		}))
	})

	resolver := ai.NewConfigResolver(&dimsMockStore{baseURL: srv.URL, dims: 0})
	if _, err := ai.GenerateEmbeddings(context.Background(), resolver, []string{"hello"}, "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hadDims.Load() {
		t.Error("dimensions field must be omitted when no dimensions are configured")
	}
}

// A backend that ignores the dimensions parameter (returns native-size
// vectors) must produce a deterministic error, not silently store the wrong
// size — and must not burn retries on it.
func TestGenerateEmbeddings_DimensionMismatchFailsWithoutRetry(t *testing.T) {
	var calls atomic.Int32

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// 3-dim vector although 8 were requested.
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, []float64{0.1, 0.2, 0.3}),
		}))
	})

	resolver := ai.NewConfigResolver(&dimsMockStore{baseURL: srv.URL, dims: 8})
	_, err := ai.GenerateEmbeddings(context.Background(), resolver, []string{"hello"}, "", nil)
	if err == nil {
		t.Fatal("expected dimension-mismatch error, got nil")
	}

	var dme *ai.DimensionMismatchError
	if !errors.As(err, &dme) {
		t.Fatalf("expected *ai.DimensionMismatchError, got %T: %v", err, err)
	}
	if dme.Want != 8 || dme.Got != 3 {
		t.Errorf("expected Want=8 Got=3, got Want=%d Got=%d", dme.Want, dme.Got)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 request (deterministic failure, no retry), got %d", n)
	}
}

// Cached vectors of the model's native size (stored before dimensions were
// configured) must NOT be served once a truncated size is configured: the
// cache key has to be dims-scoped. Conversely the truncated result must be
// cached and hit on the second call.
func TestGenerateEmbeddings_CacheKeyIsDimensionScoped(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	cache := ai.NewEmbeddingCache(rdb, time.Hour)

	const model = "qwen3-embedding-8b"
	const text = "hello"

	// Seed a stale native-size (3-dim) vector under the un-scoped key, as an
	// old build without configured dimensions would have written it.
	cache.Set(context.Background(), model, text, []float64{9.9, 9.9, 9.9})

	var calls atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		vec := make([]float64, 8)
		for i := range vec {
			vec[i] = 0.5
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{validEmbeddingData(0, vec)}))
	})

	resolver := ai.NewConfigResolver(&dimsMockStore{baseURL: srv.URL, dims: 8})

	// First call: the stale 3-dim entry must be invisible → API is hit.
	got, err := ai.GenerateEmbeddings(context.Background(), resolver, []string{text}, "", cache)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got[0]) != 8 {
		t.Fatalf("expected 8-dim embedding from API, got %d-dim (stale cache entry served?)", len(got[0]))
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("expected 1 API call (stale un-scoped entry must not hit), got %d", n)
	}

	// Second call: the 8-dim result must now be served from cache.
	// The write is fire-and-forget; poll briefly until it lands.
	deadline := time.Now().Add(2 * time.Second)
	for {
		calls.Store(1)
		got, err = ai.GenerateEmbeddings(context.Background(), resolver, []string{text}, "", cache)
		if err != nil {
			t.Fatalf("unexpected error on second call: %v", err)
		}
		if calls.Load() == 1 {
			break // cache hit — no extra API call
		}
		if time.Now().After(deadline) {
			t.Fatal("dims-scoped cache entry never became readable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got[0]) != 8 {
		t.Fatalf("expected cached 8-dim embedding, got %d-dim", len(got[0]))
	}
}

// The late-chunking path must carry the same dimensions parameter and reject
// wrong-size responses — it writes into the same dim-keyed vector tables.
func TestGenerateEmbeddingsLateChunked_SendsDimensions(t *testing.T) {
	var gotDims atomic.Value

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeEmbeddingBody(t, r)
		gotDims.Store(body["dimensions"])
		vec := make([]float64, 8)
		for i := range vec {
			vec[i] = 0.5
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{validEmbeddingData(0, vec)}))
	})

	resolver := ai.NewConfigResolver(&dimsMockStore{baseURL: srv.URL, dims: 8})
	got, err := ai.GenerateEmbeddingsLateChunked(context.Background(), resolver, []string{"chunk one"}, "", 8192)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0]) != 8 {
		t.Fatalf("expected one 8-dim embedding, got %v", got)
	}

	d, ok := gotDims.Load().(float64)
	if !ok {
		t.Fatalf("expected dimensions field in late-chunking request body, got %v", gotDims.Load())
	}
	if int(d) != 8 {
		t.Errorf("expected dimensions 8 in request body, got %v", d)
	}
}

// A 4xx from the provider is deterministic (e.g. vLLM rejecting the
// dimensions parameter for a non-Matryoshka-declared model) — retrying burns
// 2s/4s backoff per text batch for the same guaranteed failure. Only 408/429
// remain retryable among client errors.
func TestGenerateEmbeddings_BadRequestIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"does not support matryoshka representation"}}`))
	})

	resolver := ai.NewConfigResolver(&dimsMockStore{baseURL: srv.URL, dims: 8})
	_, err := ai.GenerateEmbeddings(context.Background(), resolver, []string{"hello"}, "", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 request for a 400 (deterministic), got %d", n)
	}
}

// 429 (rate limit) stays retryable — it is transient, unlike other 4xx.
func TestGenerateEmbeddings_RateLimitIsRetried(t *testing.T) {
	var calls atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`rate limited`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, []float64{0.1, 0.2, 0.3}),
		}))
	})

	resolver := ai.NewConfigResolver(&dimsMockStore{baseURL: srv.URL, dims: 0})
	got, err := ai.GenerateEmbeddings(context.Background(), resolver, []string{"hello"}, "", nil)
	if err != nil {
		t.Fatalf("expected retry to succeed after 429, got error: %v", err)
	}
	if len(got) != 1 || len(got[0]) != 3 {
		t.Fatalf("expected one 3-dim embedding, got %v", got)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("expected 2 requests (429 then success), got %d", n)
	}
}

func TestGenerateEmbeddingsLateChunked_DimensionMismatchFails(t *testing.T) {
	var calls atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse([]map[string]any{
			validEmbeddingData(0, []float64{0.1, 0.2, 0.3}),
		}))
	})

	resolver := ai.NewConfigResolver(&dimsMockStore{baseURL: srv.URL, dims: 8})
	_, err := ai.GenerateEmbeddingsLateChunked(context.Background(), resolver, []string{"chunk one"}, "", 8192)
	if err == nil {
		t.Fatal("expected dimension-mismatch error, got nil")
	}
	var dme *ai.DimensionMismatchError
	if !errors.As(err, &dme) {
		t.Fatalf("expected *ai.DimensionMismatchError, got %T: %v", err, err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 request (no retry), got %d", n)
	}
}
