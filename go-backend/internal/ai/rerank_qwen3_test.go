package ai

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// BuildQwen3RerankMessages
// ---------------------------------------------------------------------------

func TestBuildQwen3RerankMessages_EmptyInstructionUsesDefault(t *testing.T) {
	msgs := BuildQwen3RerankMessages("", "wer ist projektleiter?", "Hans Müller leitet …")
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, `"yes" or "no"`) {
		t.Errorf("unexpected system message: %+v", msgs[0])
	}
	if msgs[1].Role != "user" {
		t.Errorf("expected user message, got role %q", msgs[1].Role)
	}
	if !strings.Contains(msgs[1].Content, defaultQwen3RerankInstruction) {
		t.Errorf("user content missing default instruction: %q", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, "<Query>: wer ist projektleiter?") {
		t.Errorf("user content missing query tag: %q", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, "<Document>: Hans Müller leitet …") {
		t.Errorf("user content missing document tag: %q", msgs[1].Content)
	}
}

func TestBuildQwen3RerankMessages_CustomInstruction(t *testing.T) {
	msgs := BuildQwen3RerankMessages("Retrieve all passages about X", "q", "d")
	if !strings.Contains(msgs[1].Content, "<Instruct>: Retrieve all passages about X") {
		t.Errorf("user content missing custom instruction: %q", msgs[1].Content)
	}
}

// ---------------------------------------------------------------------------
// IsQwen3RerankerModel
// ---------------------------------------------------------------------------

func TestIsQwen3RerankerModel(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"jlu/Qwen3-Reranker-4B", true},
		{"qwen3-reranker-0.6b", true},
		{"Qwen/Qwen3-Reranker-8B", true},
		{"cohere-rerank-v3", false},
		{"voyage-rerank-1", false},
		{"bge-reranker-v2", false},
		{"", false},
		{"qwen3-embedding-4b", false}, // embedder, not reranker
	}
	for _, c := range cases {
		got := IsQwen3RerankerModel(c.name)
		if got != c.want {
			t.Errorf("IsQwen3RerankerModel(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// scoreYesNoFromLogprobs
// ---------------------------------------------------------------------------

func TestScoreYesNoFromLogprobs_BothPresent(t *testing.T) {
	// logprob(yes) = -0.1 (≈ 0.905 prob), logprob(no) = -2.3 (≈ 0.100 prob).
	// softmax should give ~0.900 for yes.
	top := []ChatLogprobEntry{
		{Token: "yes", Logprob: -0.1},
		{Token: "no", Logprob: -2.3},
		{Token: "maybe", Logprob: -10.0},
	}
	score := scoreYesNoFromLogprobs(top)
	if math.Abs(score-0.900) > 0.01 {
		t.Errorf("expected ~0.900, got %f", score)
	}
}

func TestScoreYesNoFromLogprobs_OnlyYesPresent(t *testing.T) {
	// yes only → with missing_floor=-20 for no, softmax should be very close to 1.
	top := []ChatLogprobEntry{
		{Token: "yes", Logprob: -0.01},
		{Token: "maybe", Logprob: -2.0},
	}
	score := scoreYesNoFromLogprobs(top)
	if score < 0.99 {
		t.Errorf("expected score ≥ 0.99 when only yes is present, got %f", score)
	}
}

func TestScoreYesNoFromLogprobs_OnlyNoPresent(t *testing.T) {
	top := []ChatLogprobEntry{
		{Token: "no", Logprob: -0.01},
		{Token: "never", Logprob: -5.0},
	}
	score := scoreYesNoFromLogprobs(top)
	if score > 0.01 {
		t.Errorf("expected score ≤ 0.01 when only no is present, got %f", score)
	}
}

func TestScoreYesNoFromLogprobs_Neither(t *testing.T) {
	top := []ChatLogprobEntry{
		{Token: "maybe", Logprob: -0.1},
		{Token: "perhaps", Logprob: -2.0},
	}
	score := scoreYesNoFromLogprobs(top)
	if score != 0.5 {
		t.Errorf("expected neutral 0.5 when neither token present, got %f", score)
	}
}

func TestScoreYesNoFromLogprobs_CaseInsensitiveAndTrimmed(t *testing.T) {
	// "Yes" with leading space + uppercase still matches; " No" likewise.
	top := []ChatLogprobEntry{
		{Token: " Yes", Logprob: -0.5},
		{Token: "NO", Logprob: -1.0},
	}
	score := scoreYesNoFromLogprobs(top)
	// -0.5 vs -1.0 softmax → eYes/(eYes+eNo) where difference is 0.5 → ~0.6225.
	if math.Abs(score-0.6225) > 0.01 {
		t.Errorf("expected ~0.6225, got %f", score)
	}
}

// ---------------------------------------------------------------------------
// End-to-end: chat template path hits /chat/completions and uses logprobs
// ---------------------------------------------------------------------------

// qwen3ChatHandler simulates a Qwen3-Reranker backend. Each call returns a
// slightly different yes/no distribution so downstream validation sees
// varied scores (the real reranker wouldn't emit identical probabilities
// for distinct documents). Bodies and call count are captured for
// assertions.
func qwen3ChatHandler(calls *atomic.Int32, gotBodies *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gotBodies != nil {
			*gotBodies = append(*gotBodies, string(body))
		}
		n := calls.Add(1)
		// Vary yesLogp across calls by a full nat so the downstream range-
		// based "poor discrimination" guard (max-min ≥ 0.1) sees enough
		// spread on a small batch of mocked responses.
		yesLogp := -0.1 - 1.0*float64(n-1) // -0.1, -1.1, -2.1, ...
		noLogp := -2.3
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{"content": "yes"},
					"logprobs": map[string]any{
						"content": []map[string]any{
							{
								"token":   "yes",
								"logprob": yesLogp,
								"top_logprobs": []map[string]any{
									{"token": "yes", "logprob": yesLogp},
									{"token": "no", "logprob": noLogp},
								},
							},
						},
					},
				},
			},
		})
	}
}

