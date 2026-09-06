package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/justrag/go-backend/internal/prompts"
)

// Judge runs LLM-as-judge evaluations. Construct with NewJudge; the three
// core metrics (faithfulness, answer relevance, context precision) are
// attempted per Evaluate call, plus a fourth "coverage" metric (W4-R5) when
// the question carries ExpectedPoints. Per-metric failures are captured in
// JudgeErrors.
type Judge struct {
	completer Completer
}

// NewJudge returns a Judge backed by completer.
func NewJudge(completer Completer) *Judge {
	return &Judge{completer: completer}
}

// Evaluate runs the judge prompts and assembles a JudgeMetrics. When
// chunks/contents is empty, context precision is skipped. When
// q.ExpectedPoints is empty, coverage is skipped (Coverage stays nil and
// the completer is not called for it).
func (j *Judge) Evaluate(ctx context.Context, q Question, answer string, chunks []RetrievedChunk, contents []string) JudgeMetrics {
	out := JudgeMetrics{Answer: answer}

	if len(chunks) != len(contents) {
		out.JudgeErrors = append(out.JudgeErrors, fmt.Sprintf("faithfulness: chunk/content length mismatch: %d chunks vs %d contents", len(chunks), len(contents)))
	} else {
		contextText := assembleContextText(chunks, contents)
		if f, err := j.faithfulness(ctx, q, answer, contextText); err != nil {
			out.JudgeErrors = append(out.JudgeErrors, fmt.Sprintf("faithfulness: %v", err))
		} else {
			out.Faithfulness = &f
		}
	}

	if r, err := j.answerRelevance(ctx, q, answer); err != nil {
		out.JudgeErrors = append(out.JudgeErrors, fmt.Sprintf("answer_relevance: %v", err))
	} else {
		out.AnswerRelevance = &r
	}

	if len(contents) == 0 {
		// skip context precision
	} else if p, warnings, err := j.contextPrecision(ctx, q, contents); err != nil {
		out.JudgeErrors = append(out.JudgeErrors, fmt.Sprintf("context_precision: %v", err))
	} else {
		out.ContextPrecision = &p
		out.JudgeWarnings = append(out.JudgeWarnings, warnings...)
	}

	if len(q.ExpectedPoints) == 0 {
		// skip coverage — no ground-truth points authored for this row (W4-R5)
	} else if c, warnings, err := j.coverage(ctx, q, answer); err != nil {
		out.JudgeErrors = append(out.JudgeErrors, fmt.Sprintf("coverage: %v", err))
	} else {
		out.Coverage = &c
		out.JudgeWarnings = append(out.JudgeWarnings, warnings...)
	}

	return out
}

func (j *Judge) faithfulness(ctx context.Context, q Question, answer, contextText string) (float64, error) {
	sys := prompts.FaithfulnessSystemPrompt(q.Language)
	user := prompts.FaithfulnessUserPrompt(q.Question, answer, contextText)
	resp, err := j.completer.Complete(ctx, user, sys)
	if err != nil {
		return 0, err
	}
	var parsed struct {
		Claims []struct {
			Text      string `json:"text"`
			Supported bool   `json:"supported"`
		} `json:"claims"`
	}
	if err := unmarshalStrict(resp, &parsed); err != nil {
		return 0, err
	}
	if len(parsed.Claims) == 0 {
		return 1.0, nil
	}
	supported := 0
	for _, c := range parsed.Claims {
		if c.Supported {
			supported++
		}
	}
	return float64(supported) / float64(len(parsed.Claims)), nil
}

func (j *Judge) answerRelevance(ctx context.Context, q Question, answer string) (float64, error) {
	sys := prompts.AnswerRelevanceSystemPrompt(q.Language)
	user := prompts.AnswerRelevanceUserPrompt(q.Question, answer)
	resp, err := j.completer.Complete(ctx, user, sys)
	if err != nil {
		return 0, err
	}
	var parsed struct {
		Score     json.RawMessage `json:"score"`
		Reasoning string          `json:"reasoning"`
	}
	if err := unmarshalStrict(resp, &parsed); err != nil {
		return 0, err
	}
	score, err := parseJudgeScore(parsed.Score)
	if err != nil {
		return 0, fmt.Errorf("score: %w", err)
	}
	return float64(score-1) / 4.0, nil
}

