package ai

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/prompts"
)

// Grade is the per-chunk relevance verdict produced by GradeRelevance.
type Grade string

const (
	// GradeRelevant — chunk directly answers the query.
	GradeRelevant Grade = "relevant"
	// GradeAmbiguous — chunk discusses the same topic but doesn't directly answer.
	// Used as the fail-open default whenever parsing or the LLM call falters,
	// so a degraded grader never silently drops chunks downstream.
	GradeAmbiguous Grade = "ambiguous"
	// GradeIrrelevant — chunk is on a different topic.
	GradeIrrelevant Grade = "irrelevant"
)

// GradeRelevance asks a small LLM to grade each chunk's relevance to query.
// Used by Corrective RAG (CRAG) to decide whether to proceed with the
// retrieved context, rewrite the query and retry, or abstain.
//
// Returns one Grade per input chunk, in the same order. Length always equals
// len(contents): if the LLM returns fewer entries the tail is padded with
// GradeAmbiguous; extras are ignored.
//
// modelOverride picks a non-default chat model — typically a small/fast one
// (e.g. gpt-4o-mini, gemma-3-4b) since the grader does not need depth, only
// consistency. Empty string falls back to the resolver's ChatModel.
//
// Fail-open semantics: any error during the LLM call yields a slice of
// GradeAmbiguous of the right length and a nil error. The CRAG pipeline
// must never block answers on a flaky grader.
func GradeRelevance(ctx context.Context, resolver *ConfigResolver, query string, contents []string, kbID, modelOverride string) ([]Grade, error) {
	if len(contents) == 0 {
		return nil, nil
	}

	userPrompt := prompts.RelevanceGraderUser(query, contents)
	// Deterministic temperature: same chunks should always get the same
	// verdict, otherwise CRAG decisions would flap between identical calls.
	result, err := GenerateCompletionWithModelDeterministic(ctx, resolver, userPrompt, prompts.RelevanceGraderSystem(), kbID, false, modelOverride)
	if err != nil {
		logctx.From(ctx).Warn("ai.grader.llm_failed",
			"error", err,
			"chunks", len(contents),
			"kbId", kbID,
			"fallback", "failOpen")
		return failOpen(len(contents)), nil
	}

	grades := parseGraderResponse(result.Content, len(contents))
	return grades, nil
}

// parseGraderResponse extracts the grades array from the LLM response. The
// expected shape is `{"grades": [...]}`, but we tolerate:
//   - leading/trailing whitespace and ```json fences
//   - a bare array (no enclosing object)
//   - mixed-case grade strings
//   - extra fields in the object
//   - length mismatch (pad with GradeAmbiguous, drop overflow)
//
// On a hard parse failure the entire batch is graded GradeAmbiguous so the
// CRAG pipeline keeps moving.
func parseGraderResponse(text string, expected int) []Grade {
	text = stripJSONFences(strings.TrimSpace(text))

	type wrapper struct {
		Grades []string `json:"grades"`
	}

	var raw []string
	var w wrapper
	if err := json.Unmarshal([]byte(text), &w); err == nil && w.Grades != nil {
		raw = w.Grades
	} else if err := json.Unmarshal([]byte(text), &raw); err != nil {
		// Try to locate the grades array inside surrounding noise.
		if start := strings.Index(text, `"grades"`); start >= 0 {
			if open := strings.Index(text[start:], "["); open >= 0 {
				if close := strings.Index(text[start+open:], "]"); close >= 0 {
					snippet := text[start+open : start+open+close+1]
					_ = json.Unmarshal([]byte(snippet), &raw)
				}
			}
		}
	}

	if raw == nil {
		return failOpen(expected)
	}

	out := make([]Grade, expected)
	for i := range out {
		if i < len(raw) {
			out[i] = normalizeGrade(raw[i])
		} else {
			out[i] = GradeAmbiguous
		}
	}
	return out
}

// normalizeGrade maps free-form grader output to one of the three Grade
// constants. Anything unexpected becomes GradeAmbiguous so a typo or
// hallucinated label never drops a chunk.
func normalizeGrade(s string) Grade {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(GradeRelevant):
		return GradeRelevant
	case string(GradeIrrelevant):
		return GradeIrrelevant
	default:
		return GradeAmbiguous
	}
}

// stripJSONFences removes a leading ```json (or plain ```) fence and a
// trailing ``` if present. Models routinely wrap structured output in
// markdown fences even when explicitly told not to.
func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "```"); ok {
		// Drop the opening fence line entirely (could be ```json, ```JSON, ```).
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			s = rest[nl+1:]
		} else {
			s = rest
		}
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// failOpen returns a slice of length n filled with GradeAmbiguous — the
// neutral verdict the rest of the CRAG pipeline treats as "neither helpful
// nor disqualifying".
func failOpen(n int) []Grade {
	out := make([]Grade, n)
	for i := range out {
		out[i] = GradeAmbiguous
	}
	return out
}

// CountByGrade tallies a slice of Grades into the three buckets. Useful for
// CRAG decision logic and metrics emission.
func CountByGrade(grades []Grade) (relevant, ambiguous, irrelevant int) {
	for _, g := range grades {
		switch g {
		case GradeRelevant:
			relevant++
		case GradeIrrelevant:
			irrelevant++
		default:
			ambiguous++
		}
	}
	return
}
