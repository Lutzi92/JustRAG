package ai

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
)

// KBRouterMatch is one entry in the AP-A4 router's ranked list.
// Confidence is the LLM's self-rated 0..1 score.
type KBRouterMatch struct {
	KBID       string  `json:"kb_id"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason,omitempty"`
}

type kbRouterResponse struct {
	Matches []KBRouterMatch `json:"matches"`
}

// kbRouterSpec is the Structured-Outputs contract for RouteKB, matching
// the {"matches":[...]} shape parseKBRouterJSON expects. kb_id stays a
// free-form string (the candidate UUID list is dynamic per call);
// hallucinated IDs are still filtered against the candidate allowlist
// after parsing. parseKBRouterJSON stays tolerant for the json_object
// fallback path.
var kbRouterSpec = &StructuredSpec{
	Name: "kb_router_matches",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"matches": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"kb_id": {"type": "string"},
						"confidence": {"type": "number"},
						"reason": {"type": "string"}
					},
					"required": ["kb_id", "confidence", "reason"],
					"additionalProperties": false
				}
			}
		},
		"required": ["matches"],
		"additionalProperties": false
	}`),
}

// RouteKB asks the LLM which KB(s) the query most likely belongs to.
// Returns the matches in confidence-descending order. An empty list
// signals "no plausible match" — the caller falls back to the
// existing search-all path.
//
// On any LLM-side failure (call error, parse error) returns nil + the
// error, mirroring the verifier path. The router is fail-open at
// the chat layer: an LLM failure surfaces as `outcome=llm_error` and
// the caller proceeds with the user's original kb_id.
//
// modelOverride empty → resolver default; for routing we want a
// small/fast model (the decision is one-shot classification, not
// reasoning). Set `chat_kb_router_model` if the default chat model
// is overkill for this hop.
func RouteKB(ctx context.Context, resolver *ConfigResolver, query string, candidates []prompts.KBRouterCandidate, kbID, modelOverride string) ([]KBRouterMatch, error) {
	start := time.Now()
	defer func() {
		observability.ObserveKBRouterSeconds(time.Since(start).Seconds())
	}()

	if len(candidates) == 0 {
		return nil, fmt.Errorf("kb_router: no candidates")
	}

	sys := prompts.KBRouterSystemPrompt()
	user := prompts.KBRouterUserPrompt(query, candidates)

	res, err := resolver.structuredCompletionFn(ctx, user, sys, kbID, modelOverride, kbRouterSpec)
	if err != nil {
		logctx.From(ctx).Warn("kb_router: completion failed", "error", err)
		return nil, fmt.Errorf("kb_router: completion: %w", err)
	}
	if res == nil || strings.TrimSpace(res.Content) == "" {
		return nil, fmt.Errorf("kb_router: empty completion")
	}

	parsed, perr := parseKBRouterJSON(res.Content)
	if perr != nil {
		logctx.From(ctx).Warn("kb_router: parse failed", "error", perr,
			"raw", firstNChars(res.Content, 240))
		return nil, fmt.Errorf("kb_router: parse: %w", perr)
	}

	// Defensive: drop entries with no kb_id, clamp confidence to [0,1],
	// and sort by confidence descending. The LLM is instructed to
	// pre-sort but we don't trust that.
	out := make([]KBRouterMatch, 0, len(parsed.Matches))
	allowed := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		allowed[c.ID] = true
	}
	for _, m := range parsed.Matches {
		id := strings.TrimSpace(m.KBID)
		if id == "" {
			continue
		}
		// Ignore hallucinated kb_ids — the LLM occasionally emits a
		// UUID that wasn't in the candidate list.
		if !allowed[id] {
			continue
		}
		conf := m.Confidence
		if conf < 0 {
			conf = 0
		}
		if conf > 1 {
			conf = 1
		}
		out = append(out, KBRouterMatch{KBID: id, Confidence: conf, Reason: strings.TrimSpace(m.Reason)})
	}
	slices.SortStableFunc(out, func(a, b KBRouterMatch) int { return cmp.Compare(b.Confidence, a.Confidence) })

	logctx.From(ctx).Info("rag.kb_router.decide",
		"stage", "kb_router",
		"candidates", len(candidates),
		"matches", len(out),
		"top_confidence", topConfidence(out),
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return out, nil
}

func parseKBRouterJSON(text string) (kbRouterResponse, error) {
	var out kbRouterResponse
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
	return kbRouterResponse{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}

func topConfidence(out []KBRouterMatch) float64 {
	if len(out) == 0 {
		return 0
	}
	return out[0].Confidence
}
