package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
)

// RefineResult is what the chat-side caller logs into the verification
// blob. ClaimsBefore is the count that triggered refine (after filter
// to refine-eligible reasons); ClaimsAfter is the verifier's verdict on
// the refined answer.
type RefineResult struct {
	Mode         prompts.RefineMode `json:"mode"`
	ClaimsBefore int                `json:"claims_before"`
	ClaimsAfter  int                `json:"claims_after"`
	Content      string             `json:"-"` // refined answer body; not persisted in this blob
}

// ClaimsTriggeringRefine returns the subset of flagged claims that
// should trigger a refine round. The plan limits refine to
// "unsupported" and "contradicted" — out_of_scope claims are policy
// concerns the user already opted into (e.g. permitting general
// knowledge), and a refine pass on them would just remove correct
// auxiliary text. Exported so the chat package can apply the same
// filter when measuring v2 (post-refine) flagging without duplicating
// the reason-set.
func ClaimsTriggeringRefine(claims []FlaggedClaim) []FlaggedClaim {
	out := make([]FlaggedClaim, 0, len(claims))
	for _, c := range claims {
		switch c.Reason {
		case "unsupported", "contradicted":
			out = append(out, c)
		}
	}
	return out
}

// SelectRefineMode picks surgical vs holistic based on the count of
// refine-triggering claims. Threshold is 3: 1–3 → surgical (cheaper,
// preserves correct prose), 4+ → holistic (the answer is so heavily
// flagged that surgical edits would produce a fragmented or
// self-contradictory text). The threshold is the user-confirmed default
// from AP-A1 brainstorming; tune via the caller if a future eval shows
// drift.
func SelectRefineMode(claims []FlaggedClaim) prompts.RefineMode {
	if len(claims) >= 4 {
		return prompts.RefineModeHolistic
	}
	return prompts.RefineModeSurgical
}

// RefineFlaggedClaims runs ONE refine round against the original
// answer + the claims the verifier flagged. The mode is picked by the
// caller via SelectRefineMode (kept separate so tests can fix the mode
// without re-deriving from inputs).
//
// On any LLM-side failure (call error, empty content) returns the
// original answer unchanged with a non-nil error — the caller decides
// whether to log + drop (current default) or surface to the user. The
// "fail-open keeps original" behaviour mirrors the verifier path
// (factuality_verifier.go:48-58).
//
// `sourcesBlock` is the same context-text the original answer was
// produced from — passing in the rebuilt block here would risk
// renumbering [N] markers and breaking the verifier's second pass.
func RefineFlaggedClaims(
	ctx context.Context,
	resolver *ConfigResolver,
	question, originalAnswer, sourcesBlock string,
	claims []FlaggedClaim,
	mode prompts.RefineMode,
	kbID, lang, modelOverride string,
) (string, error) {
	start := time.Now()
	defer func() {
		observability.ObserveFactualityRefineSeconds(time.Since(start).Seconds())
	}()

	if len(claims) == 0 {
		return originalAnswer, nil
	}

	promptClaims := make([]prompts.FlaggedClaimPromptInput, len(claims))
	for i, c := range claims {
		promptClaims[i] = prompts.FlaggedClaimPromptInput{
			ClaimText: c.ClaimText,
			Reason:    c.Reason,
			CitedN:    c.CitedN,
		}
	}

	sys := prompts.FactualityRefineSystemPrompt(lang, mode)
	user := prompts.FactualityRefineUserPrompt(question, originalAnswer, sourcesBlock, promptClaims)

	res, err := resolver.completionFn(ctx, user, sys, kbID, modelOverride)
	if err != nil {
		observability.RecordFactualityRefine("error")
		logctx.From(ctx).Warn("factuality_refine: completion failed", "error", err, "mode", string(mode))
		return originalAnswer, fmt.Errorf("factuality_refine: completion: %w", err)
	}
	if res == nil || strings.TrimSpace(res.Content) == "" {
		observability.RecordFactualityRefine("error")
		return originalAnswer, fmt.Errorf("factuality_refine: empty completion")
	}

	refined := strings.TrimSpace(res.Content)
	if refined == strings.TrimSpace(originalAnswer) {
		observability.RecordFactualityRefine("unchanged")
	} else {
		observability.RecordFactualityRefine("refined")
	}
	logctx.From(ctx).Info("rag.factuality.refine",
		"stage", "factuality_refine",
		"mode", string(mode),
		"claims", len(claims),
		"duration_ms", time.Since(start).Milliseconds(),
		"len_before", len(originalAnswer),
		"len_after", len(refined),
	)
	return refined, nil
}
