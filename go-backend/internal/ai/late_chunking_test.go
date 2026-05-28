package ai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
)

// TestEmbeddingRequest_LateChunkingFieldOmitted confirms the new field is
// elided from the request body when false. Providers that don't understand
// `late_chunking` should not see an unexpected JSON key when callers opt
// out (the existing GenerateEmbedding path).
func TestEmbeddingRequest_LateChunkingFieldOmitted(t *testing.T) {
	body, err := json.Marshal(&ai.EmbeddingRequest{
		Model: "test-model",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "late_chunking") {
		t.Errorf("unexpected late_chunking key when field is false: %s", body)
	}
}

// TestEmbeddingRequest_LateChunkingFieldEmittedWhenTrue confirms callers
// can opt in by setting the new field to true.
func TestEmbeddingRequest_LateChunkingFieldEmittedWhenTrue(t *testing.T) {
	body, err := json.Marshal(&ai.EmbeddingRequest{
		Model:        "test-model",
		Input:        []string{"a", "b"},
		LateChunking: true,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"late_chunking":true`) {
		t.Errorf("late_chunking flag missing from body: %s", body)
	}
}

// TestGenerateEmbeddingsLateChunked_ForwardsFlagAndReturnsVectorsInOrder
// verifies the public helper sends late_chunking=true to the server, that
// the captured request body lists the chunks in document order, and that
// the returned slice maps each chunk index to its server-returned vector.
func TestGenerateEmbeddingsLateChunked_ForwardsFlagAndReturnsVectorsInOrder(t *testing.T) {
	var capturedFlag atomic.Bool
	var capturedInputs atomic.Value // []string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var req struct {
			Model        string   `json:"model"`
			Input        []string `json:"input"`
			LateChunking bool     `json:"late_chunking"`
		}
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			t.Errorf("request body parse: %v", err)
		}
		capturedFlag.Store(req.LateChunking)
		capturedInputs.Store(req.Input)

		// Echo the inputs back as embeddings: vec[0]==len(chunk) so the
		// test can map each returned vector to its source chunk.
		data := make([]map[string]any, len(req.Input))
		for i, s := range req.Input {
			data[i] = validEmbeddingData(i, []float64{float64(len(s)), 0.0, 0.0})
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse(data))
	})

	resolver := newEmbeddingResolver(srv.URL)
	chunks := []string{"alpha", "be", "gamma_long"}
	// maxInputTokens=10_000 — all chunks fit in one window.
	vecs, err := ai.GenerateEmbeddingsLateChunked(context.Background(), resolver, chunks, "", 10_000)
	if err != nil {
		t.Fatalf("GenerateEmbeddingsLateChunked: %v", err)
	}

	if !capturedFlag.Load() {
		t.Errorf("server did not see late_chunking=true")
	}
	gotInputs, _ := capturedInputs.Load().([]string)
	if len(gotInputs) != len(chunks) {
		t.Fatalf("server got %d inputs, want %d", len(gotInputs), len(chunks))
	}
	for i := range chunks {
		if gotInputs[i] != chunks[i] {
			t.Errorf("input[%d] = %q, want %q (document order violated)", i, gotInputs[i], chunks[i])
		}
	}

	if len(vecs) != len(chunks) {
		t.Fatalf("got %d vectors, want %d", len(vecs), len(chunks))
	}
	for i, c := range chunks {
		if vecs[i][0] != float64(len(c)) {
			t.Errorf("vec[%d][0] = %v, want %v (vector→chunk mapping wrong)", i, vecs[i][0], float64(len(c)))
		}
	}
}

// TestGenerateEmbeddingsLateChunked_SplitsIntoWindowsByTokenBudget feeds
// many small chunks and a tight token budget; expects more than one HTTP
// call, with the inputs partitioned in document order across calls.
func TestGenerateEmbeddingsLateChunked_SplitsIntoWindowsByTokenBudget(t *testing.T) {
	var callCount atomic.Int32
	var capturedBatches atomic.Value // [][]string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var req struct {
			Input        []string `json:"input"`
			LateChunking bool     `json:"late_chunking"`
		}
		_ = json.Unmarshal(bodyBytes, &req)
		if !req.LateChunking {
			t.Errorf("call %d missing late_chunking flag", callCount.Load())
		}
		// Append this call's input slice to the running list.
		prev, _ := capturedBatches.Load().([][]string)
		next := make([][]string, 0, len(prev)+1)
		next = append(next, prev...)
		next = append(next, req.Input)
		capturedBatches.Store(next)
		callCount.Add(1)

		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[i] = validEmbeddingData(i, []float64{1.0, 0.0})
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse(data))
	})

	resolver := newEmbeddingResolver(srv.URL)
	// Each "word_N" is roughly 2 tokens under cl100k_base; 12 such chunks
	// with maxInputTokens=4 forces splitting into multiple windows. The
	// exact window count depends on the tokenizer, but it MUST be >1.
	chunks := make([]string, 12)
	for i := range chunks {
		chunks[i] = "word_xyz_" + string(rune('a'+i))
	}
	_, err := ai.GenerateEmbeddingsLateChunked(context.Background(), resolver, chunks, "", 4)
	if err != nil {
		t.Fatalf("GenerateEmbeddingsLateChunked: %v", err)
	}

	if callCount.Load() < 2 {
		t.Errorf("expected >1 HTTP calls under tight budget, got %d", callCount.Load())
	}

	// Verify the union of all batches covers every chunk in document
	// order — no chunk dropped or re-ordered.
	batches, _ := capturedBatches.Load().([][]string)
	var flat []string
	for _, b := range batches {
		flat = append(flat, b...)
	}
	if len(flat) != len(chunks) {
		t.Fatalf("total inputs across windows = %d, want %d", len(flat), len(chunks))
	}
	for i := range chunks {
		if flat[i] != chunks[i] {
			t.Errorf("input[%d] (across windows) = %q, want %q", i, flat[i], chunks[i])
		}
	}
}

// TestGenerateEmbeddingsLateChunked_OversizeChunkGetsItsOwnWindow exercises
// the packLateChunkingWindows fallback: a single chunk larger than the
// per-call budget must still be sent (not silently dropped) — on its own.
func TestGenerateEmbeddingsLateChunked_OversizeChunkGetsItsOwnWindow(t *testing.T) {
	var callCount atomic.Int32
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.Unmarshal(bodyBytes, &req)
		callCount.Add(1)
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[i] = validEmbeddingData(i, []float64{1.0, 0.0})
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(embeddingResponse(data))
	})

	resolver := newEmbeddingResolver(srv.URL)
	// One small chunk + one huge chunk (definitely > 4 tokens cl100k_base).
	huge := strings.Repeat("oversized content for the embedder window. ", 200)
	chunks := []string{"small", huge}
	vecs, err := ai.GenerateEmbeddingsLateChunked(context.Background(), resolver, chunks, "", 4)
	if err != nil {
		t.Fatalf("GenerateEmbeddingsLateChunked: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vecs))
	}
	if callCount.Load() < 2 {
		t.Errorf("oversize chunk did not trigger its own window: %d calls", callCount.Load())
	}
}
