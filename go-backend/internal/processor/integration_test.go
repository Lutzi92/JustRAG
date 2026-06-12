//go:build integration

// End-to-end ingestion-path test: parse → split → embed → store, plus the
// cross-file dedup and re-ingest idempotency behaviors that unit tests can't
// exercise because they span processor + vector + ai. The embedder is a fake
// OpenAI-compatible HTTP server returning deterministic 1536-dim vectors, so
// the test needs only the vector Postgres (TEST_VECTOR_DSN, same contract as
// the internal/vector integration tests) — no main DB, no Redis, no real LLM.
package processor_test

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/processor"
	"github.com/justrag/go-backend/internal/vector"
)

const embedDim = 1536 // matches the processor's dedup-table dimension, so cross-file dedup is active

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeEmbedder serves POST /embeddings with deterministic non-zero vectors
// derived from each input text, and counts every text it embeds so dedup
// assertions can verify which ingests actually hit the embedding API.
func fakeEmbedder(t *testing.T, embeddedTexts *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		resp := struct {
			Data []datum `json:"data"`
		}{Data: make([]datum, len(req.Input))}
		for i, text := range req.Input {
			embeddedTexts.Add(1)
			h := fnv.New64a()
			_, _ = h.Write([]byte(text))
			seed := h.Sum64()
			vec := make([]float64, embedDim)
			for j := range vec {
				// Deterministic, text-dependent, never the zero vector
				// (GenerateEmbeddings rejects zero vectors as provider bugs).
				vec[j] = 0.001 + float64((seed+uint64(j))%1000)/1000.0
			}
			resp.Data[i] = datum{Index: i, Embedding: vec}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeConfigStore satisfies ai.ConfigStore with a single active provider
// pointing at the fake embedder.
type fakeConfigStore struct {
	baseURL string
}

func (f *fakeConfigStore) GetActiveAIProvider(ctx context.Context) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{ID: "test-provider", Name: "test", APIKey: "test-key", BaseURL: f.baseURL}, nil
}

func (f *fakeConfigStore) GetAIProviderByID(ctx context.Context, id string) (*ai.AIProviderInfo, error) {
	return f.GetActiveAIProvider(ctx)
}

func (f *fakeConfigStore) GetAIModelsByProvider(ctx context.Context, providerID string) ([]ai.AIModelInfo, error) {
	return []ai.AIModelInfo{
		{Name: "fake-chat"},
		{Name: "fake-embed", IsEmbedding: true},
	}, nil
}

func (f *fakeConfigStore) GetKBModelOverrides(ctx context.Context, kbID string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

// fakeFileStore records status transitions and progress updates.
type fakeFileStore struct {
	mu       sync.Mutex
	statuses []string
	progress []int
}

func (f *fakeFileStore) UpdateFileStatus(ctx context.Context, fileID, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, status)
	return nil
}

func (f *fakeFileStore) UpdateFileProgress(ctx context.Context, fileID string, progress int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progress = append(f.progress, progress)
	return nil
}

func (f *fakeFileStore) MarkFileError(ctx context.Context, fileID, _, _ string) error {
	return f.UpdateFileStatus(ctx, fileID, "error")
}

func (f *fakeFileStore) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = nil
	f.progress = nil
}

func (f *fakeFileStore) snapshot() (statuses []string, progress []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.statuses...), append([]int(nil), f.progress...)
}

// fakeSiteConfig serves site_config keys from a map; unknown keys return nil
// (the "unset" answer), so every gated stage keeps its default except the
// ones the test overrides.
type fakeSiteConfig struct {
	values map[string]string
}

