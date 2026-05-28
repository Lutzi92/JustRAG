package ai

import (
	"context"
	"strings"

	"github.com/justrag/go-backend/internal/prompts"
)

// maxDecomposedSubQueries caps the LLM output the same way GenerateMultiQueries
// caps multi-query output. Beyond ~4 sub-questions the retrieval gain plateaus
// while RRF noise and per-turn token cost keep climbing — pinned at 4 to match
// the prompt's "2-4 sub-questions" instruction.
const maxDecomposedSubQueries = 4

// GenerateSubQueries asks the LLM to decompose a multi-hop / multi-part
// question into 2-4 semantically distinct sub-questions, each retrievable
// independently. Distinct from GenerateMultiQueries: that one produces
// paraphrases of the SAME information need (synonyms, alternative phrasings);
// decomposition produces DIFFERENT information needs whose answers combine
// to resolve the original. Example:
//
//	"Which of Alice's projects has the highest budget and who leads it?"
//	→ ["What projects is Alice on?", "What is the budget of each project?",
//	   "Who leads each project?"]
//
// The result is meant to fan out as additional retrieval queries (folded
// into the RRF pool via vector.SearchOptions.SubQueries) for the standard
// retrieval path. Plan-Execute already runs its own structured decomposition
// at the orchestration level, so callers should NOT additionally invoke
// this when Plan-Execute is the active orchestrator — that would compound
// LLM cost for the same effect.
func GenerateSubQueries(ctx context.Context, resolver *ConfigResolver, query, kbID, lang string) ([]string, error) {
	return GenerateSubQueriesWithModel(ctx, resolver, query, kbID, lang, "")
}

// GenerateSubQueriesWithModel is like GenerateSubQueries but lets the
// caller override the chat model. Empty modelOverride falls back to the
// resolver's ChatModel. Wire fast-tier resolution at the call site via
// chat.ResolveFastTierModel(ctx, reader, "query_decompose_model").
func GenerateSubQueriesWithModel(ctx context.Context, resolver *ConfigResolver, query, kbID, lang, modelOverride string) ([]string, error) {
	result, err := GenerateCompletionWithModel(ctx, resolver, query, prompts.DecomposeQueryPrompt(lang), kbID, false, modelOverride)
	if err != nil {
		return nil, err
	}

	queries := parseJSONStringArray(result.Content)
	if queries == nil {
		return []string{}, nil
	}
	// Drop empties / whitespace-only items; the prompt asks for a JSON
	// string array but mid-tier models occasionally emit "" entries.
	out := queries[:0]
	for _, q := range queries {
		if t := strings.TrimSpace(q); t != "" {
			out = append(out, t)
		}
	}
	if len(out) > maxDecomposedSubQueries {
		out = out[:maxDecomposedSubQueries]
	}
	return out, nil
}
