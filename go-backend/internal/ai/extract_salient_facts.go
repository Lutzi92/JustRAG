package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
)

// SalientFact is one entry the AP-D1 extractor produces. The
// chat handler's fire-and-forget write goroutine maps this to
// the longmem store row.
type SalientFact struct {
	Kind     string  `json:"kind"`
	Content  string  `json:"content"`
	Salience float64 `json:"salience"`
}

type salientFactsResponse struct {
	Facts []SalientFact `json:"facts"`
}

// salientFactsSpec is the Structured-Outputs contract for the AP-D1
// extractor, matching the prompt's {"facts":[...]} shape. The kind
// enum mirrors prompts.SalientFactKinds; the post-parse normalizer
// still drops drift on the json_object fallback path.
var salientFactsSpec = &StructuredSpec{
	Name: "salient_facts",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"facts": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"kind": {"type": "string", "enum": ["episodic", "semantic", "procedural"]},
						"content": {"type": "string"},
						"salience": {"type": "number"}
					},
					"required": ["kind", "content", "salience"],
					"additionalProperties": false
				}
			}
		},
		"required": ["facts"],
		"additionalProperties": false
	}`),
}

// salientFactContentMaxLen mirrors the 280-char ceiling the prompt
// enforces. The Go-side normalizer trims past this; the storage
// layer's column type is TEXT so over-long entries would land
// untrimmed without it.
const salientFactContentMaxLen = 280

// salientFactsMaxPerCall caps how many facts the storage path
// will accept per LLM call. Mirrors the prompt's "≤5 per call"
// instruction; protects against the LLM emitting a wall-of-text
// extraction.
const salientFactsMaxPerCall = 5

// ExtractSalientFacts asks the LLM to extract durable per-user
// facts from one chat turn. Output is normalised:
//   - empty content drops
//   - kind not in the closed enum drops
//   - content trimmed + capped at 280 chars
//   - salience clamped to [0.0, 1.0]
//   - at most 5 facts surface
//
// On any LLM-side failure (call error, parse error) returns the
// zero slice + a non-nil error. The chat handler's fire-and-
// forget goroutine logs and drops — extraction is best-effort
// by design (a missed fact is recoverable next turn; a thrown
// error must NEVER propagate to the user-facing answer).
//
// modelOverride empty → resolver default. Operators usually
// point this at a small/fast model (Gemma-class, Qwen3-class —
// per-turn extraction is structured-output, not reasoning).
func ExtractSalientFacts(ctx context.Context, resolver *ConfigResolver, userMessage, assistantAnswer, kbID, lang, modelOverride string) ([]SalientFact, error) {
	start := time.Now()
	defer func() {
		observability.ObserveLongmemExtractSeconds(time.Since(start).Seconds())
	}()

	if strings.TrimSpace(userMessage) == "" || strings.TrimSpace(assistantAnswer) == "" {
		// Empty turn: nothing to extract. Not an error — pre-empt
		// the LLM call so we don't pay tokens on no-ops.
		observability.RecordLongmemExtract("empty")
		return nil, nil
	}

	sys := prompts.ExtractSalientFactsSystemPrompt(lang)
	user := prompts.ExtractSalientFactsUserPrompt(userMessage, assistantAnswer)

	res, err := resolver.structuredCompletionFn(ctx, user, sys, kbID, modelOverride, salientFactsSpec)
	if err != nil {
		observability.RecordLongmemExtract("error")
		logctx.From(ctx).Warn("longmem_extract: completion failed", "error", err)
		return nil, fmt.Errorf("longmem_extract: completion: %w", err)
	}
	if res == nil || strings.TrimSpace(res.Content) == "" {
		observability.RecordLongmemExtract("error")
		return nil, fmt.Errorf("longmem_extract: empty completion")
	}

	parsed, perr := parseSalientFactsJSON(res.Content)
	if perr != nil {
		observability.RecordLongmemExtract("error")
		logctx.From(ctx).Warn("longmem_extract: parse failed",
			"error", perr, "raw", firstNChars(res.Content, 240))
		return nil, fmt.Errorf("longmem_extract: parse: %w", perr)
	}

	out := make([]SalientFact, 0, len(parsed.Facts))
	for _, f := range parsed.Facts {
		if len(out) >= salientFactsMaxPerCall {
			break
		}
		content := strings.TrimSpace(f.Content)
		if content == "" {
			continue
		}
		if !prompts.ContainsSalientFactKind(f.Kind) {
			continue
		}
		if len([]rune(content)) > salientFactContentMaxLen {
			r := []rune(content)
			content = string(r[:salientFactContentMaxLen])
		}
		s := f.Salience
		if s < 0 {
			s = 0
		}
		if s > 1 {
			s = 1
		}
		out = append(out, SalientFact{
			Kind:     f.Kind,
			Content:  content,
			Salience: s,
		})
	}

	if len(out) == 0 {
		observability.RecordLongmemExtract("empty")
	} else {
		observability.RecordLongmemExtract("ok")
	}
	logctx.From(ctx).Info("rag.longmem.extract",
		"stage", "longmem_extract",
		"facts", len(out),
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return out, nil
}

func parseSalientFactsJSON(text string) (salientFactsResponse, error) {
	var out salientFactsResponse
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
	return salientFactsResponse{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}
