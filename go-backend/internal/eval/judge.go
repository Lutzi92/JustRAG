package eval

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/justrag/go-backend/internal/prompts"
)

// Judge runs LLM-as-judge evaluations. Construct with NewJudge; all three
// metrics (faithfulness, answer relevance, context precision) are attempted
// per Evaluate call. Per-metric failures are captured in JudgeErrors.
type Judge struct {
	completer Completer
}

// NewJudge returns a Judge backed by completer.
func NewJudge(completer Completer) *Judge {
	return &Judge{completer: completer}
}

// Evaluate runs all three judge prompts and assembles a JudgeMetrics.
// When chunks/contents is empty, context precision is skipped.
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
	} else if p, err := j.contextPrecision(ctx, q, contents); err != nil {
		out.JudgeErrors = append(out.JudgeErrors, fmt.Sprintf("context_precision: %v", err))
	} else {
		out.ContextPrecision = &p
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
		Score     int    `json:"score"`
		Reasoning string `json:"reasoning"`
	}
	if err := unmarshalStrict(resp, &parsed); err != nil {
		return 0, err
	}
	if parsed.Score < 1 || parsed.Score > 5 {
		return 0, fmt.Errorf("score out of range: %d", parsed.Score)
	}
	return float64(parsed.Score-1) / 4.0, nil
}

func (j *Judge) contextPrecision(ctx context.Context, q Question, contents []string) (float64, error) {
	sys := prompts.ContextPrecisionSystemPrompt(q.Language)
	user := prompts.ContextPrecisionUserPrompt(q.Question, contents)
	resp, err := j.completer.Complete(ctx, user, sys)
	if err != nil {
		return 0, err
	}
	var parsed struct {
		Relevant []bool `json:"relevant"`
	}
	if err := unmarshalStrict(resp, &parsed); err != nil {
		return 0, err
	}
	if len(parsed.Relevant) != len(contents) {
		return 0, fmt.Errorf("judge returned %d booleans, expected %d", len(parsed.Relevant), len(contents))
	}
	relevantCount := 0
	for _, r := range parsed.Relevant {
		if r {
			relevantCount++
		}
	}
	return float64(relevantCount) / float64(len(contents)), nil
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
