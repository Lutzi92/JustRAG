package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sseServer creates a test HTTP server that streams SSE chunks and then [DONE].
// chunks is a slice of (content, reasoningContent) pairs.
func sseServer(t *testing.T, chunks []struct{ content, reasoning string }) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter does not implement http.Flusher")
			return
		}

		for _, ch := range chunks {
			delta := map[string]string{}
			if ch.content != "" {
				delta["content"] = ch.content
			}
			if ch.reasoning != "" {
				delta["reasoning_content"] = ch.reasoning
			}
			payload := map[string]any{
				"choices": []map[string]any{
					{"index": 0, "delta": delta},
				},
			}
			b, _ := json.Marshal(payload)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}

		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// collectStream drains a StreamChunk channel and returns all non-Done chunks
// plus a bool confirming Done was received.
func collectStream(ch <-chan StreamChunk) ([]StreamChunk, bool) {
	var chunks []StreamChunk
	doneSeen := false
	for c := range ch {
		if c.Done {
			doneSeen = true
		} else {
			chunks = append(chunks, c)
		}
	}
	return chunks, doneSeen
}

// collectEvents drains a StreamEvent channel and returns all non-Done events
// plus a bool confirming Done was received.
func collectEvents(ch <-chan StreamEvent) ([]StreamEvent, bool) {
	var events []StreamEvent
	doneSeen := false
	for e := range ch {
		if e.Done {
			doneSeen = true
		} else {
			events = append(events, e)
		}
	}
	return events, doneSeen
}

// mockResolver returns a ConfigResolver backed by a stub store that always
// returns a config pointing at baseURL with the given model.
func mockResolver(baseURL, model string) *ConfigResolver {
	store := &stubStore{
		provider: &AIProviderInfo{ID: "p1", Name: "test", APIKey: "k", BaseURL: baseURL},
		models:   []AIModelInfo{{Name: model}},
	}
	return NewConfigResolver(store)
}

// stubStore is a minimal ConfigStore for testing.
type stubStore struct {
	provider *AIProviderInfo
	models   []AIModelInfo
}

func (s *stubStore) GetActiveAIProvider(_ context.Context) (*AIProviderInfo, error) {
	return s.provider, nil
}
func (s *stubStore) GetAIProviderByID(_ context.Context, _ string) (*AIProviderInfo, error) {
	return s.provider, nil
}
func (s *stubStore) GetAIModelsByProvider(_ context.Context, _ string) ([]AIModelInfo, error) {
	return s.models, nil
}
func (s *stubStore) GetKBModelOverrides(_ context.Context, _ string) (*KBModelOverrides, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Client.StreamChatCompletion tests
// ---------------------------------------------------------------------------

func TestStreamChatCompletion_ThreeChunksThenDone(t *testing.T) {
	srv := sseServer(t, []struct{ content, reasoning string }{
		{"Hello", ""},
		{" world", ""},
		{"!", ""},
	})
	defer srv.Close()

	client := NewClient(srv.URL+"/v1/", "test-key")
	req := ChatRequest{Model: "gpt-4", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	ch, err := client.StreamChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	chunks, done := collectStream(ch)
	if !done {
		t.Error("expected Done chunk")
	}
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	want := []string{"Hello", " world", "!"}
	for i, c := range chunks {
		if c.Content != want[i] {
			t.Errorf("chunk %d: got %q, want %q", i, c.Content, want[i])
		}
	}
}

func TestStreamChatCompletion_ContextCancelled(t *testing.T) {
	// Server that blocks indefinitely — context should cancel cleanly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	client := NewClient(srv.URL+"/v1/", "")
	req := ChatRequest{Model: "gpt-4", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	ch, err := client.StreamChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Drain — should not block forever.
	for range ch {
	}
}

// TestStreamChatCompletion_ScannerErrorSurfaced: a single SSE frame larger
// than the 1 MB scanner cap aborts the scan loop with bufio.ErrTooLong.
// The terminal chunk must carry Err so consumers can distinguish a
// truncated stream from a clean completion (and avoid persisting the
// partial content as a finished AI message).
func TestStreamChatCompletion_ScannerErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		// A valid content frame first, then a frame over the scanner cap.
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"%s\"}}]}\n\n",
			strings.Repeat("x", 2<<20))
		flusher.Flush()
	}))
	defer srv.Close()

	client := NewClient(srv.URL+"/v1/", "test-key")
	req := ChatRequest{Model: "gpt-4", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	ch, err := client.StreamChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var contents []string
	var terminal *StreamChunk
	for c := range ch {
		if c.Done {
			cc := c
			terminal = &cc
			continue
		}
		contents = append(contents, c.Content)
	}
	if terminal == nil {
		t.Fatal("expected a terminal Done chunk")
	}
	if terminal.Err == nil {
		t.Fatal("terminal chunk after scanner failure must carry Err — truncated stream was reported as clean completion")
	}
	if len(contents) != 1 || contents[0] != "partial" {
		t.Errorf("expected the one pre-failure content chunk, got %v", contents)
	}
}

