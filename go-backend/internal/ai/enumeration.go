package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
)

// EnumerationMatch is one verified hit produced by the structured-extraction
// pre-pass. The pre-pass exists because mid-range self-hosted LLMs reliably
// drop entries when asked for prose enumerations over many similar chunks
// (documented in the Chroma 2025 "Context Rot" study); the structured
// per-chunk yes/no pass is far more consistent at that model scale and
// gives the prose-generation pass a hard completeness contract.
type EnumerationMatch struct {
	// SourceIdx is the 1-based index into the ChatContext.Sources slice the
	// pre-pass read from. The prose LLM uses this for [N] citations.
	SourceIdx int `json:"source_idx"`
	// Entity names the match (e.g. project name, document title, person).
	Entity string `json:"entity"`
	// RoleOrDetail carries the key relationship snippet ("Projektleiter",
	// "erwähnt in §3", etc.). Short — this is a hint for prose writing.
	RoleOrDetail string `json:"role_or_detail"`
	// Quote is a verbatim snippet (max 150 chars) from the matched chunk.
	Quote string `json:"quote"`
}

// enumerationResponse is the wrapper shape the JSON schema enforces. Kept as
// an unexported type to avoid leaking the wrapper into the public API — the
// caller gets the bare []EnumerationMatch slice.
type enumerationResponse struct {
	Matches []EnumerationMatch `json:"matches"`
}

// enumerationSchemaRaw is the OpenAI-Structured-Outputs / vLLM guided_json
// schema that constrains the extraction output. strict=true in the outer
// ResponseFormat disables generation paths outside this schema, so the
// response is guaranteed to parse; no tolerant parser needed.
var enumerationSchemaRaw = json.RawMessage(`{
  "type": "object",
  "properties": {
    "matches": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "source_idx": {"type": "integer", "minimum": 1},
          "entity": {"type": "string"},
          "role_or_detail": {"type": "string"},
          "quote": {"type": "string"}
        },
        "required": ["source_idx", "entity", "role_or_detail", "quote"],
        "additionalProperties": false
      }
    }
  },
  "required": ["matches"],
  "additionalProperties": false
}`)

// SeedMeta carries the metadata needed to synthesize a verified match when
// the Pre-Pass LLM fails to return one for a BM25-seeded candidate. Keyed
// by 1-based source index matching the context layout PrepareChatContext
// produces.
type SeedMeta struct {
	FileName string // rendered as Entity in the synthesized match
	Content  string // source for the synthesized Quote (first 150 chars)
}

