package ai

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/observability"
)

// thinkTagRe matches <think>...</think> blocks including multiline content.
var thinkTagRe = regexp.MustCompile(`(?s)<think>(.*?)</think>`)

// modelRejectsSystemRole reports whether a model name belongs to a family
// that does not accept the "system" role and therefore needs the system
// prompt either dropped or merged into the user message. Currently this
// matches OpenAI's o1 family (o1, o1-mini, o1-preview). Other reasoning
// models such as DeepSeek-R1, QwQ, and vLLM-served thinkers accept the
// system role normally, so the `is_reasoning` flag on AIModelInfo is the
// wrong gate here — keep this as a targeted family check.
func modelRejectsSystemRole(model string) bool {
	return strings.Contains(model, "o1")
}

// CompletionResult holds the content and optional reasoning from a chat completion.
type CompletionResult struct {
	Content   string
	Reasoning string // from reasoning_content field or <think> tags

	// Token usage as reported by the provider. Zero values indicate the
	// provider did not include a usage block (some self-hosted setups).
	PromptTokens       int
	CompletionTokens   int
	CachedPromptTokens int // OpenAI prompt_tokens_details.cached_tokens or DeepSeek prompt_cache_hit_tokens
}

// GenerateCompletion sends a non-streaming chat request with retry logic.
// wantReasoning enables extraction from ReasoningContent or <think> tags.
func GenerateCompletion(ctx context.Context, resolver *ConfigResolver, prompt, systemPrompt, kbID string, wantReasoning bool) (*CompletionResult, error) {
	config, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("ai: resolve config: %w", err)
	}
	return generateCompletionInner(ctx, config, prompt, systemPrompt, config.ChatModel, wantReasoning, 0.2)
}

// GenerateCompletionWithModel is like GenerateCompletion but lets the caller
// override which chat model is used. When modelOverride is empty the resolved
// config.ChatModel is used, keeping the behaviour identical to GenerateCompletion.
func GenerateCompletionWithModel(ctx context.Context, resolver *ConfigResolver, prompt, systemPrompt, kbID string, wantReasoning bool, modelOverride string) (*CompletionResult, error) {
	config, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("ai: resolve config: %w", err)
	}
	model := config.ChatModel
	if modelOverride != "" {
		if slices.Contains(config.ChatModels, modelOverride) {
			model = modelOverride
		} else {
			observability.RecordModelFallback("not_in_chat_models")
			slog.Warn("ai: model override not available in resolved config, falling back to ChatModel",
				"modelOverride", modelOverride,
				"providerName", config.ProviderName,
				"fallback", config.ChatModel)
		}
	}
	return generateCompletionInner(ctx, config, prompt, systemPrompt, model, wantReasoning, 0.2)
}

// GenerateCompletionWithModelDeterministic is like GenerateCompletionWithModel
// but sets temperature=0 for deterministic judging/evaluation workflows.
// An empty modelOverride or an override not in config.ChatModels falls back
// to config.ChatModel (same behavior as GenerateCompletionWithModel).
func GenerateCompletionWithModelDeterministic(ctx context.Context, resolver *ConfigResolver, prompt, systemPrompt, kbID string, wantReasoning bool, modelOverride string) (*CompletionResult, error) {
	config, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("ai: resolve config: %w", err)
	}
	model := config.ChatModel
	if modelOverride != "" {
		if slices.Contains(config.ChatModels, modelOverride) {
			model = modelOverride
		} else {
			observability.RecordModelFallback("not_in_chat_models")
			slog.Warn("ai: model override not available in resolved config, falling back to ChatModel",
				"modelOverride", modelOverride,
				"providerName", config.ProviderName,
				"fallback", config.ChatModel)
		}
	}
	return generateCompletionInner(ctx, config, prompt, systemPrompt, model, wantReasoning, 0.0)
}

