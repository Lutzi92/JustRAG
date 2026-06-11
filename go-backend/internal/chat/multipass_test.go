package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

// multipassTestServer wires an httptest server + ConfigResolver pointed at it
// so we can drive RunMultipassExtraction through the real
// GenerateCompletionWithModel path. The handler decides per-request what to
// return, so a single server can produce NONE for one chunk and verbatim
// extraction for another.
func multipassTestServer(t *testing.T, handler http.HandlerFunc) *ai.ConfigResolver {
	t.Helper()
	// Use a bare HandlerFunc rather than a ServeMux: the chatTestConfigStore
	// hands the client a baseURL of `${srv}/v1/`, so requests land at paths
	// like `/v1/chat/completions`. A mux registered on `/chat/completions`
	// would 404 those, silently triggering the LLM-error passthrough branch
	// in RunMultipassExtraction and breaking every test that needs a real
	// response. A bare handler matches any path.
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	store := &chatTestConfigStore{baseURL: srv.URL + "/v1/", model: "fake-model"}
	return ai.NewConfigResolver(store)
}

func mustWriteChatCompletion(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"content": content}},
		},
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func chunkWith(id, content string) vector.SearchChunk {
	return vector.SearchChunk{ID: id, FileID: "f-" + id, FileName: id + ".md", Content: content}
}

// All chunks return NONE — the result should be an empty slice and the
// downstream prompt assembly will have no CONTEXT block.
func TestRunMultipassExtraction_AllNoneDropsEverything(t *testing.T) {
	resolver := multipassTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		mustWriteChatCompletion(t, w, "NONE")
	})
	chunks := []vector.SearchChunk{
		chunkWith("a", "irrelevant prose"),
		chunkWith("b", "more irrelevant prose"),
	}
	out := RunMultipassExtraction(context.Background(), resolver, "kb", "what is X?", chunks, "")
	if len(out) != 0 {
		t.Fatalf("expected 0 chunks, got %d", len(out))
	}
}

// Full cancellation before any goroutine produces a result must NOT return
// an empty slice (which would leave the answer model with no context).
// Instead the original chunks pass through unextracted. A pre-cancelled
// context makes this deterministic: every goroutine hits the ctx.Done()
// branch of the semaphore acquire and returns without sending a result.
func TestRunMultipassExtraction_FullCancellationReturnsOriginal(t *testing.T) {
	resolver := multipassTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		mustWriteChatCompletion(t, w, "NONE")
	})
	chunks := []vector.SearchChunk{
		chunkWith("a", "first chunk"),
		chunkWith("b", "second chunk"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call so all goroutines bail at the semaphore

	out := RunMultipassExtraction(ctx, resolver, "kb", "what is X?", chunks, "")
	if len(out) != len(chunks) {
		t.Fatalf("expected original %d chunks to pass through, got %d", len(chunks), len(out))
	}
	for i := range chunks {
		if out[i].Content != chunks[i].Content {
			t.Errorf("chunk %d: content was modified to %q, want original %q", i, out[i].Content, chunks[i].Content)
		}
	}
}

// LLM returns extracted sentences verbatim — content is replaced and the
// chunk is kept. Metadata fields (FileID, FileName) are preserved so the
// citation system can still resolve [N] back to a source.
func TestRunMultipassExtraction_ReplacesContentAndKeepsMetadata(t *testing.T) {
	resolver := multipassTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		mustWriteChatCompletion(t, w, "The capital is Paris.\nIt has 2 million residents.")
	})
	chunks := []vector.SearchChunk{
		chunkWith("a", "France is a country in Europe. The capital is Paris. It has 2 million residents. Paris hosts a famous tower."),
	}
	out := RunMultipassExtraction(context.Background(), resolver, "kb", "what is the capital?", chunks, "")
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}
	if out[0].Content != "The capital is Paris.\nIt has 2 million residents." {
		t.Errorf("content not replaced: %q", out[0].Content)
	}
	if out[0].FileID != "f-a" || out[0].FileName != "a.md" || out[0].ID != "a" {
		t.Errorf("metadata not preserved: %+v", out[0])
	}
}