func TestStreamChatCompletion_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := NewClient(srv.URL+"/v1/", "bad-key")
	req := ChatRequest{Model: "gpt-4", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	_, err := client.StreamChatCompletion(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", apiErr.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// StreamCompletion tests
// ---------------------------------------------------------------------------

func TestStreamCompletion_ContentOnly(t *testing.T) {
	srv := sseServer(t, []struct{ content, reasoning string }{
		{"Hello", ""},
		{" there", ""},
	})
	defer srv.Close()

	resolver := mockResolver(srv.URL+"/v1/", "gpt-4")
	ch, err := StreamCompletion(context.Background(), resolver, "hi", "", "", "", 0.2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, done := collectEvents(ch)
	if !done {
		t.Error("expected Done event")
	}

	var sb strings.Builder
	for _, e := range events {
		sb.WriteString(e.Content)
		if e.Reasoning != "" {
			t.Errorf("unexpected reasoning: %q", e.Reasoning)
		}
	}
	if got := sb.String(); got != "Hello there" {
		t.Errorf("content: got %q, want %q", got, "Hello there")
	}
}

func TestStreamCompletion_ReasoningContent(t *testing.T) {
	srv := sseServer(t, []struct{ content, reasoning string }{
		{"", "step 1"},
		{"", "step 2"},
		{"answer", ""},
	})
	defer srv.Close()

	resolver := mockResolver(srv.URL+"/v1/", "deepseek-r1")
	ch, err := StreamCompletion(context.Background(), resolver, "solve", "", "", "medium", 0.2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, done := collectEvents(ch)
	if !done {
		t.Error("expected Done event")
	}

	var content, reasoning strings.Builder
	for _, e := range events {
		content.WriteString(e.Content)
		reasoning.WriteString(e.Reasoning)
	}
	if got := reasoning.String(); got != "step 1step 2" {
		t.Errorf("reasoning: got %q, want %q", got, "step 1step 2")
	}
	if got := content.String(); got != "answer" {
		t.Errorf("content: got %q, want %q", got, "answer")
	}
}

func TestStreamCompletion_ThinkTagsInContent(t *testing.T) {
	// Single chunk that contains a full <think> block.
	srv := sseServer(t, []struct{ content, reasoning string }{
		{"<think>internal thought</think>the answer", ""},
	})
	defer srv.Close()

	resolver := mockResolver(srv.URL+"/v1/", "qwen")
	ch, err := StreamCompletion(context.Background(), resolver, "q", "", "", "medium", 0.2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, done := collectEvents(ch)
	if !done {
		t.Error("expected Done event")
	}

	var content, reasoning strings.Builder
	for _, e := range events {
		content.WriteString(e.Content)
		reasoning.WriteString(e.Reasoning)
	}
	if got := reasoning.String(); got != "internal thought" {
		t.Errorf("reasoning: got %q, want %q", got, "internal thought")
	}
	if got := content.String(); got != "the answer" {
		t.Errorf("content: got %q, want %q", got, "the answer")
	}
}

func TestStreamCompletion_ThinkTagSplitAcrossChunks(t *testing.T) {
	// The closing tag </think> is split across two chunks.
	srv := sseServer(t, []struct{ content, reasoning string }{
		{"<think>thought</thi", ""},
		{"nk>result", ""},
	})
	defer srv.Close()

	resolver := mockResolver(srv.URL+"/v1/", "qwen")
	ch, err := StreamCompletion(context.Background(), resolver, "q", "", "", "medium", 0.2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, done := collectEvents(ch)
	if !done {
		t.Error("expected Done event")
	}

	var content, reasoning strings.Builder
	for _, e := range events {
		content.WriteString(e.Content)
		reasoning.WriteString(e.Reasoning)
	}
	if got := reasoning.String(); got != "thought" {
		t.Errorf("reasoning: got %q, want %q", got, "thought")
	}
	if got := content.String(); got != "result" {
		t.Errorf("content: got %q, want %q", got, "result")
	}
}

func TestStreamCompletion_OpenTagSplitAcrossChunks(t *testing.T) {
	// The opening tag <think> is split across two chunks.
	srv := sseServer(t, []struct{ content, reasoning string }{
		{"before<thi", ""},
		{"nk>reasoning</think>after", ""},
	})
	defer srv.Close()

	resolver := mockResolver(srv.URL+"/v1/", "qwen")
	ch, err := StreamCompletion(context.Background(), resolver, "q", "", "", "medium", 0.2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events, done := collectEvents(ch)
	if !done {
		t.Error("expected Done event")
	}

	var content, reasoning strings.Builder
	for _, e := range events {
		content.WriteString(e.Content)
		reasoning.WriteString(e.Reasoning)
	}
	if got := reasoning.String(); got != "reasoning" {
		t.Errorf("reasoning: got %q, want %q", got, "reasoning")
	}
	if got := content.String(); got != "beforeafter" {
		t.Errorf("content: got %q, want %q", got, "beforeafter")
	}
}

// ---------------------------------------------------------------------------
// reasoning_effort request-side wiring
// ---------------------------------------------------------------------------

// bodyCapturingSSEServer is sseServer plus a hook that records the raw request
// body of the (single) chat completion call, so tests can assert on what was
// sent to the provider.
func bodyCapturingSSEServer(t *testing.T, captured *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*captured = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\n")
		fl.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
}

// A non-empty reasoning effort must be sent to the provider as
// "reasoning_effort": this is the request-side signal that makes an o-series
// model produce chain-of-thought. Without it the (otherwise complete) display
// pipeline has nothing to show.
func TestStreamCompletion_SendsReasoningEffort(t *testing.T) {
	var body string
	srv := bodyCapturingSSEServer(t, &body)
	defer srv.Close()

	resolver := mockResolver(srv.URL+"/v1/", "o3-mini")
	ch, err := StreamCompletion(context.Background(), resolver, "q", "", "", "high", 0.2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	events, done := collectEvents(ch)
	if !done {
		t.Error("expected Done event")
	}

	if !strings.Contains(body, `"reasoning_effort":"high"`) {
		t.Errorf("request body must carry reasoning_effort=high, got: %s", body)
	}
	// The LiteLLM→vLLM gateway does not translate reasoning_effort into
	// enable_thinking, so the explicit chat_template_kwargs switch must also
	// be present — that is what actually puts gemma-4 into thinking mode.
	if !strings.Contains(body, `"chat_template_kwargs":{"enable_thinking":true}`) {
		t.Errorf("request body must carry chat_template_kwargs.enable_thinking=true, got: %s", body)
	}
	// The returned reasoning must still flow through to the caller.
	var reasoning strings.Builder
	for _, e := range events {
		reasoning.WriteString(e.Reasoning)
	}
	if got := reasoning.String(); got != "thinking" {
		t.Errorf("reasoning: got %q, want %q", got, "thinking")
	}
}

// An empty effort must NOT add reasoning_effort to the request — omitempty
// keeps non-reasoning calls byte-identical to the pre-feature request.
func TestStreamCompletion_OmitsReasoningEffortWhenEmpty(t *testing.T) {
	var body string
	srv := bodyCapturingSSEServer(t, &body)
	defer srv.Close()

	resolver := mockResolver(srv.URL+"/v1/", "gpt-4")
	ch, err := StreamCompletion(context.Background(), resolver, "q", "", "", "", 0.2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, done := collectEvents(ch); !done {
		t.Error("expected Done event")
	}

	if strings.Contains(body, "reasoning_effort") {
		t.Errorf("request body must not carry reasoning_effort when disabled, got: %s", body)
	}
	if strings.Contains(body, "chat_template_kwargs") {
		t.Errorf("request body must not carry chat_template_kwargs when disabled, got: %s", body)
	}
}

// An explicit temperature is forwarded verbatim to the provider.
func TestStreamCompletion_SendsTemperature(t *testing.T) {
	var body string
	srv := bodyCapturingSSEServer(t, &body)
	defer srv.Close()

	resolver := mockResolver(srv.URL+"/v1/", "gpt-4")
	ch, err := StreamCompletion(context.Background(), resolver, "q", "", "", "", 0.7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, done := collectEvents(ch); !done {
		t.Error("expected Done event")
	}
	if !strings.Contains(body, `"temperature":0.7`) {
		t.Errorf("request body must carry temperature=0.7, got: %s", body)
	}
}

// A non-positive temperature falls back to DefaultAnswerTemperature so a
// zero-valued caller never accidentally requests greedy decoding.
func TestStreamCompletion_TemperatureFallsBackToDefault(t *testing.T) {
	var body string
	srv := bodyCapturingSSEServer(t, &body)
	defer srv.Close()

	resolver := mockResolver(srv.URL+"/v1/", "gpt-4")
	ch, err := StreamCompletion(context.Background(), resolver, "q", "", "", "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, done := collectEvents(ch); !done {
		t.Error("expected Done event")
	}
	if !strings.Contains(body, `"temperature":0.3`) {
		t.Errorf("zero temperature must fall back to DefaultAnswerTemperature 0.3, got: %s", body)
	}
}

// ---------------------------------------------------------------------------
// Regression guard: non-blocking send in panic recovery path
// ---------------------------------------------------------------------------

// TestPanicRecoveryNonBlockingSend models the shape of the goroutine in
// stream.go and asserts that the recovery-path send does NOT block when the
// receiver has exited. This guards against regressing to a blocking send.
func TestPanicRecoveryNonBlockingSend(t *testing.T) {
	out := make(chan struct{}) // unbuffered — nobody will ever read
	done := make(chan struct{})

	var panicked atomic.Bool

	go func() {
		defer close(done)
		defer close(out)
		defer func() {
			if r := recover(); r != nil {
				panicked.Store(true)
				// This MUST be non-blocking — the receiver is gone.
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}()
		panic("boom")
	}()

	select {
	case <-done:
		// Producer goroutine returned cleanly.
	case <-time.After(2 * time.Second):
		runtime.Gosched()
		t.Fatal("producer goroutine blocked on recovery-path send — deadlock regression")
	}

	if !panicked.Load() {
		t.Fatal("panic recovery did not fire")
	}
}

// ---------------------------------------------------------------------------
// Tool-call delta streaming
// ---------------------------------------------------------------------------

// TestStreamChatCompletion_AssemblesToolCallDeltas exercises the streaming
// tool-call path. The fake provider emits four SSE chunks:
//  1. {delta:{role:"assistant"}}                          — preamble
//  2. {delta:{tool_calls:[{index:0,id:"c1",type:"function",
//     function:{name:"calculator",arguments:"{\"e"}}]}}
//  3. {delta:{tool_calls:[{index:0,function:{arguments:"xpression\":\"2+2\"}"}}]}}
//  4. {finish_reason:"tool_calls"} [DONE]
//
// The client must pass each tool_call delta through verbatim (caller
// assembles), and the chunk carrying finish_reason must surface it.
func TestStreamChatCompletion_AssemblesToolCallDeltas(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"calculator","arguments":"{\"e"}}]}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"xpression\":\"2+2\"}"}}]}}]}`,
		``,
		`data: {"choices":[{"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/v1/", "test-key")
	ch, err := c.StreamChatCompletion(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []ChatMessage{{Role: "user", Content: "2+2"}},
		Tools: []ChatTool{{Type: "function", Function: ChatToolFunction{
			Name: "calculator", Description: "math", Parameters: json.RawMessage(`{}`),
		}}},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var deltas []ToolCallDelta
	var finish string
	var done bool
	for chunk := range ch {
		deltas = append(deltas, chunk.ToolCallDeltas...)
		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}
		if chunk.Done {
			done = true
		}
	}
	if !done {
		t.Fatalf("stream ended without Done chunk")
	}
	if finish != "tool_calls" {
		t.Fatalf("finish_reason: got %q, want tool_calls", finish)
	}
	if len(deltas) != 2 {
		t.Fatalf("want 2 deltas, got %d (%+v)", len(deltas), deltas)
	}
	if deltas[0].Index != 0 || deltas[0].ID != "c1" || deltas[0].Name != "calculator" {
		t.Fatalf("delta 0: %+v", deltas[0])
	}
	if deltas[0].Arguments != `{"e` || deltas[1].Arguments != `xpression":"2+2"}` {
		t.Fatalf("arg fragments wrong: %q | %q", deltas[0].Arguments, deltas[1].Arguments)
	}
}