func (f *fakeSiteConfig) GetSiteConfigValue(ctx context.Context, key string) (*string, error) {
	if v, ok := f.values[key]; ok {
		return &v, nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Test infrastructure
// ---------------------------------------------------------------------------

func openVectorPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_VECTOR_DSN")
	if dsn == "" {
		t.Skip("TEST_VECTOR_DSN unset; skipping processor integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New(TEST_VECTOR_DSN): %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// writeTestDoc writes a plain-text document large enough to split into
// several chunks under the default splitter config (512-token chunks).
func writeTestDoc(t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < 60; i++ {
		sb.WriteString("Section marker alpha-")
		sb.WriteString(strings.Repeat("x", i%7+1))
		sb.WriteString(". The ingestion pipeline parses documents, splits them into chunks, ")
		sb.WriteString("generates embeddings for each chunk, and stores the vectors in the ")
		sb.WriteString("dimension-keyed chunk table together with a content hash for dedup.\n\n")
	}
	path := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write test doc: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// The test
// ---------------------------------------------------------------------------

func TestProcessFile_IngestEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool := openVectorPool(t)

	var embeddedTexts atomic.Int64
	embedSrv := fakeEmbedder(t, &embeddedTexts)

	resolver := ai.NewConfigResolver(&fakeConfigStore{baseURL: embedSrv.URL})
	chunkSvc := vector.NewChunkService(pool)
	fileStore := &fakeFileStore{}

	p := processor.NewProcessor(parser.DefaultFactoryWith(nil), resolver, chunkSvc, fileStore)
	p.SetSiteConfigReader(&fakeSiteConfig{values: map[string]string{
		// Enrichment defaults ON and would issue per-chunk LLM completion
		// calls; this test covers the embed path, not the enricher.
		"contextual_enrichment": "false",
	}})

	kbID := uuid.NewString()
	fileA := uuid.NewString()
	t.Cleanup(func() {
		if err := chunkSvc.DeleteChunksByKbID(context.Background(), kbID, embedDim); err != nil {
			t.Logf("cleanup DeleteChunksByKbID: %v", err)
		}
	})

	docPath := writeTestDoc(t)

	// --- Ingest file A: the happy path -----------------------------------
	if err := p.ProcessFile(ctx, processor.ProcessFileInput{
		FileID:   fileA,
		FilePath: docPath,
		FileName: "doc.txt",
		MimeType: "text/plain",
		KBID:     kbID,
	}); err != nil {
		t.Fatalf("ProcessFile(A): %v", err)
	}

	statuses, progress := fileStore.snapshot()
	if want := []string{"processing", "completed"}; len(statuses) != 2 || statuses[0] != want[0] || statuses[1] != want[1] {
		t.Errorf("file A statuses = %v, want %v", statuses, want)
	}
	if len(progress) == 0 || progress[len(progress)-1] != 100 {
		t.Errorf("file A final progress = %v, want last element 100", progress)
	}

	rowsA, err := chunkSvc.GetChunksByFileID(ctx, kbID, fileA, embedDim)
	if err != nil {
		t.Fatalf("GetChunksByFileID(A): %v", err)
	}
	if len(rowsA) < 2 {
		t.Fatalf("file A stored %d chunks, want >= 2 (test doc too small or splitter regression)", len(rowsA))
	}
	var joined strings.Builder
	for _, row := range rowsA {
		if strings.TrimSpace(row.Content) == "" {
			t.Errorf("file A chunk %s has empty content", row.ID)
		}
		joined.WriteString(row.Content)
	}
	if !strings.Contains(joined.String(), "dimension-keyed chunk table") {
		t.Error("stored chunk contents do not contain the source document text")
	}
	if got := embeddedTexts.Load(); got != int64(len(rowsA)) {
		t.Errorf("embedder saw %d texts for file A, want %d (one per stored chunk)", got, len(rowsA))
	}

	// Stored rows must carry a content hash and an embedding — both feed
	// retrieval (dedup + ANN) and neither is visible through FileChunkRow.
	var hashedAndEmbedded int
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM document_chunks
		  WHERE file_id = $1::uuid AND content_hash <> '' AND embedding IS NOT NULL`,
		fileA).Scan(&hashedAndEmbedded)
	if err != nil {
		t.Fatalf("count hashed+embedded rows: %v", err)
	}
	if hashedAndEmbedded != len(rowsA) {
		t.Errorf("%d/%d file A rows have content_hash + embedding", hashedAndEmbedded, len(rowsA))
	}

	// --- Ingest file B with identical content: cross-file dedup ----------
	fileB := uuid.NewString()
	fileStore.reset()
	embeddedBefore := embeddedTexts.Load()

	if err := p.ProcessFile(ctx, processor.ProcessFileInput{
		FileID:   fileB,
		FilePath: docPath,
		FileName: "doc-copy.txt",
		MimeType: "text/plain",
		KBID:     kbID,
	}); err != nil {
		t.Fatalf("ProcessFile(B): %v", err)
	}
	statuses, _ = fileStore.snapshot()
	if len(statuses) == 0 || statuses[len(statuses)-1] != "completed" {
		t.Errorf("file B statuses = %v, want final status completed", statuses)
	}
	if got := embeddedTexts.Load(); got != embeddedBefore {
		t.Errorf("file B triggered %d embedding calls, want 0 (all chunks are cross-file duplicates)", got-embeddedBefore)
	}
	rowsB, err := chunkSvc.GetChunksByFileID(ctx, kbID, fileB, embedDim)
	if err != nil {
		t.Fatalf("GetChunksByFileID(B): %v", err)
	}
	if len(rowsB) != 0 {
		t.Errorf("file B stored %d chunks, want 0 (dedup drops duplicates instead of re-pointing them)", len(rowsB))
	}

	// --- Re-ingest file A: idempotency (Asynq retry / user re-ingest) ----
	fileStore.reset()
	if err := p.ProcessFile(ctx, processor.ProcessFileInput{
		FileID:   fileA,
		FilePath: docPath,
		FileName: "doc.txt",
		MimeType: "text/plain",
		KBID:     kbID,
	}); err != nil {
		t.Fatalf("ProcessFile(A, re-ingest): %v", err)
	}
	rowsA2, err := chunkSvc.GetChunksByFileID(ctx, kbID, fileA, embedDim)
	if err != nil {
		t.Fatalf("GetChunksByFileID(A, re-ingest): %v", err)
	}
	if len(rowsA2) != len(rowsA) {
		t.Errorf("re-ingest of file A stored %d chunks, want %d (pre-ingest cleanup must replace, not append)", len(rowsA2), len(rowsA))
	}
}