// parseJudgeScore parses a judge-emitted "score" field that may arrive as a
// JSON number (5, 4.0) or, tolerated because some models emit it that way,
// a numeric JSON string ("5", "4.0"). The result is rounded to the nearest
// integer and clamped to the 1..5 Likert range. An error is returned only
// when raw is neither a number nor a numeric string.
func parseJudgeScore(raw json.RawMessage) (int, error) {
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return clampScore(f), nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return clampScore(f), nil
		}
	}
	return 0, fmt.Errorf("not a number or numeric string: %q", string(raw))
}

func clampScore(f float64) int {
	r := int(math.Round(f))
	if r < 1 {
		return 1
	}
	if r > 5 {
		return 5
	}
	return r
}

// contextPrecision returns the precision score plus any tolerance warnings
// (non-fatal — recorded in JudgeMetrics.JudgeWarnings by the caller). An
// error is returned only when the judge's response is not valid JSON at
// all; a boolean-count mismatch is tolerated by truncating/padding rather
// than dropping the sample.
func (j *Judge) contextPrecision(ctx context.Context, q Question, contents []string) (float64, []string, error) {
	sys := prompts.ContextPrecisionSystemPrompt(q.Language)
	user := prompts.ContextPrecisionUserPrompt(q.Question, contents)
	resp, err := j.completer.Complete(ctx, user, sys)
	if err != nil {
		return 0, nil, err
	}
	var parsed struct {
		Relevant []bool `json:"relevant"`
	}
	if err := unmarshalStrict(resp, &parsed); err != nil {
		return 0, nil, err
	}
	relevant, warnings := alignBooleans("context_precision", parsed.Relevant, len(contents))
	relevantCount := 0
	for _, r := range relevant {
		if r {
			relevantCount++
		}
	}
	return float64(relevantCount) / float64(len(contents)), warnings, nil
}

// coverage returns the W4-R5 coverage score (covered points / total points)
// plus any tolerance warnings, mirroring contextPrecision's shape. An error
// is returned only when the judge's response is not valid JSON at all; a
// boolean-count mismatch is tolerated via alignBooleans, same as
// contextPrecision. Callers must not invoke this when q.ExpectedPoints is
// empty — Evaluate guards on that.
func (j *Judge) coverage(ctx context.Context, q Question, answer string) (float64, []string, error) {
	points := q.ExpectedPoints
	sys := prompts.CoverageSystemPrompt(q.Language)
	user := prompts.CoverageUserPrompt(q.Question, answer, points)
	resp, err := j.completer.Complete(ctx, user, sys)
	if err != nil {
		return 0, nil, err
	}
	var parsed struct {
		Covered []bool `json:"covered"`
	}
	if err := unmarshalStrict(resp, &parsed); err != nil {
		return 0, nil, err
	}
	covered, warnings := alignBooleans("coverage", parsed.Covered, len(points))
	coveredCount := 0
	for _, c := range covered {
		if c {
			coveredCount++
		}
	}
	return float64(coveredCount) / float64(len(points)), warnings, nil
}

// alignBooleans truncates or pads a judge-returned boolean slice to exactly
// n elements (the expected count — chunks for context_precision, points for
// coverage), returning a one-element warning describing the mismatch when
// the counts differ. label identifies the caller in the warning message.
// Shared by contextPrecision and coverage so the tolerance policy (W4-R1)
// cannot drift between the two judges.
func alignBooleans(label string, got []bool, n int) ([]bool, []string) {
	if len(got) == n {
		return got, nil
	}
	warning := fmt.Sprintf("%s: judge returned %d booleans, expected %d — truncated/padded", label, len(got), n)
	if len(got) > n {
		return got[:n], []string{warning}
	}
	padded := make([]bool, n)
	copy(padded, got)
	return padded, []string{warning}
}

func assembleContextText(chunks []RetrievedChunk, contents []string) string {
	if len(chunks) != len(contents) {
		return ""
	}
	var out string
	for i := range chunks {
		out += fmt.Sprintf("[%d] %s\n\n", i+1, contents[i])
	}
	return out
}

// unmarshalStrict tries JSON; if that fails, attempts to extract a JSON
// object from within a larger string (LLMs sometimes wrap JSON in prose).
func unmarshalStrict(text string, v any) error {
	if err := json.Unmarshal([]byte(text), v); err == nil {
		return nil
	}
	start, end := -1, -1
	for i := 0; i < len(text); i++ {
		if text[i] == '{' {
			start = i
			break
		}
	}
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == '}' {
			end = i + 1
			break
		}
	}
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(text[start:end]), v); err == nil {
			return nil
		}
	}
	return fmt.Errorf("response is not valid JSON: %q", firstN(text, 120))
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
