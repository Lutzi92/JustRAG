package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/prompts"
)

// driftFollowupsSpec is the Structured-Outputs contract: a bare JSON array
// of strings (mirrors decomposeSpec; vLLM guided_json accepts a top-level
// array, and on the json_object downgrade parseJSONStringArray's substring
// extraction still recovers the embedded array).
var driftFollowupsSpec = &StructuredSpec{
	Name: "drift_followups",
	Schema: json.RawMessage(`{
		"type": "array",
		"items": {"type": "string"}
	}`),
}

// maxDriftFollowupsHardCap bounds the LLM output regardless of the caller's
// configured max — the orchestrator additionally clamps to its own knob.
const maxDriftFollowupsHardCap = 8

// GenerateDriftFollowups asks the LLM for specific follow-up sub-questions
// that drill into the themes a community-level primer surfaces for a broad
// (global-synthesis) question. Empty primerText is valid: the prompt then
// decomposes the question itself. Returns an empty slice (no error) when the
// model emits no usable array — the DRIFT orchestrator treats empty as a
// fallback (search the original query). An LLM transport error bubbles.
func GenerateDriftFollowups(ctx context.Context, resolver *ConfigResolver, kbID, query, primerText, language, modelOverride string) ([]string, error) {
	user := buildDriftFollowupUserPrompt(query, primerText)
	result, err := resolver.structuredCompletionFn(ctx, user, prompts.DriftFollowupsPrompt(language), kbID, modelOverride, driftFollowupsSpec)
	if err != nil {
		return nil, err
	}
	items := parseJSONStringArray(result.Content)
	out := make([]string, 0, len(items))
	for _, q := range items {
		if t := strings.TrimSpace(q); t != "" {
			out = append(out, t)
		}
	}
	if len(out) > maxDriftFollowupsHardCap {
		out = out[:maxDriftFollowupsHardCap]
	}
	return out, nil
}

func buildDriftFollowupUserPrompt(query, primerText string) string {
	primer := strings.TrimSpace(primerText)
	if primer == "" {
		return fmt.Sprintf("Original question:\n%s\n\n(No community-level primer is available — decompose the question itself.)", query)
	}
	return fmt.Sprintf("Original question:\n%s\n\nCommunity-level primer (summaries of thematic clusters across the knowledge base):\n%s", query, primer)
}
