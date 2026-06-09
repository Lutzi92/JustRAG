package chat

import (
	"context"
	"strings"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/prompts"
)

// runRefineGate executes the AP-A1/A2 refine round in the main
// goroutine. Returns (refinedAnswer, refineStatus, finalClaims). On
// any fail-open path, refinedAnswer stays empty and finalClaims
// stays at the v1 verdict.
//
// Trajectory emission (AP-A2): refine_start fires BEFORE the LLM
// call so the frontend paints "Korrigiert nach Faktencheck …" as
// soon as the gate decides to refine. refine_complete fires AFTER
// the v2 verifier returns, carrying the word-level diff and the
// residual claim count. Both events go through emitTrajectory which
// is a no-op when emit is nil.
func (h *Handler) runRefineGate(
	ctx context.Context,
	emit func(map[string]any),
	userMessage, aiResponse, contextText, kbID, lang, aiMsgID string,
	v1Claims []ai.FlaggedClaim,
	triggering []ai.FlaggedClaim,
	forceHolistic bool,
) (string, *RefineStatus, []ai.FlaggedClaim) {
	mode := ai.SelectRefineMode(triggering)
	// AP-D2: ISUSE=no overrides the surgical/holistic threshold.
	// "Wrong answer entirely" needs a full rewrite even when the
	// triggering claim count is low (or zero, in the
	// no-ISSUP-but-ISUSE-failed case).
	if forceHolistic {
		mode = prompts.RefineModeHolistic
	}
	refineModel := ChatRefineModel(ctx, h.siteConfigReader)

	emitTrajectory(emit, TrajectoryEvent{
		Stage:        "refine_start",
		Mode:         string(mode),
		ClaimsBefore: len(triggering),
	}, nil)

	refined, rerr := ai.RefineFlaggedClaims(ctx, h.aiResolver,
		userMessage, aiResponse, contextText, triggering,
		mode, kbID, lang, refineModel)
	if rerr != nil {
		logctx.From(ctx).Warn("factuality refine failed; keeping original answer",
			"error", rerr, "mode", string(mode), "triggering", len(triggering))
		return "", nil, v1Claims
	}
	if strings.TrimSpace(refined) == "" {
		logctx.From(ctx).Warn("factuality refine: empty refined output; keeping original answer",
			"mode", string(mode), "triggering", len(triggering))
		return "", nil, v1Claims
	}
	if strings.TrimSpace(refined) == strings.TrimSpace(aiResponse) {
		// Identical text: skip persist + v2 verifier (would re-spend
		// the same LLM call on the same input). Still emit complete
		// with an empty diff so the frontend can clear its
		// "korrigiert" indicator instead of leaving it spinning.
		status := &RefineStatus{
			Mode:         string(mode),
			ClaimsBefore: len(triggering),
			ClaimsAfter:  len(triggering),
		}
		emitTrajectory(emit, TrajectoryEvent{
			Stage:        "refine_complete",
			Mode:         string(mode),
			ClaimsBefore: len(triggering),
			ClaimsAfter:  len(triggering),
			Diff:         nil,
			Reason:       "unchanged",
		}, nil)
		return "", status, v1Claims
	}

	if uerr := h.store.UpdateMessageContent(ctx, aiMsgID, refined); uerr != nil {
		logctx.From(ctx).Warn("factuality refine: failed to persist refined content",
			"error", uerr, "messageId", aiMsgID)
		return "", nil, v1Claims
	}

	v2Claims, v2err := ai.VerifyFactuality(ctx, h.aiResolver,
		userMessage, refined, contextText, kbID, lang,
		ChatFactualityVerifierModel(ctx, h.siteConfigReader))
	diff := computeRefineDiff(aiResponse, refined)
	if v2err != nil {
		logctx.From(ctx).Warn("factuality refine: second-pass verifier failed; surfacing v1 verdict",
			"error", v2err)
		status := &RefineStatus{
			Mode:         string(mode),
			ClaimsBefore: len(triggering),
			ClaimsAfter:  -1, // sentinel: second-pass errored
		}
		emitTrajectory(emit, TrajectoryEvent{
			Stage:        "refine_complete",
			Mode:         string(mode),
			ClaimsBefore: len(triggering),
			ClaimsAfter:  -1,
			Diff:         diff,
			RefinedText:  refined,
			Reason:       "v2_verifier_error",
		}, nil)
		return refined, status, v1Claims
	}

	finalClaims := v2Claims
	status := &RefineStatus{
		Mode:         string(mode),
		ClaimsBefore: len(triggering),
		ClaimsAfter:  len(ai.ClaimsTriggeringRefine(v2Claims)),
	}
	emitTrajectory(emit, TrajectoryEvent{
		Stage:        "refine_complete",
		Mode:         string(mode),
		ClaimsBefore: status.ClaimsBefore,
		ClaimsAfter:  status.ClaimsAfter,
		Diff:         diff,
		RefinedText:  refined,
	}, nil)
	return refined, status, finalClaims
}
