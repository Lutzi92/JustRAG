package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/justrag/go-backend/internal/observability"
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
		if f, warnings, err := j.faithfulness(ctx, q, answer, contextText); err != nil {
			out.JudgeErrors = append(out.JudgeErrors, fmt.Sprintf("faithfulness: %v", err))
		} else {
			out.Faithfulness = &f
			out.JudgeWarnings = append(out.JudgeWarnings, warnings...)
		}
	}

	if r, warnings, err := j.answerRelevance(ctx, q, answer); err != nil {
		out.JudgeErrors = append(out.JudgeErrors, fmt.Sprintf("answer_relevance: %v", err))
	} else {
		out.AnswerRelevance = &r
		out.JudgeWarnings = append(out.JudgeWarnings, warnings...)
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

func (j *Judge) faithfulness(ctx context.Context, q Question, answer, contextText string) (float64, []string, error) {
	sys := prompts.FaithfulnessSystemPrompt(q.Language)
	user := prompts.FaithfulnessUserPrompt(q.Question, answer, contextText)
	var parsed struct {
		Claims []struct {
			Text      string `json:"text"`
			Supported bool   `json:"supported"`
		} `json:"claims"`
	}
	retried, err := j.completeJSON(ctx, "faithfulness", user, sys, &parsed)
	if err != nil {
		return 0, nil, err
	}
	var warnings []string
	if retried {
		warnings = append(warnings, "retry:faithfulness")
	}
	if len(parsed.Claims) == 0 {
		return 1.0, warnings, nil
	}
	supported := 0
	for _, c := range parsed.Claims {
		if c.Supported {
			supported++
		}
	}
	return float64(supported) / float64(len(parsed.Claims)), warnings, nil
}

func (j *Judge) answerRelevance(ctx context.Context, q Question, answer string) (float64, []string, error) {
	sys := prompts.AnswerRelevanceSystemPrompt(q.Language)
	user := prompts.AnswerRelevanceUserPrompt(q.Question, answer)
	var parsed struct {
		Score     json.RawMessage `json:"score"`
		Reasoning string          `json:"reasoning"`
	}
	retried, err := j.completeJSON(ctx, "answer_relevance", user, sys, &parsed)
	if err != nil {
		return 0, nil, err
	}
	score, err := parseJudgeScore(parsed.Score)
	if err != nil {
		return 0, nil, fmt.Errorf("score: %w", err)
	}
	var warnings []string
	if retried {
		warnings = append(warnings, "retry:answer_relevance")
	}
	return float64(score-1) / 4.0, warnings, nil
}

// parseJudgeScore parses a judge-emitted "score" field that may arrive as a
// JSON number (5, 4.0) or, tolerated because some models emit it that way,
// a numeric JSON string ("5", "4.0"). The result is rounded to the nearest
// integer and clamped to the 1..5 Likert range. An error is returned when
// raw is missing, JSON null, or neither a number nor a numeric string.
//
// The null case needs its own guard: `json.Unmarshal("null", &float64)`
// SUCCEEDS and leaves the zero value untouched, so a judge that answered
// {"score":null} — "I have no verdict" — used to be scored 0 and clamped up
// to 1, i.e. silently recorded as a real "barely relevant" rating that
// dragged the mean down. A missing verdict must drop the sample instead
// (the caller turns the error into a judge_errors entry and leaves
// AnswerRelevance nil, which W4-R2's per-metric `n` then makes visible).
func parseJudgeScore(raw json.RawMessage) (int, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, fmt.Errorf("missing (%q)", trimmed)
	}
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
	var parsed struct {
		Relevant []bool `json:"relevant"`
	}
	retried, err := j.completeJSON(ctx, "context_precision", user, sys, &parsed)
	if err != nil {
		return 0, nil, err
	}
	relevant, warnings := alignBooleans("context_precision", parsed.Relevant, len(contents))
	if retried {
		warnings = append(warnings, "retry:context_precision")
	}
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
// contextPrecision. Evaluate is the primary path and skips this method when
// q.ExpectedPoints is empty; the guard below makes a direct call safe too —
// covered/len(points) would otherwise be 0/0 = NaN, and returning an error
// rather than a 0.0 keeps such a misuse out of mean_coverage instead of
// recording "the answer covered nothing".
func (j *Judge) coverage(ctx context.Context, q Question, answer string) (float64, []string, error) {
	points := q.ExpectedPoints
	if len(points) == 0 {
		return 0, nil, fmt.Errorf("no expected points for question %q", q.ID)
	}
	sys := prompts.CoverageSystemPrompt(q.Language)
	user := prompts.CoverageUserPrompt(q.Question, answer, points)
	var parsed struct {
		Covered []bool `json:"covered"`
	}
	retried, err := j.completeJSON(ctx, "coverage", user, sys, &parsed)
	if err != nil {
		return 0, nil, err
	}
	covered, warnings := alignBooleans("coverage", parsed.Covered, len(points))
	if retried {
		warnings = append(warnings, "retry:coverage")
	}
	coveredCount := 0
	for _, c := range covered {
		if c {
			coveredCount++
		}
	}
	return float64(coveredCount) / float64(len(points)), warnings, nil
}

// completeJSON asks the completer once, and on a JSON decoder failure
// re-asks exactly once more with the decoder's own diagnostic appended
// (W6-R4 / W6-R17): the Wave-5 fix wave established that live judge parse
// failures are brace-balanced objects the decoder still rejects (a raw
// newline or unescaped quote inside a string, or a trailing comma) — not
// truncation and not a code fence, both of which unmarshalStrict already
// tolerates without any retry. The retry is unconditional (no site_config
// gate) and bounded to exactly one extra call regardless of outcome; the
// parser itself (unmarshalStrict) stays strict — this wraps it, it does not
// loosen it. judge identifies the caller for the rag_judge_retry_total
// metric and the "retry:<judge>" warning the caller appends.
func (j *Judge) completeJSON(ctx context.Context, judge, user, sys string, v any) (retried bool, err error) {
	resp, err := j.completer.Complete(ctx, user, sys)
	if err != nil {
		return false, err
	}
	firstErr := unmarshalStrict(resp, v)
	if firstErr == nil {
		return false, nil
	}
	observability.RecordJudgeRetry(judge)
	retryPrompt := user + "\n\nYour previous reply was not valid JSON: " + firstErr.Error() +
		"\nReply with only the corrected JSON object — escape newlines inside strings, no trailing commas."
	resp2, err := j.completer.Complete(ctx, retryPrompt, sys)
	if err != nil {
		return true, fmt.Errorf("%w (retry call failed: %v)", firstErr, err)
	}
	if err := unmarshalStrict(resp2, v); err != nil {
		return true, fmt.Errorf("%v (after retry: %v)", firstErr, err)
	}
	return true, nil
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

// unmarshalStrict tries JSON; if that fails, extracts a JSON object from
// within a larger string (LLMs wrap JSON in prose, and in ```json fences).
//
// The extraction scans BALANCED objects rather than taking the span from the
// first '{' to the last '}', because that span is wrong in the two shapes
// that actually failed live (fix wave, finding F4 — the parked diagnosis
// "the judge used a code fence" is not the cause: a fenced but complete
// object parses under either strategy):
//
//   - Two objects in one reply. The span swallows both plus whatever sits
//     between them, which is never valid JSON, so a perfectly good first
//     verdict was thrown away. Now the first object that unmarshals wins.
//   - A reply cut off before the object closes (a completion-token limit on
//     a long German "reasoning" string, or a long faithfulness claim list).
//     The span then ends at some earlier INNER '}' — or there is no '}' at
//     all — and the error read "response is not valid JSON", which is true
//     but useless. An unterminated object is now reported as "truncated
//     JSON" so the next occurrence is diagnosable from the report alone.
//
// Tolerance is otherwise unchanged and deliberately conservative: this
// parser also runs in production for the RAGAS background sampler
// (internal/worker/ragas_sample.go) and the in-app/scheduled eval runner, so
// garbage must still error rather than be coerced into a score.
func unmarshalStrict(text string, v any) error {
	if err := json.Unmarshal([]byte(text), v); err == nil {
		return nil
	}
	objects, unterminated := scanJSONObjects(text)
	var lastErr error
	for _, obj := range objects {
		err := json.Unmarshal([]byte(obj), v)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	if unterminated {
		return fmt.Errorf("truncated JSON: the response opens an object that never closes (%d bytes — a completion-token limit on the judge model is the usual cause): %q", len(text), previewJSON(text))
	}
	if lastErr != nil {
		// A BALANCED object that json still rejects — neither truncation nor
		// a code fence, which is what the three Wave-5 Task-10 failures were
		// (t10-pw-2 G06, t10-mr1 G05, t10-mr2 G09). The 120-byte preview the
		// error used to carry could not distinguish the candidate shapes (a
		// raw newline inside a string, an unescaped quote inside a string, a
		// trailing comma), because the offending byte sits past it and the
		// underlying decoder error — which names both the character and its
		// offset — was dropped. Carry it.
		return fmt.Errorf("invalid JSON inside a complete object (%d bytes; %v — neither truncation nor a code fence): %q", len(text), lastErr, previewJSON(text))
	}
	return fmt.Errorf("response is not valid JSON: no JSON object in %d bytes: %q", len(text), previewJSON(text))
}

// judgePreviewRunes bounds the response excerpt an error carries. It is wide
// enough to reach the offending byte in a realistic judge reply (a German
// "reasoning" string runs a few hundred runes) and still short enough to sit
// in a report's judge_errors array.
const judgePreviewRunes = 400

// previewJSON is firstN in RUNES: a byte cut through a German judge response
// splits a multi-byte rune and renders as an escape in the %q'd error,
// exactly where the reader is trying to see the offending character.
func previewJSON(s string) string {
	r := []rune(s)
	if len(r) <= judgePreviewRunes {
		return s
	}
	return string(r[:judgePreviewRunes]) + "..."
}

// scanJSONObjects returns every top-level {...} span in text, in order, plus
// a flag reporting that the last '{' was never closed. It tracks string
// literals and their escapes so a brace inside a string value (common in
// judge reasoning that quotes JSON) neither opens nor closes an object.
func scanJSONObjects(text string) (objects []string, unterminated bool) {
	depth, start := 0, -1
	inString, escaped := false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					objects = append(objects, text[start:i+1])
					start = -1
				}
			}
		}
	}
	return objects, depth > 0
}
