package ai

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/prompts"
)

// sufficientContextSpec is the Structured-Outputs contract for
// JudgeContextSufficiency: a single boolean verdict.
var sufficientContextSpec = &StructuredSpec{
	Name: "sufficient_context",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {"sufficient": {"type": "boolean"}},
		"required": ["sufficient"],
		"additionalProperties": false
	}`),
}

// JudgeContextSufficiency asks a fast-tier LLM whether the assembled
// context as a whole suffices to answer the question (Q2 sufficient-context
// abstention gate). Complements CRAG, which grades each chunk's relevance
// independently: a set of individually "relevant" chunks can still be
// jointly insufficient, which is exactly the regime where models
// hallucinate instead of abstaining.
//
// modelOverride routes the call to a fast-tier model (resolved by the
// caller via chat.ResolveFastTierModel); empty falls back to the KB's
// ChatModel.
//
// Fail-open semantics: any LLM or parse error returns true ("sufficient")
// with a warning log — the gate must never block answers on a flaky judge.
func JudgeContextSufficiency(ctx context.Context, resolver *ConfigResolver, kbID, question, contextText, lang, modelOverride string) bool {
	res, err := resolver.structuredCompletionFn(ctx,
		prompts.SufficientContextUser(question, contextText),
		prompts.SufficientContextSystem(lang),
		kbID, modelOverride, sufficientContextSpec)
	if err != nil {
		logctx.From(ctx).Warn("ai.sufficient_context.llm_failed",
			"error", err, "kbId", kbID, "fallback", "sufficient")
		return true
	}

	var parsed struct {
		Sufficient *bool `json:"sufficient"`
	}
	text := stripJSONFences(strings.TrimSpace(res.Content))
	if err := json.Unmarshal([]byte(text), &parsed); err != nil || parsed.Sufficient == nil {
		if err2 := parseJSONFromText(text, &parsed); err2 != nil || parsed.Sufficient == nil {
			logctx.From(ctx).Warn("ai.sufficient_context.parse_failed",
				"content_preview", truncateForLog(res.Content, 200), "fallback", "sufficient")
			return true
		}
	}
	return *parsed.Sufficient
}
