package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/aibudget"
	"github.com/justrag/go-backend/internal/prompts"
)

// IterateActionMaxQueries caps the per-round sub-query fan-out. Caller
// (orchestrator) inherits this cap; intentionally not configurable to
// keep the admin surface small (MaxSubQueries governs the Plan stage
// only).
const IterateActionMaxQueries = 3

type iterateActionResponse struct {
	Action  string   `json:"action"`
	Queries []string `json:"queries"`
	Reason  string   `json:"reason"`
}

// DecideNextAction asks the LLM, given the current question + chunks
// accumulated so far, whether to search again (and propose 0..3
// sub-queries) or answer.
//
// Defensive normalization:
//   - action=="search" with empty queries → returned as "answer"
//     (orchestrator can't make progress with no queries; cleanest exit
//     is to terminate the loop).
//   - queries are trimmed, empty entries dropped, list capped at
//     IterateActionMaxQueries.
//
// Errors bubble up; caller treats LLM/parse error as outcome=iter_error
// and breaks the loop with the chunks already in hand.
func DecideNextAction(ctx context.Context, resolver *ConfigResolver, kbID, question, chunkSummaries string, round int, language, modelOverride string) (string, []string, string, error) {
	sys := prompts.IterateActionSystemPrompt(language)
	user := prompts.IterateActionUserPrompt(question, chunkSummaries, round)

	res, err := resolver.completionFn(ctx, user, sys, kbID, modelOverride)
	if err != nil {
		return "", nil, "", fmt.Errorf("decide_next_action: completion: %w", err)
	}
	if res == nil || res.Content == "" {
		return "", nil, "", fmt.Errorf("decide_next_action: empty completion")
	}
	aibudget.Add(ctx, res.PromptTokens+res.CompletionTokens)

	parsed, perr := parseIterateActionJSON(res.Content)
	if perr != nil {
		return "", nil, "", fmt.Errorf("decide_next_action: parse: %w", perr)
	}

	switch parsed.Action {
	case "answer", "search":
		// ok
	default:
		return "", nil, "", fmt.Errorf("decide_next_action: unknown action %q", parsed.Action)
	}

	cleaned := make([]string, 0, len(parsed.Queries))
	for _, q := range parsed.Queries {
		if t := strings.TrimSpace(q); t != "" {
			cleaned = append(cleaned, t)
		}
		if len(cleaned) >= IterateActionMaxQueries {
			break
		}
	}

	a := parsed.Action
	if a == "search" && len(cleaned) == 0 {
		a = "answer"
	}

	return a, cleaned, strings.TrimSpace(parsed.Reason), nil
}

func parseIterateActionJSON(text string) (iterateActionResponse, error) {
	var out iterateActionResponse
	trimmed := strings.TrimSpace(text)
	if err := json.Unmarshal([]byte(trimmed), &out); err == nil {
		return out, nil
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(trimmed[start:end+1]), &out); err == nil {
			return out, nil
		}
	}
	return iterateActionResponse{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}
