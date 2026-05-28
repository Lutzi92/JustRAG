package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// buildChatServer starts an httptest server that serves /chat/completions.
// The handler function receives the request and must write the response.
func buildChatServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// chatResponse builds a JSON chat completion response.
func chatResponse(content, reasoningContent string) map[string]any {
	msg := map[string]string{"content": content}
	if reasoningContent != "" {
		msg["reasoning_content"] = reasoningContent
	}
	return map[string]any{
		"choices": []map[string]any{
			{"message": msg},
		},
	}
}

// resolverForServer creates a ConfigResolver whose config points at srv.URL.
// The first model is the default; any extraModels are appended to the provider's
// model list so they appear in ChatModels and are accepted as valid overrides.
func resolverForServer(srv *httptest.Server, model string, extraModels ...string) *ConfigResolver {
	models := []AIModelInfo{{Name: model}}
	for _, em := range extraModels {
		models = append(models, AIModelInfo{Name: em})
	}
	store := &mockStore{
		activeProvider: &AIProviderInfo{
			ID:      "prov-test",
			Name:    "Test",
			APIKey:  "test-key",
			BaseURL: srv.URL,
		},
		modelsByProv: map[string][]AIModelInfo{
			"prov-test": models,
		},
		kbOverrides: map[string]*KBModelOverrides{},
	}
	return NewConfigResolver(store)
}

// writeChatJSON encodes v as JSON to w with content-type set.
func writeChatJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// Test 1: Basic completion — returns content with no reasoning.
func TestGenerateCompletion_BasicContent(t *testing.T) {
	t.Parallel()
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse("Hello, world!", ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	result, err := GenerateCompletion(context.Background(), resolver, "Hi", "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Hello, world!" {
		t.Errorf("unexpected content: %q", result.Content)
	}
	if result.Reasoning != "" {
		t.Errorf("expected empty reasoning, got %q", result.Reasoning)
	}
}

// Test 2: Reasoning extracted from reasoning_content field.
func TestGenerateCompletion_ReasoningFromReasoningContent(t *testing.T) {
	t.Parallel()
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse("The answer is 42.", "I thought carefully."))
	})

	resolver := resolverForServer(srv, "deepseek-r1")
	result, err := GenerateCompletion(context.Background(), resolver, "What is the answer?", "", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "The answer is 42." {
		t.Errorf("unexpected content: %q", result.Content)
	}
	if result.Reasoning != "I thought carefully." {
		t.Errorf("unexpected reasoning: %q", result.Reasoning)
	}
}

// Test 3: Reasoning extracted from <think> tags when reasoning_content is absent.
func TestGenerateCompletion_ReasoningFromThinkTags(t *testing.T) {
	t.Parallel()
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse("<think>I pondered this.</think>The final answer.", ""))
	})

	resolver := resolverForServer(srv, "qwen3")
	result, err := GenerateCompletion(context.Background(), resolver, "Explain?", "You are helpful.", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "The final answer." {
		t.Errorf("unexpected content: %q", result.Content)
	}
	if result.Reasoning != "I pondered this." {
		t.Errorf("unexpected reasoning: %q", result.Reasoning)
	}
}

// Test 4: Multiple <think> blocks are merged with newline.
func TestGenerateCompletion_MultipleThinkBlocks(t *testing.T) {
	t.Parallel()
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := "<think>First thought.</think>Middle text.<think>Second thought.</think>End."
		writeChatJSON(w, chatResponse(body, ""))
	})

	resolver := resolverForServer(srv, "qwen3")
	result, err := GenerateCompletion(context.Background(), resolver, "Tell me.", "", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Middle text.\nEnd." {
		t.Errorf("unexpected content: %q", result.Content)
	}
	if result.Reasoning != "First thought.\nSecond thought." {
		t.Errorf("unexpected reasoning: %q", result.Reasoning)
	}
}

// Test 5: Retry on error — first call fails with 500, second succeeds.
func TestGenerateCompletion_RetryOnError(t *testing.T) {
	t.Parallel()
	var callCount int32

	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			// First attempt: return server error.
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"temporary failure"}`))
			return
		}
		// Second attempt: succeed.
		writeChatJSON(w, chatResponse("Recovered!", ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	result, err := GenerateCompletion(context.Background(), resolver, "Hi", "", "", false)
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if result.Content != "Recovered!" {
		t.Errorf("unexpected content: %q", result.Content)
	}
	if atomic.LoadInt32(&callCount) < 2 {
		t.Errorf("expected at least 2 calls, got %d", callCount)
	}
}

// Test 6: All retries fail — error is returned.
func TestGenerateCompletion_AllRetriesFail(t *testing.T) {
	t.Parallel()
	var callCount int32

	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"always fails"}`)
	})

	resolver := resolverForServer(srv, "gpt-4o")
	_, err := GenerateCompletion(context.Background(), resolver, "Hi", "", "", false)
	if err == nil {
		t.Fatal("expected error when all retries fail, got nil")
	}
	if atomic.LoadInt32(&callCount) != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", callCount)
	}
}

