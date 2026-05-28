package ai

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/splitter"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// ChatHistoryEntry represents a single turn in a conversation history.
type ChatHistoryEntry struct {
	Role    string
	Content string
}

// ---------------------------------------------------------------------------
// Role injection prevention
// ---------------------------------------------------------------------------

// roleInjectionRe matches newlines followed by "User:" or "Assistant:" to prevent
// role injection when formatting chat history into a prompt.
var roleInjectionRe = regexp.MustCompile(`\n(User|Assistant):`)

// ---------------------------------------------------------------------------
// JSON parsing helper
// ---------------------------------------------------------------------------

// parseJSONStringArray tries to parse text as a JSON string array.
// It first attempts a direct unmarshal; if that fails it tries to extract the
// substring between the first '[' and last ']' and parse that.
// Returns nil on failure.
func parseJSONStringArray(text string) []string {
	text = strings.TrimSpace(text)

	// Convert once; both attempts reuse the same backing []byte. The LLM
	// frequently wraps its JSON array in prose, so the first unmarshal
	// fails on every multi-query/HyDE response and we fall through to the
	// substring extraction below.
	b := []byte(text)

	var result []string
	if err := json.Unmarshal(b, &result); err == nil {
		return result
	}

	// Try to find a JSON array embedded in the text.
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start >= 0 && end > start {
		if err := json.Unmarshal(b[start:end+1], &result); err == nil {
			return result
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Query enhancement functions
// ---------------------------------------------------------------------------

// RewriteQuery rewrites a search query to be more precise using the AI model.
func RewriteQuery(ctx context.Context, resolver *ConfigResolver, query, kbID, lang string) (string, error) {
	return RewriteQueryWithModel(ctx, resolver, query, kbID, lang, "")
}

// RewriteQueryWithModel is like RewriteQuery but lets the caller override the
// chat model (e.g. to route deep-chat planning calls through a faster model).
// Empty modelOverride falls back to the resolver's ChatModel.
func RewriteQueryWithModel(ctx context.Context, resolver *ConfigResolver, query, kbID, lang, modelOverride string) (string, error) {
	result, err := GenerateCompletionWithModel(ctx, resolver, query, prompts.QueryRewritePrompt(lang), kbID, false, modelOverride)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// ExpandQuery expands a search query with related terms and synonyms.
// The returned string is the original query joined with the AI-generated expansion.
func ExpandQuery(ctx context.Context, resolver *ConfigResolver, query, kbID, lang string) (string, error) {
	return ExpandQueryWithModel(ctx, resolver, query, kbID, lang, "")
}

// ExpandQueryWithModel is like ExpandQuery but lets the caller override the
// chat model. Empty modelOverride falls back to the resolver's ChatModel.
func ExpandQueryWithModel(ctx context.Context, resolver *ConfigResolver, query, kbID, lang, modelOverride string) (string, error) {
	result, err := GenerateCompletionWithModel(ctx, resolver, query, prompts.QueryExpandPrompt(lang), kbID, false, modelOverride)
	if err != nil {
		return "", err
	}
	return query + " " + result.Content, nil
}

// SpellCorrect returns a spell-corrected version of the query.
func SpellCorrect(ctx context.Context, resolver *ConfigResolver, query, kbID, lang string) (string, error) {
	result, err := GenerateCompletion(ctx, resolver, query, prompts.SpellCorrectPrompt(lang), kbID, false)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// CondenseQuestion turns a follow-up question into a standalone query by
// incorporating context from the conversation history.
// When history is empty the follow-up is returned unchanged without an API call.
func CondenseQuestion(ctx context.Context, resolver *ConfigResolver, history []ChatHistoryEntry, followUp, kbID, lang string) (string, error) {
	if len(history) == 0 {
		return followUp, nil
	}

	// Format history as "Role: content\n..." lines.
	// Pre-size the builder to skip geometric reallocation: on a long
	// multi-turn conversation (~20 turns × ~200 chars) the default growth
	// rehashes ~5 times. role + ": " + content + "\n" is the lower bound
	// (role-injection sanitization can grow the content a few bytes).
	var totalLen int
	for _, entry := range history {
		totalLen += len(entry.Role) + len(": ") + len(entry.Content) + len("\n")
	}
	var sb strings.Builder
	sb.Grow(totalLen)
	for _, entry := range history {
		role := entry.Role
		// Capitalise first letter for readability (user -> User, assistant -> Assistant).
		if len(role) > 0 {
			role = strings.ToUpper(role[:1]) + role[1:]
		}
		sb.WriteString(role)
		sb.WriteString(": ")
		// Sanitize content to prevent role injection.
		sanitized := roleInjectionRe.ReplaceAllString(entry.Content, "\n$1\\:")
		sb.WriteString(sanitized)
		sb.WriteString("\n")
	}

	formattedHistory := strings.TrimRight(sb.String(), "\n")
	userPrompt := prompts.ConversationCondenseUser(formattedHistory, followUp)

	result, err := GenerateCompletion(ctx, resolver, userPrompt, prompts.ConversationCondenseSystem(lang), kbID, false)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Content), nil
}

// GenerateFollowUpQuestions generates up to 3 follow-up questions based on the
// user message and the AI response. On JSON parse failure an empty slice is
// returned (non-critical operation).
func GenerateFollowUpQuestions(ctx context.Context, resolver *ConfigResolver, userMessage, aiResponse, kbID, lang string) ([]string, error) {
	// Truncate aiResponse to avoid excessive token usage.
	truncated := aiResponse
	if len(truncated) > 1500 {
		truncated = truncated[:1500]
	}

	userPrompt := prompts.FollowUpQuestionsUser(userMessage, truncated)
	result, err := GenerateCompletion(ctx, resolver, userPrompt, prompts.FollowUpQuestionsSystem(lang), kbID, false)
	if err != nil {
		return nil, err
	}

	questions := parseJSONStringArray(result.Content)
	if questions == nil {
		return []string{}, nil
	}
	if len(questions) > 3 {
		questions = questions[:3]
	}
	return questions, nil
}

// ClassifyQueryComplexity returns true when the AI classifies the query as
// complex. On JSON parse failure false is returned (default to simple).
func ClassifyQueryComplexity(ctx context.Context, resolver *ConfigResolver, query, kbID, lang string) (bool, error) {
	result, err := GenerateCompletion(ctx, resolver, query, prompts.QueryClassifierPrompt(lang), kbID, false)
	if err != nil {
		return false, err
	}

	var parsed struct {
		IsComplex bool `json:"isComplex"`
	}
	text := strings.TrimSpace(result.Content)

	// Try direct unmarshal first.
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		return parsed.IsComplex, nil
	}

	// Try to extract embedded JSON object.
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(text[start:end+1]), &parsed); err == nil {
			return parsed.IsComplex, nil
		}
	}

	return false, nil
}

// GenerateHypotheticalAnswer generates a hypothetical answer for use in
// HyDE (Hypothetical Document Embeddings) retrieval.
func GenerateHypotheticalAnswer(ctx context.Context, resolver *ConfigResolver, query, kbID, lang string) (string, error) {
	return GenerateHypotheticalAnswerWithModel(ctx, resolver, query, kbID, lang, "")
}

// GenerateHypotheticalAnswerWithModel is like GenerateHypotheticalAnswer but
// lets the caller override the chat model. Empty modelOverride falls back to
// the resolver's ChatModel.
func GenerateHypotheticalAnswerWithModel(ctx context.Context, resolver *ConfigResolver, query, kbID, lang, modelOverride string) (string, error) {
	result, err := GenerateCompletionWithModel(ctx, resolver, query, prompts.HyDESystemPrompt(lang), kbID, false, modelOverride)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Content), nil
}

