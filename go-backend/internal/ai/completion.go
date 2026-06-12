package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
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
	return GenerateCompletionWithHistory(ctx, resolver, nil, prompt, systemPrompt, kbID, wantReasoning)
}

// GenerateCompletionWithHistory is GenerateCompletion plus recent
// conversation turns inserted between the system prompt and the current user
// message (see BuildAnswerMessages). A nil/empty history is byte-identical
// to GenerateCompletion.
func GenerateCompletionWithHistory(ctx context.Context, resolver *ConfigResolver, history []ChatHistoryEntry, prompt, systemPrompt, kbID string, wantReasoning bool) (*CompletionResult, error) {
	config, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("ai: resolve config: %w", err)
	}
	return generateCompletionHistoryInner(ctx, config, history, prompt, systemPrompt, config.ChatModel, wantReasoning, 0.2)
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

// StructuredSpec describes an optional Structured-Outputs contract for a
// completion call: the request is sent with a strict `json_schema`
// response_format (vLLM guided_json / OpenAI Structured Outputs) and on
// rejection downgraded to the looser `json_object` contract, so the call
// degrades gracefully on backends without grammar support. Callers keep
// their tolerant JSON parsing as the last line of defense.
type StructuredSpec struct {
	Name   string
	Schema json.RawMessage
}

// GenerateCompletionStructured is GenerateCompletionWithModelDeterministic
// plus a Structured-Outputs contract. Classifier/planner-style fast-tier
// calls want both properties together: temperature 0 (same input → same
// verdict) and grammar-enforced output shape (the dominant failure mode of
// small models on these calls is malformed JSON, not wrong intent). A nil
// spec behaves exactly like GenerateCompletionWithModelDeterministic.
func GenerateCompletionStructured(ctx context.Context, resolver *ConfigResolver, prompt, systemPrompt, kbID, modelOverride string, spec *StructuredSpec) (*CompletionResult, error) {
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
	return generateCompletionStructuredInner(ctx, config, nil, prompt, systemPrompt, model, false, 0.0, spec)
}

// structuredCompletionFn is the Structured-Outputs sibling of completionFn:
// it dispatches through the same per-resolver test hook (which ignores the
// spec — hooked tests assert on prompts and parsing, not wire format), and
// in production adds the strict-json_schema contract on top of the
// deterministic path.
func (r *ConfigResolver) structuredCompletionFn(ctx context.Context, prompt, systemPrompt, kbID, modelOverride string, spec *StructuredSpec) (*CompletionResult, error) {
	if h := r.completionHook.Load(); h != nil {
		return (*h)(ctx, r, prompt, systemPrompt, kbID, modelOverride)
	}
	return GenerateCompletionStructured(ctx, r, prompt, systemPrompt, kbID, modelOverride, spec)
}

// generateCompletionInner performs the actual chat completion request using
// the supplied resolved config and model name. The model parameter replaces
// any reference to config.ChatModel so callers can override it.
func generateCompletionInner(ctx context.Context, config *ResolvedConfig, prompt, systemPrompt, model string, wantReasoning bool, temperature float64) (*CompletionResult, error) {
	return generateCompletionHistoryInner(ctx, config, nil, prompt, systemPrompt, model, wantReasoning, temperature)
}

func generateCompletionHistoryInner(ctx context.Context, config *ResolvedConfig, history []ChatHistoryEntry, prompt, systemPrompt, model string, wantReasoning bool, temperature float64) (*CompletionResult, error) {
	return generateCompletionStructuredInner(ctx, config, history, prompt, systemPrompt, model, wantReasoning, temperature, nil)
}

func generateCompletionStructuredInner(ctx context.Context, config *ResolvedConfig, history []ChatHistoryEntry, prompt, systemPrompt, model string, wantReasoning bool, temperature float64, structured *StructuredSpec) (*CompletionResult, error) {
	ctx, span := observability.StartGenerationSpan(ctx, "rag.llm_completion", observability.GenerationAttrs{
		System: observability.ProviderSystemFromBaseURL(config.BaseURL),
		Model:  model,
	})
	defer span.End()

	messages := BuildAnswerMessages(systemPrompt, model, history, prompt)

	req := &ChatRequest{
		Model:       model,
		Messages:    messages,
		Temperature: &temperature,
	}
	if structured != nil {
		req.ResponseFormat = &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &ResponseJSONSchema{
				Name:   structured.Name,
				Strict: true,
				Schema: structured.Schema,
			},
		}
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
			// Some backends don't support strict json_schema — downgrade
			// to the looser json_object contract for the remaining
			// attempts so the feature degrades gracefully on older
			// LiteLLM / vLLM stacks (same fallback as the enumeration
			// pre-pass). A transient network error also takes this path;
			// that costs only strictness, not correctness, since every
			// caller keeps tolerant JSON parsing.
			if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_schema" {
				logctx.From(ctx).Info("ai.structured.strict_schema_rejected_falling_back",
					"error", err, "model", model)
				req.ResponseFormat = &ResponseFormat{Type: "json_object"}
			}
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

	parts := make([]string, 0, len(matches)+1)
	for _, m := range matches {
		parts = append(parts, strings.TrimSpace(m[1]))
	}

	// Replace each *closed* think block with a newline so surrounding text
	// doesn't merge.
	content = thinkTagRe.ReplaceAllString(text, "\n")

	// An unclosed <think> (the model opened a reasoning block but the
	// completion ended — or was truncated by the token limit — before
	// </think>) is not matched by the paired regex above, so the opening tag
	// and every reasoning token after it would otherwise leak into the
	// user-facing answer. The streaming ThinkTagFilter already routes such a
	// tail to reasoning; mirror that here for the non-streaming path: treat
	// everything from the orphaned <think> onward as reasoning and drop it
	// from content.
	if idx := strings.Index(content, "<think>"); idx >= 0 {
		if orphan := strings.TrimSpace(content[idx+len("<think>"):]); orphan != "" {
			parts = append(parts, orphan)
		}
		content = content[:idx]
	}

	reasoning = strings.Join(parts, "\n")
	// Collapse runs of whitespace-only lines and trim edges.
	content = strings.TrimSpace(content)

	return reasoning, content
}
