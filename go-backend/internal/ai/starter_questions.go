package ai

import (
	"context"
	"encoding/json"

	"github.com/justrag/go-backend/internal/prompts"
)

// starterQuestionsSpec is the Structured-Outputs contract for the starter
// questions: a bare JSON array of strings, the same shape as
// driftFollowupsSpec (vLLM guided_json accepts a top-level array; on the
// json_object downgrade parseJSONStringArray still recovers it).
var starterQuestionsSpec = &StructuredSpec{
	Name: "starter_questions",
	Schema: json.RawMessage(`{
		"type": "array",
		"items": {"type": "string"}
	}`),
}

// GenerateStarterQuestions asks the model for up to n questions to open an
// empty chat with, from the KB's name and one excerpt per document.
// modelOverride is the caller's fast-tier pick (model_tier_fast); empty falls
// back to the KB chat model inside GenerateCompletionStructured. The raw
// strings are returned unvalidated — the caller filters them (length, URLs,
// duplicates) before caching or serving. Unparseable output yields an empty
// slice and no error; a transport error is returned as is.
func GenerateStarterQuestions(ctx context.Context, resolver *ConfigResolver, kbID, modelOverride, kbName string, docs []prompts.StarterDoc, lang string, n int) ([]string, error) {
	user := prompts.StarterQuestionsUser(kbName, docs, n)
	res, err := resolver.structuredCompletionFn(ctx, user, prompts.StarterQuestionsSystem(lang, n), kbID, modelOverride, starterQuestionsSpec)
	if err != nil {
		return nil, err
	}
	return parseJSONStringArray(res.Content), nil
}