// GenerateMultiQueries generates up to 3 alternative phrasings of the query.
// On JSON parse failure an empty slice is returned (non-critical operation).
func GenerateMultiQueries(ctx context.Context, resolver *ConfigResolver, query, kbID, lang string) ([]string, error) {
	return GenerateMultiQueriesWithModel(ctx, resolver, query, kbID, lang, "")
}

// GenerateMultiQueriesWithModel is like GenerateMultiQueries but lets the
// caller override the chat model. Empty modelOverride falls back to the
// resolver's ChatModel.
func GenerateMultiQueriesWithModel(ctx context.Context, resolver *ConfigResolver, query, kbID, lang, modelOverride string) ([]string, error) {
	result, err := GenerateCompletionWithModel(ctx, resolver, query, prompts.MultiQueryPrompt(lang), kbID, false, modelOverride)
	if err != nil {
		return nil, err
	}

	queries := parseJSONStringArray(result.Content)
	if queries == nil {
		return []string{}, nil
	}
	if len(queries) > 3 {
		queries = queries[:3]
	}
	return queries, nil
}

// contextualPrefixDocBudget is the cl100k_base token budget for the document
// portion of the Contextual Retrieval prompt. The document is the cacheable
// prefix shared across every chunk-context call for one file, so it pays to
// include as much as the model can comfortably handle while keeping the
// per-call cost bounded.
const contextualPrefixDocBudget = 8000

