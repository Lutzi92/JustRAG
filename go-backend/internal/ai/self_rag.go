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

// ChunkRelevance is one entry in the AP-D2 ISREL verdict slice.
// Verdict mirrors the closed enum in the prompt: relevant /
// partially_relevant / irrelevant. The integer N matches the
// 1-based source position the chat handler shows the LLM.
type ChunkRelevance struct {
	N       int    `json:"n"`
	Verdict string `json:"verdict"`
}

// UsefulnessVerdict is the AP-D2 ISUSE result. yes / partial / no
// match the prompt's closed enum; reason is a one-sentence
// justification surfaced to the admin panel and (when verdict
// is no) used by the refine gate to compose its rewrite prompt.
type UsefulnessVerdict struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// SelfRAGResult is the unified output of one SelfRAG call. ISSUP
// reuses FlaggedClaim so the existing AP-A1 refine path
// (claimsTriggeringRefine, SelectRefineMode, RefineFlaggedClaims)
// works unchanged — Self-RAG is a drop-in replacement for the
// legacy factuality verifier with two extra fields.
type SelfRAGResult struct {
	Relevance     []ChunkRelevance  `json:"isrel"`
	FlaggedClaims []FlaggedClaim    `json:"issup"`
	Usefulness    UsefulnessVerdict `json:"isuse"`
}