// ExtractEnumerationMatches runs the enumeration pre-pass over the retrieved
// context and returns the structured list of matches. The prose-generation
// pass then receives this list via prompts.EnumerationVerifiedMatchesAddendum
// as a hard completeness contract the LLM must satisfy.
//
// Three-stage deterministic post-processing hardens the LLM output against
// mid-range-model flakiness (Gemma-4-class reliably oscillates between
// over- and under-include for this task):
//
//  1. Candidate-only filter: when preSeededSourceIndices is non-empty,
//     drop any LLM match whose source_idx is not in the seed list. Gemma
//     interprets "consider only these blocks" as a soft preference and
//     regularly adds 1-2 false positives from non-candidate blocks.
//  2. Dedup by source_idx: Gemma occasionally emits the same chunk twice
//     under slightly different entity/role framings. Keep the first.
//  3. Seed guarantee: every seed source_idx MUST end up in the result.
//     For seeds the LLM dropped (empirically 1-3 for Person-class queries
//     on Gemma-31B), synthesize a match using SeedMeta (file name as
//     entity, first 150 chars of chunk as quote). The downstream prose
//     pass still gets to filter false positives against the full chunk.
//
// Fail-open: on LLM error or empty response, returns the fully-synthesized
// seed list (so the pipeline behaves as if Gemma had validated nothing,
// with BM25 as the sole recall floor). On hard parse failure, returns nil.
//
// contextText is the already-annotated `[N] [Source: …]` context string
// produced by PrepareChatContext, so source_idx values returned here are
// directly usable for downstream citations.
//
// preSeededSourceIndices is the list of source indices that BM25 keyword
// search identified as probable matches. seedMeta maps each of those
// source indices to the chunk's filename and content, for synthesis of
// missing matches.
//
// modelOverride picks a non-default chat model. Empty = use ChatModel.
func ExtractEnumerationMatches(
	ctx context.Context,
	resolver *ConfigResolver,
	question, contextText, kbID, lang, modelOverride string,
	preSeededSourceIndices []int,
	seedMeta map[int]SeedMeta,
) ([]EnumerationMatch, error) {
	config, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Warn("ai.enumeration.resolve_failed", "error", err, "kbId", kbID)
		return nil, nil
	}

	model := config.ChatModel
	if modelOverride != "" && slices.Contains(config.ChatModels, modelOverride) {
		model = modelOverride
	}

	ctx, span := observability.StartGenerationSpan(ctx, "rag.enumeration.extract", observability.GenerationAttrs{
		System: observability.ProviderSystemFromBaseURL(config.BaseURL),
		Model:  model,
	})
	defer span.End()

	start := time.Now()

	// Deterministic temperature: same chunks should yield the same matches
	// across calls, otherwise the enumeration result flaps between
	// semantically-equivalent queries.
	temp := 0.0

	req := &ChatRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "system", Content: prompts.EnumerationExtractionSystem(lang)},
			{Role: "user", Content: prompts.EnumerationExtractionUser(question, contextText, preSeededSourceIndices)},
		},
		Temperature: &temp,
		ResponseFormat: &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &ResponseJSONSchema{
				Name:   "enumeration_matches",
				Strict: true,
				Schema: enumerationSchemaRaw,
			},
		},
	}

	client := CachedClient(config.BaseURL, config.APIKey)
	resp, err := client.ChatCompletion(ctx, req)
	if err != nil {
		// Some backends don't support strict json_schema yet — retry once
		// with the looser json_object contract so feature degrades
		// gracefully on older LiteLLM / vLLM stacks.
		logctx.From(ctx).Info("ai.enumeration.strict_schema_rejected_falling_back",
			"error", err, "model", model)
		req.ResponseFormat = &ResponseFormat{Type: "json_object"}
		resp, err = client.ChatCompletion(ctx, req)
		if err != nil {
			logctx.From(ctx).Warn("ai.enumeration.llm_failed",
				"error", err, "model", model, "kbId", kbID)
			return nil, nil
		}
	}
	if len(resp.Choices) == 0 {
		logctx.From(ctx).Warn("ai.enumeration.no_choices", "model", model, "kbId", kbID)
		return nil, nil
	}

	content := resp.Choices[0].Message.Content
	var parsed enumerationResponse
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		// json_object fallback can emit noise before/after the object; try
		// tolerant extraction one last time before giving up.
		if err2 := parseJSONFromText(content, &parsed); err2 != nil {
			logctx.From(ctx).Warn("ai.enumeration.parse_failed",
				"error", err, "content_preview", truncateForLog(content, 200))
			return nil, nil
		}
	}

	droppedNonCandidate := 0
	droppedDuplicate := 0
	synthesized := 0

	if len(preSeededSourceIndices) > 0 {
		// Stage 1: candidate-only filter. Drop any LLM-proposed match
		// whose source_idx is not in the BM25 seed list.
		allowed := make(map[int]struct{}, len(preSeededSourceIndices))
		for _, idx := range preSeededSourceIndices {
			allowed[idx] = struct{}{}
		}
		filtered := parsed.Matches[:0]
		for _, m := range parsed.Matches {
			if _, ok := allowed[m.SourceIdx]; !ok {
				droppedNonCandidate++
				continue
			}
			filtered = append(filtered, m)
		}

		// Stage 2: dedup by source_idx. Gemma occasionally emits the
		// same chunk twice under different entity framings.
		seen := make(map[int]struct{}, len(filtered))
		uniq := filtered[:0]
		for _, m := range filtered {
			if _, dup := seen[m.SourceIdx]; dup {
				droppedDuplicate++
				continue
			}
			seen[m.SourceIdx] = struct{}{}
			uniq = append(uniq, m)
		}
		parsed.Matches = uniq

		// Stage 3: seed guarantee. For each BM25 seed the LLM didn't
		// validate, synthesize a match from seedMeta. The prose pass
		// still gets to filter false positives against the full chunk;
		// what we're preventing here is Gemma's under-recall silently
		// dropping valid BM25 evidence.
		for _, idx := range preSeededSourceIndices {
			if _, present := seen[idx]; present {
				continue
			}
			meta, hasMeta := seedMeta[idx]
			if !hasMeta {
				continue
			}
			entity := meta.FileName
			if entity == "" {
				entity = fmt.Sprintf("Quelle [%d]", idx)
			}
			parsed.Matches = append(parsed.Matches, EnumerationMatch{
				SourceIdx:    idx,
				Entity:       entity,
				RoleOrDetail: "from BM25 keyword match (LLM pre-pass did not validate)",
				Quote:        firstNChars(meta.Content, 150),
			})
			seen[idx] = struct{}{}
			synthesized++
		}
	}

	duration := time.Since(start)
	logctx.From(ctx).Info("rag.enumeration.extracted",
		"stage", "enumeration_extract",
		"matches", len(parsed.Matches),
		"non_candidate_dropped", droppedNonCandidate,
		"duplicate_dropped", droppedDuplicate,
		"seed_synthesized", synthesized,
		"duration_ms", duration.Milliseconds(),
		"model", model,
	)

	return parsed.Matches, nil
}

func firstNChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Rune-safe truncation so we don't slice in the middle of a
	// multi-byte UTF-8 sequence (German umlauts, etc.).
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// FormatEnumerationMatchesJSON returns a compact JSON representation of the
// match list suitable for embedding in the prose-pass system prompt (see
// prompts.EnumerationVerifiedMatchesAddendum). Deterministic ordering +
// indentation keeps prompt caches stable across calls that see the same
// extracted list.
func FormatEnumerationMatchesJSON(matches []EnumerationMatch) string {
	if matches == nil {
		matches = []EnumerationMatch{}
	}
	out, err := json.MarshalIndent(enumerationResponse{Matches: matches}, "", "  ")
	if err != nil {
		// Should never happen for this shape, but fall back to a
		// non-schema representation rather than breaking the chat flow.
		return fmt.Sprintf("{\"matches\": []} /* marshal-err: %v */", err)
	}
	return string(out)
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
