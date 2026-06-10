package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/prompts"
)

// completionFunc is the LLM-call shape consumed by NeedsMoreInfo,
// VerifyFactuality, DecideNextAction, PlanQueries, and friends. Tests can
// install a per-resolver override via newTestResolverWithCompletion (see
// completion_hook_test.go); production code leaves the override nil and
// falls through to GenerateCompletionWithModel.
type completionFunc func(ctx context.Context, resolver *ConfigResolver, prompt, systemPrompt, kbID, modelOverride string) (*CompletionResult, error)

// completionFn dispatches the LLM call through the resolver's per-instance
// hook when one is installed, otherwise calls GenerateCompletionWithModel.
// resolver must be non-nil — production paths always have one, and tests
// that previously relied on a package-level hook now build a resolver via
// the test helper.
func (r *ConfigResolver) completionFn(ctx context.Context, prompt, systemPrompt, kbID, modelOverride string) (*CompletionResult, error) {
	if h := r.completionHook.Load(); h != nil {
		return (*h)(ctx, r, prompt, systemPrompt, kbID, modelOverride)
	}
	return GenerateCompletionWithModel(ctx, r, prompt, systemPrompt, kbID, false, modelOverride)
}

// needsMoreInfoResponse is the shape NeedsMoreInfo expects from the LLM.
// FollowUpQuery is intentionally a string (not omitempty / pointer) so a
// missing "follow_up_query" key in the LLM output deserializes to "" and
// the caller can treat that as "insufficient but no proposed query —
// bail" without further parsing.
type needsMoreInfoResponse struct {
	Sufficient    bool   `json:"sufficient"`
	FollowUpQuery string `json:"follow_up_query"`
}

// needsMoreInfoSpec is the Structured-Outputs contract for
// NeedsMoreInfo. The prompt allows omitting follow_up_query when
// sufficient=true, but strict mode requires every property, so the
// model emits "" in that case — exactly the zero value the parser
// already produces for a missing key. parseNeedsMoreInfoJSON stays
// tolerant for the json_object fallback path.
var needsMoreInfoSpec = &StructuredSpec{
	Name: "needs_more_info",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"sufficient": {"type": "boolean"},
			"follow_up_query": {"type": "string"}
		},
		"required": ["sufficient", "follow_up_query"],
		"additionalProperties": false
	}`),
}

// NeedsMoreInfo asks the LLM whether the chunks accumulated so far
// suffice to answer the question. Returns:
//   - sufficient=true: the LLM thinks current chunks are enough; caller
//     should stop the agentic loop with outcome="early_stop_sufficient".
//   - sufficient=false, followUp != "": the LLM identified a gap and
//     proposed a focused follow-up query; caller should run another
//     search hop with that query.
//   - sufficient=false, followUp == "": the LLM said insufficient but
//     didn't propose a query (rare, depends on model behavior). Caller
//     should treat as "give up", outcome="early_stop_no_query".
//
// modelOverride lets the caller route this judgment to a smaller / faster
// model than the main chat model — typically the configured enrichment
// model. Empty string falls back to the resolver's default ChatModel.
//
// LLM errors and JSON-parse errors bubble up; the caller decides how to
// degrade (the orchestrator records outcome="judge_error" and proceeds
// with the chunks already in hand — agentic chat is fail-open).
func NeedsMoreInfo(ctx context.Context, resolver *ConfigResolver, kbID, question, chunkSummaries, language, modelOverride string) (bool, string, error) {
	sys := prompts.AgenticNeedsMoreInfoSystemPrompt(language)
	user := prompts.AgenticNeedsMoreInfoUserPrompt(question, chunkSummaries)

	res, err := resolver.structuredCompletionFn(ctx, user, sys, kbID, modelOverride, needsMoreInfoSpec)
	if err != nil {
		return false, "", fmt.Errorf("needs_more_info: completion: %w", err)
	}
	if res == nil || res.Content == "" {
		return false, "", fmt.Errorf("needs_more_info: empty completion")
	}

	parsed, perr := parseNeedsMoreInfoJSON(res.Content)
	if perr != nil {
		return false, "", fmt.Errorf("needs_more_info: parse: %w", perr)
	}
	return parsed.Sufficient, strings.TrimSpace(parsed.FollowUpQuery), nil
}

// parseNeedsMoreInfoJSON tolerates LLM preamble around the JSON object
// (some models prefix "Here is the answer:" despite the system prompt's
// "no preamble" instruction). It first tries strict unmarshal; if that
// fails, scans for the first '{' and last '}' and tries again.
func parseNeedsMoreInfoJSON(text string) (needsMoreInfoResponse, error) {
	var out needsMoreInfoResponse
	trimmed := strings.TrimSpace(text)
	if err := json.Unmarshal([]byte(trimmed), &out); err == nil {
		return out, nil
	}
	// Fallback: extract the JSON object embedded in surrounding prose.
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(trimmed[start:end+1]), &out); err == nil {
			return out, nil
		}
	}
	return needsMoreInfoResponse{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}