// generateCompletionInner performs the actual chat completion request using
// the supplied resolved config and model name. The model parameter replaces
// any reference to config.ChatModel so callers can override it.
func generateCompletionInner(ctx context.Context, config *ResolvedConfig, prompt, systemPrompt, model string, wantReasoning bool, temperature float64) (*CompletionResult, error) {
	ctx, span := observability.StartGenerationSpan(ctx, "rag.llm_completion", observability.GenerationAttrs{
		System: observability.ProviderSystemFromBaseURL(config.BaseURL),
		Model:  model,
	})
	defer span.End()

	// Build messages. Pre-size to 2 (system + user) so the second append
	// doesn't grow-and-copy: every LLM call goes through this path.
	messages := make([]ChatMessage, 0, 2)
	if systemPrompt != "" && !modelRejectsSystemRole(model) {
		messages = append(messages, ChatMessage{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, ChatMessage{Role: "user", Content: prompt})

	req := &ChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: &temperature,
	}

	// Retry loop: up to 3 attempts with exponential backoff (2s, 4s, 8s).
	const maxAttempts = 3
	backoff := 2 * time.Second

	// Construct the HTTP client once so its connection pool is reused across
	// retries. Building it inside the loop allocated a fresh *http.Transport
	// per attempt and defeated keep-alive on the failing-then-recovering path.
	// CachedClient additionally memoises the Client wrapper across requests
	// so the per-call wrapper alloc disappears at concurrent LLM volume.
	client := CachedClient(config.BaseURL, config.APIKey)

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// time.NewTimer + t.Stop() instead of time.After: when ctx is
			// cancelled mid-backoff, the timer goroutine is freed
			// immediately rather than living for the remainder of the
			// 2-8s backoff. Under retry storms (many concurrent LLM calls
			// timing out) the leaked timers pile up. Mirrors the pattern
			// in embedding.go:217.
			t := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
			backoff *= 2
		}

		resp, err := client.ChatCompletion(ctx, req)
		if err != nil {
			lastErr = err
			continue
		}

		if len(resp.Choices) == 0 {
			lastErr = fmt.Errorf("ai: no choices in response")
			continue
		}

		choice := resp.Choices[0]
		result := &CompletionResult{
			Content:            choice.Message.Content,
			PromptTokens:       resp.Usage.PromptTokens,
			CompletionTokens:   resp.Usage.CompletionTokens,
			CachedPromptTokens: resp.Usage.CachedTokens(),
		}

		if wantReasoning {
			extractedReasoning, cleanedContent := extractReasoning(choice)
			result.Reasoning = extractedReasoning
			result.Content = cleanedContent
		}

		return result, nil
	}

	return nil, fmt.Errorf("ai: all %d attempts failed: %w", maxAttempts, lastErr)
}

// extractReasoning checks multiple locations for reasoning content in priority
// order: ReasoningContent field, Reasoning field, then <think> tags in content.
// It returns the extracted reasoning and the (possibly cleaned) content.
func extractReasoning(choice ChatChoice) (reasoning, content string) {
	content = choice.Message.Content

	// Priority 1: dedicated reasoning_content field (e.g. DeepSeek, QwQ).
	if choice.Message.ReasoningContent != "" {
		return choice.Message.ReasoningContent, content
	}

	// Priority 2: reasoning field (e.g. some OpenAI-compatible providers).
	if choice.Message.Reasoning != "" {
		return choice.Message.Reasoning, content
	}

	// Priority 3: <think> tags embedded in content.
	return stripThinkTags(content)
}

// stripThinkTags removes <think>...</think> tags from text, returning the
// extracted reasoning (joined by newlines) and the cleaned content.
//
// The (reasoning, content) order matches extractReasoning so callers can
// destructure both consistently — the historical mismatch (this function
// used (content, reasoning)) was a documented footgun.
func stripThinkTags(text string) (reasoning, content string) {
	matches := thinkTagRe.FindAllStringSubmatch(text, -1)

	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		parts = append(parts, strings.TrimSpace(m[1]))
	}
	reasoning = strings.Join(parts, "\n")

	// Replace each think block with a newline so surrounding text doesn't merge.
	content = thinkTagRe.ReplaceAllString(text, "\n")
	// Collapse runs of whitespace-only lines and trim edges.
	content = strings.TrimSpace(content)

	return reasoning, content
}