func TestRerank_Qwen3ChatTemplate_FansOutAndReturnsSoftmax(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(qwen3ChatHandler(&calls, nil))
	defer srv.Close()

	resolver := newRerankResolver(srv.URL, "qwen3-reranker-4b")
	docs := []string{"doc-a", "doc-b", "doc-c"}

	scores, err := Rerank(context.Background(), resolver, "query?", docs, "", 0, RerankOptions{
		UseChatTemplate: true,
		Instruction:     "Retrieve all relevant passages",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if int(calls.Load()) != len(docs) {
		t.Errorf("expected %d chat calls, got %d", len(docs), calls.Load())
	}
	if len(scores) != len(docs) {
		t.Fatalf("expected %d scores, got %d", len(docs), len(scores))
	}
	for i, s := range scores {
		if s.Index != i {
			t.Errorf("scores[%d].Index = %d, want %d", i, s.Index, i)
		}
		// Handler varies yesLogp ∈ {-0.1, -1.1, -2.1} against noLogp=-2.3,
		// so softmax(yes, no) spans roughly 0.55..0.91. Goroutine arrival
		// order isn't deterministic so we assert the band rather than a
		// specific index-to-value mapping.
		if s.Score < 0.5 || s.Score > 0.95 {
			t.Errorf("scores[%d].Score = %f, want in [0.5, 0.95]", i, s.Score)
		}
	}
}

// TestRerank_Qwen3ChatTemplate_SkippedWhenModelFamilyMismatch verifies that
// flipping the chat-template flag on a non-Qwen3 model silently takes the
// legacy /rerank path — operators shouldn't accidentally break Cohere/Voyage
// by toggling a global setting.
func TestRerank_Qwen3ChatTemplate_SkippedWhenModelFamilyMismatch(t *testing.T) {
	legacyResults := []map[string]any{
		{"index": 0, "relevance_score": 0.8},
		{"index": 1, "relevance_score": 0.4},
	}
	srv := httptest.NewServer(rerankHandler(legacyResults))
	defer srv.Close()

	resolver := newRerankResolver(srv.URL, "cohere-rerank-v3")
	docs := []string{"doc-a", "doc-b"}

	scores, err := Rerank(context.Background(), resolver, "q", docs, "", 0, RerankOptions{
		UseChatTemplate: true, // irrelevant: model is not Qwen3
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Scores must match the /rerank shape (0.8, 0.4), not a softmax result.
	if scores[0].Score != 0.8 {
		t.Errorf("expected legacy score 0.8 for doc 0, got %f", scores[0].Score)
	}
}

// TestRerank_Qwen3ChatTemplate_FallsBackOnMissingLogprobs verifies that a
// backend which doesn't return top_logprobs (older vLLM, or a reranker-only
// deployment) fails the chat path and falls back to /rerank.
func TestRerank_Qwen3ChatTemplate_FallsBackOnMissingLogprobs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Missing logprobs entirely — the chat path should detect and fall back.
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "yes"}},
			},
		})
	})
	mux.HandleFunc("/rerank", rerankHandler([]map[string]any{
		{"index": 0, "relevance_score": 0.77},
		{"index": 1, "relevance_score": 0.42},
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resolver := newRerankResolver(srv.URL, "qwen3-reranker-4b")
	docs := []string{"doc-a", "doc-b"}

	scores, err := Rerank(context.Background(), resolver, "q", docs, "", 0, RerankOptions{
		UseChatTemplate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Fallback engaged → legacy /rerank scores.
	if scores[0].Score != 0.77 || scores[1].Score != 0.42 {
		t.Errorf("expected legacy /rerank scores [0.77, 0.42], got [%f, %f]", scores[0].Score, scores[1].Score)
	}
}

// TestRerank_Qwen3ChatTemplate_RequestShape verifies that the chat request
// body carries the knobs that make the softmax path meaningful: logprobs,
// top_logprobs>=20, max_tokens=1, and temperature=0. If any of these regress
// the scorer silently degrades (e.g. without logprobs, scores collapse to
// neutral 0.5).
func TestRerank_Qwen3ChatTemplate_RequestShape(t *testing.T) {
	var calls atomic.Int32
	var bodies []string
	srv := httptest.NewServer(qwen3ChatHandler(&calls, &bodies))
	defer srv.Close()

	resolver := newRerankResolver(srv.URL, "qwen3-reranker-4b")
	_, err := Rerank(context.Background(), resolver, "q", []string{"d"}, "", 0, RerankOptions{
		UseChatTemplate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("expected 1 request body, got %d", len(bodies))
	}

	var req map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if req["logprobs"] != true {
		t.Errorf("expected logprobs=true, got %v", req["logprobs"])
	}
	if top, _ := req["top_logprobs"].(float64); top < 20 {
		t.Errorf("expected top_logprobs ≥ 20, got %v", req["top_logprobs"])
	}
	if mt, _ := req["max_tokens"].(float64); mt != 1 {
		t.Errorf("expected max_tokens=1, got %v", req["max_tokens"])
	}
	if tmp, _ := req["temperature"].(float64); tmp != 0 {
		t.Errorf("expected temperature=0, got %v", req["temperature"])
	}
}