// selfRAGSpec is the Structured-Outputs contract for VerifySelfRAG,
// matching the prompt's three-verdict shape. All three enums mirror
// the prompt's closed vocabularies; normaliseSelfRAGResult still
// coerces drift on the json_object fallback path.
var selfRAGSpec = &StructuredSpec{
	Name: "self_rag_verdicts",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"isrel": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"n": {"type": "integer"},
						"verdict": {"type": "string", "enum": ["relevant", "partially_relevant", "irrelevant"]}
					},
					"required": ["n", "verdict"],
					"additionalProperties": false
				}
			},
			"issup": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"claim_text": {"type": "string"},
						"reason": {"type": "string", "enum": ["unsupported", "contradicted", "out_of_scope"]},
						"cited_n": {"type": "integer"}
					},
					"required": ["claim_text", "reason", "cited_n"],
					"additionalProperties": false
				}
			},
			"isuse": {
				"type": "object",
				"properties": {
					"verdict": {"type": "string", "enum": ["yes", "partial", "no"]},
					"reason": {"type": "string"}
				},
				"required": ["verdict", "reason"],
				"additionalProperties": false
			}
		},
		"required": ["isrel", "issup", "isuse"],
		"additionalProperties": false
	}`),
}

// VerifySelfRAG runs the AP-D2 unified verifier. Replaces the
// legacy VerifyFactuality call when chat_self_rag_enabled is on
// — feeds the same FlaggedClaim slice into the refine gate,
// plus surfaces ISREL + ISUSE for diagnostic + ISUSE-driven
// holistic refine.
//
// On any LLM-side failure (call error, parse error) returns a
// zero result + non-nil error. The chat handler logs and proceeds
// without verification — Self-RAG is post-response, never blocks
// the user-visible answer.
//
// modelOverride empty → resolver default (chat_self_rag_model).
// Same model class as the factuality verifier — Gemma/Qwen3 4B
// works well enough for structured-output verification.
func VerifySelfRAG(ctx context.Context, resolver *ConfigResolver, question, answer, sourcesBlock, kbID, lang, modelOverride string) (SelfRAGResult, error) {
	start := time.Now()
	defer func() {
		observability.ObserveSelfRAGSeconds(time.Since(start).Seconds())
	}()

	sys := prompts.SelfRAGSystemPrompt(lang)
	user := prompts.SelfRAGUserPrompt(question, answer, sourcesBlock)

	res, err := resolver.structuredCompletionFn(ctx, user, sys, kbID, modelOverride, selfRAGSpec)
	if err != nil {
		observability.RecordSelfRAG("error")
		logctx.From(ctx).Warn("self_rag: completion failed", "error", err)
		return SelfRAGResult{}, fmt.Errorf("self_rag: completion: %w", err)
	}
	if res == nil || strings.TrimSpace(res.Content) == "" {
		observability.RecordSelfRAG("error")
		return SelfRAGResult{}, fmt.Errorf("self_rag: empty completion")
	}

	parsed, perr := parseSelfRAGJSON(res.Content)
	if perr != nil {
		observability.RecordSelfRAG("error")
		logctx.From(ctx).Warn("self_rag: parse failed",
			"error", perr, "raw", firstNChars(res.Content, 240))
		return SelfRAGResult{}, fmt.Errorf("self_rag: parse: %w", perr)
	}

	out := normaliseSelfRAGResult(parsed)

	switch {
	case out.Usefulness.Verdict == "no":
		observability.RecordSelfRAG("flagged_isuse_no")
	case len(out.FlaggedClaims) > 0:
		observability.RecordSelfRAG("flagged_issup")
	case anyIrrelevantOrPartial(out.Relevance):
		observability.RecordSelfRAG("flagged_isrel")
	default:
		observability.RecordSelfRAG("clean")
	}

	logctx.From(ctx).Info("rag.self_rag",
		"stage", "self_rag",
		"isuse", out.Usefulness.Verdict,
		"issup_flagged", len(out.FlaggedClaims),
		"isrel_total", len(out.Relevance),
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return out, nil
}

// normaliseSelfRAGResult enforces the closed-enum contract on the
// LLM's output. Drift (e.g. "very_relevant", "supported") gets
// coerced to the nearest valid value rather than dropped, because
// dropping silently warps the panel's count distribution. Coercion
// is logged via the per-stage metric labels.
func normaliseSelfRAGResult(r SelfRAGResult) SelfRAGResult {
	out := SelfRAGResult{
		Relevance:     make([]ChunkRelevance, 0, len(r.Relevance)),
		FlaggedClaims: make([]FlaggedClaim, 0, len(r.FlaggedClaims)),
		Usefulness:    r.Usefulness,
	}

	for _, c := range r.Relevance {
		if c.N <= 0 {
			continue
		}
		v := strings.ToLower(strings.TrimSpace(c.Verdict))
		switch v {
		case "relevant", "partially_relevant", "irrelevant":
		default:
			v = "irrelevant"
		}
		out.Relevance = append(out.Relevance, ChunkRelevance{N: c.N, Verdict: v})
	}

	for _, c := range r.FlaggedClaims {
		text := strings.TrimSpace(c.ClaimText)
		if text == "" {
			continue
		}
		reason := strings.ToLower(strings.TrimSpace(c.Reason))
		switch reason {
		case "unsupported", "contradicted", "out_of_scope":
		default:
			reason = "unsupported"
		}
		out.FlaggedClaims = append(out.FlaggedClaims, FlaggedClaim{
			ClaimText: text,
			Reason:    reason,
			CitedN:    c.CitedN,
		})
	}

	verdict := strings.ToLower(strings.TrimSpace(out.Usefulness.Verdict))
	switch verdict {
	case "yes", "partial", "no":
	default:
		// Empty / unrecognised: treat as "yes" to avoid a
		// false-positive ISUSE-driven refine. The Operator can
		// chase real ISUSE failures via the metric label.
		verdict = "yes"
	}
	out.Usefulness.Verdict = verdict
	out.Usefulness.Reason = strings.TrimSpace(out.Usefulness.Reason)
	return out
}

func anyIrrelevantOrPartial(rel []ChunkRelevance) bool {
	for _, r := range rel {
		if r.Verdict != "relevant" {
			return true
		}
	}
	return false
}

func parseSelfRAGJSON(text string) (SelfRAGResult, error) {
	var out SelfRAGResult
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
	return SelfRAGResult{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}