// Test 7: o1 model — system prompt is NOT added even when non-empty.
func TestGenerateCompletion_O1ModelNoSystemPrompt(t *testing.T) {
	t.Parallel()
	var capturedMessages []ChatMessage

	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			capturedMessages = req.Messages
		}
		writeChatJSON(w, chatResponse("Done.", ""))
	})

	resolver := resolverForServer(srv, "o1-mini")
	_, err := GenerateCompletion(context.Background(), resolver, "What is 2+2?", "You are a math tutor.", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, m := range capturedMessages {
		if m.Role == "system" {
			t.Errorf("o1 model should not receive a system message, but got one: %q", m.Content)
		}
	}
	if len(capturedMessages) != 1 || capturedMessages[0].Role != "user" {
		t.Errorf("expected exactly 1 user message, got %v", capturedMessages)
	}
}

// Test 8: GenerateCompletionWithModel — model override is sent to the server.
func TestGenerateCompletionWithModel_UsesOverride(t *testing.T) {
	t.Parallel()
	var capturedModel string

	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			capturedModel = req.Model
		}
		writeChatJSON(w, chatResponse("OK", ""))
	})

	resolver := resolverForServer(srv, "gpt-4o", "fast-model")
	_, err := GenerateCompletionWithModel(context.Background(), resolver, "Hi", "", "", false, "fast-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedModel != "fast-model" {
		t.Errorf("expected model %q, got %q", "fast-model", capturedModel)
	}
}

// Test 9: GenerateCompletionWithModel — empty override falls back to config.ChatModel.
func TestGenerateCompletionWithModel_FallsBackToChatModel(t *testing.T) {
	t.Parallel()
	var capturedModel string

	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			capturedModel = req.Model
		}
		writeChatJSON(w, chatResponse("OK", ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	_, err := GenerateCompletionWithModel(context.Background(), resolver, "Hi", "", "", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedModel != "gpt-4o" {
		t.Errorf("expected model %q, got %q", "gpt-4o", capturedModel)
	}
}

// Test 10: GenerateCompletionWithModel — override not in ChatModels falls back to default.
func TestGenerateCompletionWithModel_UnavailableOverride_FallsBack(t *testing.T) {
	t.Parallel()
	var capturedModel string

	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			capturedModel = req.Model
		}
		writeChatJSON(w, chatResponse("OK", ""))
	})

	// Only "default-model" is in ChatModels; "missing-model" is not.
	resolver := resolverForServer(srv, "default-model")
	_, err := GenerateCompletionWithModel(context.Background(), resolver, "Hi", "", "", false, "missing-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedModel != "default-model" {
		t.Errorf("expected fallback to %q, got %q", "default-model", capturedModel)
	}
}

// Test 11: GenerateCompletionWithModel — override present in ChatModels is used.
func TestGenerateCompletionWithModel_AvailableOverride_UsesIt(t *testing.T) {
	t.Parallel()
	var capturedModel string

	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			capturedModel = req.Model
		}
		writeChatJSON(w, chatResponse("OK", ""))
	})

	// "fast-model" is explicitly included in ChatModels.
	resolver := resolverForServer(srv, "default-model", "fast-model")
	_, err := GenerateCompletionWithModel(context.Background(), resolver, "Hi", "", "", false, "fast-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedModel != "fast-model" {
		t.Errorf("expected model %q, got %q", "fast-model", capturedModel)
	}
}

