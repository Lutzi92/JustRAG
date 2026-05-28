package ai

import (
	"context"
	"strings"

	"github.com/justrag/go-backend/internal/prompts"
)

// GenerateStepBackQuery generates a single broader query that retrieves
// the union of contexts a comparative/multi-hop query needs. Returns the
// trimmed first non-empty line; an empty string when the LLM produced no
// usable output. Errors propagate so callers can decide how to handle
// LLM failures (the search pipeline treats step-back as best-effort and
// fails open).
func GenerateStepBackQuery(ctx context.Context, resolver *ConfigResolver, query, kbID, lang string) (string, error) {
	return GenerateStepBackQueryWithModel(ctx, resolver, query, kbID, lang, "")
}

// GenerateStepBackQueryWithModel is like GenerateStepBackQuery but lets
// the caller override the chat model. Empty modelOverride falls back to
// the resolver's ChatModel.
func GenerateStepBackQueryWithModel(ctx context.Context, resolver *ConfigResolver, query, kbID, lang, modelOverride string) (string, error) {
	result, err := GenerateCompletionWithModel(ctx, resolver, query, prompts.StepBackPrompt(lang), kbID, false, modelOverride)
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(result.Content, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed, nil
		}
	}
	return "", nil
}