// Chunks should be processed in parallel and results compacted into the
// original order. Use an atomic counter to record arrival order and
// chunk-content discriminators in the LLM response so the test can match
// each response back to its input.
func TestRunMultipassExtraction_PreservesOrder(t *testing.T) {
	resolver := multipassTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Echo a marker that identifies which input chunk this call saw.
		var req ai.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		var userMsg string
		for _, m := range req.Messages {
			if m.Role == "user" {
				userMsg = m.Content
			}
		}
		switch {
		case strings.Contains(userMsg, "alpha"):
			mustWriteChatCompletion(t, w, "alpha-fact")
		case strings.Contains(userMsg, "bravo"):
			mustWriteChatCompletion(t, w, "NONE") // dropped
		case strings.Contains(userMsg, "charlie"):
			mustWriteChatCompletion(t, w, "charlie-fact")
		default:
			mustWriteChatCompletion(t, w, "default")
		}
	})
	chunks := []vector.SearchChunk{
		chunkWith("0", "alpha passage"),
		chunkWith("1", "bravo passage"),
		chunkWith("2", "charlie passage"),
	}
	out := RunMultipassExtraction(context.Background(), resolver, "kb", "q", chunks, "")
	if len(out) != 2 {
		t.Fatalf("want 2 kept (alpha+charlie), got %d", len(out))
	}
	if out[0].ID != "0" || out[0].Content != "alpha-fact" {
		t.Errorf("first kept chunk: id=%s content=%q", out[0].ID, out[0].Content)
	}
	if out[1].ID != "2" || out[1].Content != "charlie-fact" {
		t.Errorf("second kept chunk: id=%s content=%q", out[1].ID, out[1].Content)
	}
}

// On LLM error the original chunk content passes through unchanged (best-
// effort). Better to show the answer model a raw chunk than to drop it.
func TestRunMultipassExtraction_LLMErrorPassesThrough(t *testing.T) {
	resolver := multipassTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	chunks := []vector.SearchChunk{
		chunkWith("a", "the original passage text"),
	}
	out := RunMultipassExtraction(context.Background(), resolver, "kb", "q", chunks, "")
	if len(out) != 1 {
		t.Fatalf("expected passthrough kept, got %d chunks", len(out))
	}
	if out[0].Content != "the original passage text" {
		t.Errorf("passthrough content lost: %q", out[0].Content)
	}
}

// NONE sentinel variants the prompt may produce in practice — bolded,
// lower-cased, punctuated. All must register as "drop".
func TestRunMultipassExtraction_NoneSentinelVariants(t *testing.T) {
	cases := []string{"NONE", " none ", "None.", "**NONE**", "none"}
	for _, payload := range cases {
		t.Run(payload, func(t *testing.T) {
			resolver := multipassTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				mustWriteChatCompletion(t, w, payload)
			})
			chunks := []vector.SearchChunk{chunkWith("a", "noise")}
			out := RunMultipassExtraction(context.Background(), resolver, "kb", "q", chunks, "")
			if len(out) != 0 {
				t.Errorf("payload %q: expected drop, got %d chunks", payload, len(out))
			}
		})
	}
}

// Empty content extraction (LLM emits "") also counts as drop — saves the
// answer model from seeing an empty CONTEXT slot that would otherwise
// inflate the [N] gap count for nothing.
func TestRunMultipassExtraction_EmptyResponseDrops(t *testing.T) {
	resolver := multipassTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		mustWriteChatCompletion(t, w, "")
	})
	chunks := []vector.SearchChunk{chunkWith("a", "noise")}
	out := RunMultipassExtraction(context.Background(), resolver, "kb", "q", chunks, "")
	if len(out) != 0 {
		t.Fatalf("expected drop on empty response, got %d", len(out))
	}
}

// Empty input slice short-circuits before any LLM call. Verified by an
// atomic counter that asserts the handler was never hit.
func TestRunMultipassExtraction_EmptyInputShortCircuits(t *testing.T) {
	var hits atomic.Int32
	resolver := multipassTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		mustWriteChatCompletion(t, w, "should-not-fire")
	})
	out := RunMultipassExtraction(context.Background(), resolver, "kb", "q", nil, "")
	if len(out) != 0 {
		t.Errorf("len(out)=%d", len(out))
	}
	if hits.Load() != 0 {
		t.Errorf("LLM called on empty input: %d hits", hits.Load())
	}
}

// Empty query short-circuits — no LLM call, original chunks returned. This
// protects against accidental misuse from a caller that didn't validate
// params.SearchQuery upstream.
func TestRunMultipassExtraction_EmptyQueryShortCircuits(t *testing.T) {
	var hits atomic.Int32
	resolver := multipassTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		mustWriteChatCompletion(t, w, "should-not-fire")
	})
	chunks := []vector.SearchChunk{chunkWith("a", "x")}
	out := RunMultipassExtraction(context.Background(), resolver, "kb", "  ", chunks, "")
	if len(out) != 1 || out[0].ID != "a" {
		t.Errorf("expected passthrough, got %+v", out)
	}
	if hits.Load() != 0 {
		t.Errorf("LLM called on empty query: %d hits", hits.Load())
	}
}