// Test 12: GenerateCompletionWithModelDeterministic — temperature is 0.
func TestGenerateCompletionWithModelDeterministic_UsesTemperatureZero(t *testing.T) {
	t.Parallel()
	var gotTemp *float64
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			gotTemp = req.Temperature
		}
		writeChatJSON(w, chatResponse("OK", ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	_, err := GenerateCompletionWithModelDeterministic(context.Background(), resolver, "hi", "sys", "", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTemp == nil {
		t.Fatal("expected temperature set, got nil")
	}
	if *gotTemp != 0 {
		t.Fatalf("expected temperature 0, got %v", *gotTemp)
	}
}

// ---------------------------------------------------------------------------
// extractReasoning unit tests
// ---------------------------------------------------------------------------

func TestExtractReasoning_PrefersReasoningContent(t *testing.T) {
	t.Parallel()
	choice := ChatChoice{}
	choice.Message.Content = "answer"
	choice.Message.ReasoningContent = "rc reasoning"
	choice.Message.Reasoning = "r reasoning"

	reasoning, content := extractReasoning(choice)
	if reasoning != "rc reasoning" {
		t.Errorf("expected ReasoningContent to win, got %q", reasoning)
	}
	if content != "answer" {
		t.Errorf("expected content unchanged, got %q", content)
	}
}

func TestExtractReasoning_FallsBackToReasoning(t *testing.T) {
	t.Parallel()
	choice := ChatChoice{}
	choice.Message.Content = "answer"
	choice.Message.Reasoning = "r reasoning"

	reasoning, content := extractReasoning(choice)
	if reasoning != "r reasoning" {
		t.Errorf("expected Reasoning field, got %q", reasoning)
	}
	if content != "answer" {
		t.Errorf("expected content unchanged, got %q", content)
	}
}

func TestExtractReasoning_FallsBackToThinkTags(t *testing.T) {
	t.Parallel()
	choice := ChatChoice{}
	choice.Message.Content = "<think>tag reasoning</think>answer"

	reasoning, content := extractReasoning(choice)
	if reasoning != "tag reasoning" {
		t.Errorf("expected think tag reasoning, got %q", reasoning)
	}
	if content != "answer" {
		t.Errorf("expected think tags stripped, got %q", content)
	}
}

func TestExtractReasoning_NoReasoning(t *testing.T) {
	t.Parallel()
	choice := ChatChoice{}
	choice.Message.Content = "just an answer"

	reasoning, content := extractReasoning(choice)
	if reasoning != "" {
		t.Errorf("expected empty reasoning, got %q", reasoning)
	}
	if content != "just an answer" {
		t.Errorf("expected content unchanged, got %q", content)
	}
}

// ---------------------------------------------------------------------------
// stripThinkTags unit tests
// ---------------------------------------------------------------------------

func TestStripThinkTags_NoTags(t *testing.T) {
	t.Parallel()
	reasoning, content := stripThinkTags("Plain text.")
	if content != "Plain text." {
		t.Errorf("unexpected content: %q", content)
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning, got %q", reasoning)
	}
}

func TestStripThinkTags_SingleTag(t *testing.T) {
	t.Parallel()
	reasoning, content := stripThinkTags("<think>I reasoned.</think>Answer.")
	if content != "Answer." {
		t.Errorf("unexpected content: %q", content)
	}
	if reasoning != "I reasoned." {
		t.Errorf("unexpected reasoning: %q", reasoning)
	}
}

func TestStripThinkTags_MultilineThink(t *testing.T) {
	t.Parallel()
	input := "<think>\nLine one.\nLine two.\n</think>\nResult."
	reasoning, content := stripThinkTags(input)
	if content != "Result." {
		t.Errorf("unexpected content: %q", content)
	}
	if reasoning != "Line one.\nLine two." {
		t.Errorf("unexpected reasoning: %q", reasoning)
	}
}

func TestStripThinkTags_MultipleTags(t *testing.T) {
	t.Parallel()
	input := "<think>A</think>middle<think>B</think>end"
	reasoning, content := stripThinkTags(input)
	if content != "middle\nend" {
		t.Errorf("unexpected content: %q", content)
	}
	if reasoning != "A\nB" {
		t.Errorf("unexpected reasoning: %q", reasoning)
	}
}

// TestChatUsage_CachedTokens verifies the OpenAI / DeepSeek precedence: nested
// prompt_tokens_details.cached_tokens wins, with fallback to top-level
// prompt_cache_hit_tokens.
func TestChatUsage_CachedTokens(t *testing.T) {
	t.Parallel()
	t.Run("openai-style nested wins", func(t *testing.T) {
		t.Parallel()
		var u ChatUsage
		u.PromptTokensDetails.CachedTokens = 1024
		u.PromptCacheHitTokens = 2048 // would be wrong to pick this
		if got := u.CachedTokens(); got != 1024 {
			t.Errorf("expected 1024 (nested), got %d", got)
		}
	})

	t.Run("deepseek-style top-level fallback", func(t *testing.T) {
		t.Parallel()
		var u ChatUsage
		u.PromptCacheHitTokens = 768
		if got := u.CachedTokens(); got != 768 {
			t.Errorf("expected 768 (deepseek fallback), got %d", got)
		}
	})

	t.Run("no cache hit returns zero", func(t *testing.T) {
		t.Parallel()
		var u ChatUsage
		u.PromptTokens = 1500
		if got := u.CachedTokens(); got != 0 {
			t.Errorf("expected 0 when no cache hit reported, got %d", got)
		}
	})
}
