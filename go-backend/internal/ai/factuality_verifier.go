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

// FlaggedClaim is one entry the factuality verifier produces. It
// names a verbatim claim from the answer plus the reason the verifier
// considers it unsupported.
type FlaggedClaim struct {
	ClaimText string `json:"claim_text"`
	Reason    string `json:"reason"` // "unsupported" | "contradicted" | "out_of_scope"
	CitedN    int    `json:"cited_n,omitempty"`
}

// factualityVerifierResponse is the wire shape the LLM returns. The
// public type uses a slice of FlaggedClaim directly; this struct is
// just the JSON envelope.
type factualityVerifierResponse struct {
	FlaggedClaims []FlaggedClaim `json:"flagged_claims"`
}

// VerifyFactuality runs the Phase 3 §3.3 factuality verifier against
// one answer + its source block. Returns the (possibly empty) list of
// flagged claims. On any LLM-side failure (call error, parse error)
// returns nil + the error — the chat orchestrator's caller decides
// whether a verifier failure should be loud (it currently logs +
// drops; the answer ships without verification).
//
// modelOverride empty → resolver default; the caller passes
// `chat_factuality_verifier_model` when one is configured. Latency is
// measured here and bubbled out via observability — the gate logic
// (cost-gate vs always-run) lives at the caller.
func VerifyFactuality(ctx context.Context, resolver *ConfigResolver, question, answer, sourcesBlock, kbID, lang, modelOverride string) ([]FlaggedClaim, error) {
	start := time.Now()
	sys := prompts.FactualityVerifierSystemPrompt(lang)
	user := prompts.FactualityVerifierUserPrompt(question, answer, sourcesBlock)

	res, err := resolver.completionFn(ctx, user, sys, kbID, modelOverride)
	if err != nil {
		observability.RecordFactualityVerifier("error")
		observability.ObserveFactualityVerifierSeconds(time.Since(start).Seconds())
		logctx.From(ctx).Warn("factuality_verifier: completion failed", "error", err)
		return nil, fmt.Errorf("factuality_verifier: completion: %w", err)
	}
	if res == nil || res.Content == "" {
		observability.RecordFactualityVerifier("error")
		observability.ObserveFactualityVerifierSeconds(time.Since(start).Seconds())
		return nil, fmt.Errorf("factuality_verifier: empty completion")
	}

	parsed, perr := parseFactualityJSON(res.Content)
	if perr != nil {
		observability.RecordFactualityVerifier("error")
		observability.ObserveFactualityVerifierSeconds(time.Since(start).Seconds())
		logctx.From(ctx).Warn("factuality_verifier: parse failed", "error", perr, "raw", firstNChars(res.Content, 240))
		return nil, fmt.Errorf("factuality_verifier: parse: %w", perr)
	}

	// Defensive cleanup: drop entries with no claim text or unknown
	// reasons. The LLM occasionally hallucinates a "no_violations"
	// reason for an empty payload — treating that as success keeps
	// the UI clean.
	out := make([]FlaggedClaim, 0, len(parsed.FlaggedClaims))
	for _, c := range parsed.FlaggedClaims {
		text := strings.TrimSpace(c.ClaimText)
		reason := strings.TrimSpace(strings.ToLower(c.Reason))
		if text == "" {
			continue
		}
		switch reason {
		case "unsupported", "contradicted", "out_of_scope":
		default:
			reason = "unsupported"
		}
		out = append(out, FlaggedClaim{ClaimText: text, Reason: reason, CitedN: c.CitedN})
	}

	if len(out) == 0 {
		observability.RecordFactualityVerifier("clean")
	} else {
		observability.RecordFactualityVerifier("flagged")
	}
	observability.ObserveFactualityVerifierSeconds(time.Since(start).Seconds())
	logctx.From(ctx).Info("rag.factuality_verifier",
		"stage", "factuality_verifier",
		"flagged_count", len(out),
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return out, nil
}

func parseFactualityJSON(text string) (factualityVerifierResponse, error) {
	var out factualityVerifierResponse
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
	return factualityVerifierResponse{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}
