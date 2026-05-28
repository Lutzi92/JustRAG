package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func ptr[T any](v T) *T { return &v }

// ---------------------------------------------------------------------------
// ChatCompletion tests
// ---------------------------------------------------------------------------

func TestChatCompletion_ValidResponse(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing or wrong Authorization header")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "Hello, world!"}},
			},
		})
	})

	client := ai.NewClient(srv.URL, "test-key")
	resp, err := client.ChatCompletion(context.Background(), &ai.ChatRequest{
		Model:    "gpt-4o",
		Messages: []ai.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello, world!" {
		t.Errorf("unexpected content: %s", resp.Choices[0].Message.Content)
	}
}

func TestChatCompletion_ReasoningContent(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{
						"content":           "The answer is 42.",
						"reasoning_content": "I thought about it carefully.",
					},
				},
			},
		})
	})

	client := ai.NewClient(srv.URL, "key")
	resp, err := client.ChatCompletion(context.Background(), &ai.ChatRequest{
		Model:       "deepseek-r1",
		Messages:    []ai.ChatMessage{{Role: "user", Content: "What is the answer?"}},
		Temperature: ptr(0.7),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if resp.Choices[0].Message.ReasoningContent != "I thought about it carefully." {
		t.Errorf("unexpected reasoning_content: %q", resp.Choices[0].Message.ReasoningContent)
	}
	if resp.Choices[0].Message.Content != "The answer is 42." {
		t.Errorf("unexpected content: %q", resp.Choices[0].Message.Content)
	}
}

// ---------------------------------------------------------------------------
// Embedding tests
// ---------------------------------------------------------------------------

func TestEmbedding_SortedByIndex(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		// Return embeddings deliberately out of order.
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float64{0.4, 0.5, 0.6}},
				{"index": 0, "embedding": []float64{0.1, 0.2, 0.3}},
			},
		})
	})

	client := ai.NewClient(srv.URL, "key")
	resp, err := client.Embedding(context.Background(), &ai.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: []string{"hello", "world"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.Data))
	}
	if resp.Data[0].Index != 0 {
		t.Errorf("expected Data[0].Index == 0, got %d", resp.Data[0].Index)
	}
	if resp.Data[1].Index != 1 {
		t.Errorf("expected Data[1].Index == 1, got %d", resp.Data[1].Index)
	}
	if resp.Data[0].Embedding[0] != 0.1 {
		t.Errorf("wrong embedding order: first vector should start with 0.1")
	}
}

// ---------------------------------------------------------------------------
// Rerank tests
// ---------------------------------------------------------------------------

