package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/prompts"
)

// hypeDocPrefixTokenCap matches the contextual-prefix / KG budget.
const hypeDocPrefixTokenCap = 8000

// GenerateHypotheticalQuestions returns up to maxQuestions self-contained
// questions the chunk answers. The full document is sent as a cacheable
// prefix (auto-cached by the provider across chunks of the same file),
// mirroring GenerateChunkContext. lang is the KB's raw two-letter code.
// modelOverride empty falls back to the resolver default. Best-effort:
// any LLM/parse error returns (nil, err); the caller logs and proceeds.
func GenerateHypotheticalQuestions(ctx context.Context, resolver *ConfigResolver, fileName, document, chunk, kbID string, maxQuestions int, lang, modelOverride string) ([]string, error) {
	if maxQuestions <= 0 {
		return nil, nil
	}
	truncatedDoc := truncateToTokens(document, hypeDocPrefixTokenCap)
	sys := prompts.HyPEQuestionsSystemPrompt(lang)
	user := prompts.HyPEQuestionsUserPrompt(fileName, truncatedDoc, chunk, maxQuestions)

	result, err := GenerateCompletionWithModel(ctx, resolver, user, sys, kbID, false, modelOverride)
	if err != nil {
		return nil, fmt.Errorf("hype: completion: %w", err)
	}
	if result == nil || strings.TrimSpace(result.Content) == "" {
		return nil, fmt.Errorf("hype: empty completion")
	}
	return parseHyPEQuestions(result.Content, maxQuestions), nil
}

// parseHyPEQuestions tolerantly extracts a JSON string array from the
// model output (optionally wrapped in a ```json fence), trims each
// entry, drops empties, and caps to max.
func parseHyPEQuestions(raw string, max int) []string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		s = strings.TrimSpace(s)
	}
	if lb, rb := strings.IndexByte(s, '['), strings.LastIndexByte(s, ']'); lb >= 0 && rb > lb {
		s = s[lb : rb+1]
	}
	var arr []string
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, q := range arr {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		out = append(out, q)
		if len(out) >= max {
			break
		}
	}
	return out
}
