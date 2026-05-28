package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
)

type FactCheckResult struct {
	Verified bool     `json:"verified"`
	Score    float64  `json:"score"`
	Issues   []string `json:"issues"`
}

// CheckFacts verifies an AI answer against source context via a separate LLM call.
// Fails open: returns a default "unverified" result on error rather than propagating.
func CheckFacts(ctx context.Context, resolver *ConfigResolver, question, answer, contextText, kbID, lang string) *FactCheckResult {
	cfg, _ := resolver.Resolve(ctx, kbID) // best-effort; nil-safe below
	modelName := ""
	baseURL := ""
	if cfg != nil {
		modelName = cfg.ChatModel
		baseURL = cfg.BaseURL
	}
	ctx, span := observability.StartGenerationSpan(ctx, "rag.factcheck", observability.GenerationAttrs{
		System: observability.ProviderSystemFromBaseURL(baseURL),
		Model:  modelName,
	})
	defer span.End()

	start := time.Now()
	systemPrompt := prompts.FactCheckSystemPrompt(lang)
	userPrompt := prompts.FactCheckUserPrompt(question, answer, contextText)

	result, err := GenerateCompletion(ctx, resolver, userPrompt, systemPrompt, kbID, false)
	if err != nil {
		logctx.From(ctx).Warn("fact check failed", "error", err)
		fc := &FactCheckResult{Verified: false, Score: 0, Issues: []string{"Fact check failed due to system error."}}
		logctx.From(ctx).Info("rag.factcheck",
			"stage", "factcheck",
			"verified", fc.Verified,
			"score", fc.Score,
			"issue_count", len(fc.Issues),
			"duration_ms", time.Since(start).Milliseconds(),
		)
		span.SetAttributes(
			attribute.Bool("factcheck.verified", fc.Verified),
			attribute.Float64("factcheck.score", fc.Score),
			attribute.Int("factcheck.issue_count", len(fc.Issues)),
		)
		observability.RecordFactcheck(fc.Verified, fc.Score, time.Since(start).Seconds())
		return fc
	}

	// Parse JSON from response
	var fc FactCheckResult
	if err := parseJSONFromText(result.Content, &fc); err != nil {
		logctx.From(ctx).Warn("fact check JSON parse failed", "error", err)
		out := &FactCheckResult{Verified: false, Score: 0, Issues: []string{"Fact check response could not be parsed."}}
		logctx.From(ctx).Info("rag.factcheck",
			"stage", "factcheck",
			"verified", out.Verified,
			"score", out.Score,
			"issue_count", len(out.Issues),
			"duration_ms", time.Since(start).Milliseconds(),
		)
		span.SetAttributes(
			attribute.Bool("factcheck.verified", out.Verified),
			attribute.Float64("factcheck.score", out.Score),
			attribute.Int("factcheck.issue_count", len(out.Issues)),
		)
		observability.RecordFactcheck(out.Verified, out.Score, time.Since(start).Seconds())
		return out
	}

	// Ensure issues is never nil
	if fc.Issues == nil {
		fc.Issues = []string{}
	}

	logctx.From(ctx).Info("rag.factcheck",
		"stage", "factcheck",
		"verified", fc.Verified,
		"score", fc.Score,
		"issue_count", len(fc.Issues),
		"duration_ms", time.Since(start).Milliseconds(),
	)
	span.SetAttributes(
		attribute.Bool("factcheck.verified", fc.Verified),
		attribute.Float64("factcheck.score", fc.Score),
		attribute.Int("factcheck.issue_count", len(fc.Issues)),
	)
	observability.RecordFactcheck(fc.Verified, fc.Score, time.Since(start).Seconds())
	return &fc
}

// parseJSONFromText tries to unmarshal text as JSON. If that fails, it looks for
// a JSON object within the text (LLMs sometimes wrap JSON in explanation).
func parseJSONFromText(text string, v any) error {
	if err := json.Unmarshal([]byte(text), v); err == nil {
		return nil
	}
	// Try to find JSON object in the text
	start := -1
	for i, c := range text {
		if c == '{' {
			start = i
			break
		}
	}
	end := -1
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
	return fmt.Errorf("no valid JSON found in response")
}