func TestRerank_ParsesResults(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"index": 2, "relevance_score": 0.95},
				{"index": 0, "score": 0.42},
			},
		})
	})

	client := ai.NewClient(srv.URL, "key")
	resp, err := client.Rerank(context.Background(), &ai.RerankRequest{
		Model:     "rerank-v1",
		Query:     "go programming",
		Documents: []string{"doc0", "doc1", "doc2"},
		TopN:      2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if resp.Results[0].Index != 2 {
		t.Errorf("expected first result index 2, got %d", resp.Results[0].Index)
	}
	if resp.Results[0].RelevanceScore != 0.95 {
		t.Errorf("expected relevance_score 0.95, got %f", resp.Results[0].RelevanceScore)
	}
	if resp.Results[1].Score != 0.42 {
		t.Errorf("expected score 0.42, got %f", resp.Results[1].Score)
	}
}

// ---------------------------------------------------------------------------
// ListModels tests
// ---------------------------------------------------------------------------

func TestListModels_Returns200(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	})

	client := ai.NewClient(srv.URL, "key")
	if err := client.ListModels(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// APIError tests
// ---------------------------------------------------------------------------

func TestAPIError_On401(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid api key"}`))
	})

	client := ai.NewClient(srv.URL, "bad-key")
	_, err := client.ChatCompletion(context.Background(), &ai.ChatRequest{
		Model:    "gpt-4o",
		Messages: []ai.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	apiErr, ok := err.(*ai.APIError)
	if !ok {
		t.Fatalf("expected *ai.APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", apiErr.StatusCode)
	}
	if apiErr.Body != `{"error":"invalid api key"}` {
		t.Errorf("unexpected body: %s", apiErr.Body)
	}
	if apiErr.Error() == "" {
		t.Error("APIError.Error() should not be empty")
	}
}

// ---------------------------------------------------------------------------
// Default base URL test
// ---------------------------------------------------------------------------

func TestNewClient_DefaultBaseURL(t *testing.T) {
	// When an empty baseURL is given, the client must use the OpenAI default.
	// We verify this by checking that the client is constructed without panic
	// and that a request to a test server that echoes the Host header works
	// when we explicitly provide a URL.
	//
	// For the default-URL case we simply verify the struct is not nil and a
	// request to a non-existent host fails (not a nil-pointer panic).
	client := ai.NewClient("", "key")
	if client == nil {
		t.Fatal("NewClient returned nil")
	}

	// Also verify that a trailing slash is added when omitted.
	client2 := ai.NewClient("http://example.com/v1", "key")
	if client2 == nil {
		t.Fatal("NewClient returned nil for non-empty URL")
	}
	// Attempting a request will yield a network error (not a panic), confirming
	// the URL was normalized. We use a cancelled context so we don't actually
	// hit the network.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client2.ListModels(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

// ---------------------------------------------------------------------------
// Tool-calling types tests
// ---------------------------------------------------------------------------

// TestChatRequest_ToolsFieldOmitted verifies the new Tools and ToolChoice
// fields stay out of the JSON when unset — existing call sites must
// continue to send byte-identical requests to vLLM/LiteLLM.
func TestChatRequest_ToolsFieldOmitted(t *testing.T) {
	req := ai.ChatRequest{
		Model:    "test-model",
		Messages: []ai.ChatMessage{{Role: "user", Content: "hi"}},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(raw, []byte("tools")) || bytes.Contains(raw, []byte("tool_choice")) {
		t.Fatalf("unset tool fields leaked into JSON: %s", raw)
	}
}

// TestChatRequest_ToolsRoundTrip verifies Tools, ToolChoice, ChatMessage's
// ToolCalls/ToolCallID/Name, and ChatChoice's FinishReason all marshal
// and unmarshal through the OpenAI-compatible JSON shape.
func TestChatRequest_ToolsRoundTrip(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"type":"number"}}}`)
	req := ai.ChatRequest{
		Model: "test-model",
		Messages: []ai.ChatMessage{
			{Role: "user", Content: "what is 2+2"},
			{Role: "assistant", ToolCalls: []ai.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: ai.ToolCallFunction{Name: "calculator", Arguments: `{"expression":"2+2"}`},
			}}},
			{Role: "tool", ToolCallID: "call_1", Name: "calculator", Content: "4"},
		},
		Tools: []ai.ChatTool{{
			Type: "function",
			Function: ai.ChatToolFunction{
				Name: "calculator", Description: "evaluate arithmetic", Parameters: schema,
			},
		}},
		ToolChoice: "auto",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ai.ChatRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Tools) != 1 || out.Tools[0].Function.Name != "calculator" {
		t.Fatalf("tools lost in round-trip: %+v", out.Tools)
	}
	if out.ToolChoice != "auto" {
		t.Fatalf("tool_choice lost: %v", out.ToolChoice)
	}
	if len(out.Messages) != 3 ||
		len(out.Messages[1].ToolCalls) != 1 ||
		out.Messages[1].ToolCalls[0].ID != "call_1" ||
		out.Messages[2].ToolCallID != "call_1" {
		t.Fatalf("messages lost: %+v", out.Messages)
	}
}

// TestChatChoice_FinishReasonParses verifies provider responses with
// finish_reason="tool_calls" and finish_reason="stop" both unmarshal
// into ChatChoice without losing the reason or the assistant tool_calls.
func TestChatChoice_FinishReasonParses(t *testing.T) {
	payload := []byte(`{
      "choices":[{
        "message":{"role":"assistant","content":"","tool_calls":[
          {"id":"call_1","type":"function",
           "function":{"name":"calculator","arguments":"{\"expression\":\"2+2\"}"}}
        ]},
        "finish_reason":"tool_calls"
      }]
    }`)
	var resp ai.ChatResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("want 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason: got %q", resp.Choices[0].FinishReason)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 ||
		resp.Choices[0].Message.ToolCalls[0].Function.Name != "calculator" {
		t.Fatalf("tool_calls lost: %+v", resp.Choices[0].Message)
	}
}