// GenerateChunkContext returns an Anthropic-style 1-sentence context line
// situating chunk inside its source document. The full document is sent
// each call (truncated to ~8k cl100k_base tokens) so the model has the
// global picture; the chunk goes at the end of the user message so that
// OpenAI-compatible automatic prompt caching (OpenAI ≥1024-tok prefix,
// DeepSeek auto, vLLM prefix caching, …) can re-use the document prefix
// across all chunks of one file — the ~90 % cost reduction Anthropic's
// cookbook quotes.
//
// modelOverride picks a non-default chat model; empty string uses the
// resolver's configured ChatModel.
func GenerateChunkContext(ctx context.Context, resolver *ConfigResolver, fileName, document, chunk, kbID, modelOverride string) (string, error) {
	truncatedDoc := truncateToTokens(document, contextualPrefixDocBudget)

	userPrompt := prompts.ContextualPrefixUser(fileName, truncatedDoc, chunk)
	result, err := GenerateCompletionWithModel(ctx, resolver, userPrompt, prompts.ContextualPrefixSystem(), kbID, false, modelOverride)
	if err != nil {
		return "", err
	}

	// Surface cache-hit telemetry so operators can verify Contextual
	// Retrieval is actually amortising the document prefix. Only logged
	// when the provider reported a non-zero cached-token count.
	if result.CachedPromptTokens > 0 {
		logctx.From(ctx).Info("ai.contextual_prefix.cache_hit",
			slog.Int("cached_tokens", result.CachedPromptTokens),
			slog.Int("prompt_tokens", result.PromptTokens),
		)
	}

	return strings.TrimSpace(result.Content), nil
}

// truncateToTokens trims s so its cl100k_base token count does not exceed
// budget. Falls back to a character estimate if the tokenizer is
// unavailable. Head-only truncation (Anthropic cookbook strategy) — most
// documents have their highest-signal context (titles, ToC, opening
// paragraphs) at the start.
func truncateToTokens(s string, budget int) string {
	if budget <= 0 || s == "" {
		return s
	}
	if splitter.CountTokens(s) <= budget {
		return s
	}
	// Binary-search a byte cut-off (snapped to a UTF-8 rune boundary).
	// Probing in the byte domain keeps each `s[:mid]` slice header-only —
	// it shares the original string's backing array, avoiding the
	// `[]rune(s)` materialization (≈4× the byte length on ASCII) and the
	// `string(runes[:mid])` copy per probe that the previous rune-domain
	// search incurred. For per-chunk contextual enrichment on a long
	// document, that's GBs of short-lived heap saved per file.
	lo, hi := 0, len(s)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		// Snap mid down to a valid rune start. If mid lands inside a
		// multi-byte rune, walk back until we hit a rune boundary so we
		// never hand CountTokens an invalid UTF-8 string.
		for mid > 0 && mid < len(s) && !utf8.RuneStart(s[mid]) {
			mid--
		}
		if splitter.CountTokens(s[:mid]) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	// Final boundary snap in case lo landed mid-rune via the hi=mid-1 path.
	for lo > 0 && lo < len(s) && !utf8.RuneStart(s[lo]) {
		lo--
	}
	return s[:lo]
}
