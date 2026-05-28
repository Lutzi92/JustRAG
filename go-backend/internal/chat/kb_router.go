package chat

import (
	"context"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
)

// KBRouterCandidateLister abstracts the dependency on the kb package
// so the chat handler can read the user's KB list without an import
// cycle. Implementation lives in publicapi or wherever kb.Store is
// already wired into the chat handler.
type KBRouterCandidateLister interface {
	ListKBRouterCandidates(ctx context.Context, userID string) ([]prompts.KBRouterCandidate, error)
}

// ResolveKBViaRouter is the AP-A4 entry point called from the chat
// handler. Given the user's query + the explicit URL kb_id the
// caller passed, it returns the kb_id the rest of the pipeline
// should use. When the gate is off OR the request didn't request
// auto-routing OR the LLM call failed, it returns the original
// `requestedKBID` unchanged — never returns the empty string.
//
// The `auto` boolean is the chat handler's read of the query param
// `?route=auto`. The handler controls when to invoke this; the
// resolver itself is config-aware (skipped_disabled outcome when
// the gate is off and auto=true) for clean metrics.
//
// Returns (resolvedKBID, outcome, err). outcome is one of the labels
// the metric counter recognises; the caller uses it for trajectory
// emission. err is non-nil only on infrastructure failures (DB
// errors); LLM errors are absorbed into outcome=llm_error.
func ResolveKBViaRouter(
	ctx context.Context,
	resolver *ai.ConfigResolver,
	siteCfg SiteConfigReader,
	lister KBRouterCandidateLister,
	userID, requestedKBID, query string,
	auto bool,
) (string, string, error) {
	if !auto {
		observability.RecordKBRouterDecision("skipped_no_auto")
		return requestedKBID, "skipped_no_auto", nil
	}
	if !ChatKBRouterEnabled(ctx, siteCfg) {
		observability.RecordKBRouterDecision("skipped_disabled")
		return requestedKBID, "skipped_disabled", nil
	}
	if lister == nil {
		// No candidate provider wired — config-by-construction error,
		// not a runtime one. Surface as llm_error for now (operator
		// will see the metric spike and a debug log line).
		observability.RecordKBRouterDecision("llm_error")
		logctx.From(ctx).Warn("kb_router: no candidate lister configured")
		return requestedKBID, "llm_error", nil
	}

	candidates, err := lister.ListKBRouterCandidates(ctx, userID)
	if err != nil {
		return requestedKBID, "llm_error", err
	}
	if len(candidates) == 0 {
		observability.RecordKBRouterDecision("skipped_no_candidates")
		return requestedKBID, "skipped_no_candidates", nil
	}

	start := time.Now()
	matches, rerr := ai.RouteKB(ctx, resolver, query, candidates,
		requestedKBID, ChatKBRouterModel(ctx, siteCfg))
	dur := time.Since(start)
	if rerr != nil {
		observability.RecordKBRouterDecision("llm_error")
		logctx.From(ctx).Warn("kb_router: LLM call failed; keeping requested kb_id",
			"error", rerr, "duration_ms", dur.Milliseconds())
		return requestedKBID, "llm_error", nil
	}
	if len(matches) == 0 {
		observability.RecordKBRouterDecision("fallback_all")
		logctx.From(ctx).Info("rag.kb_router.fallback",
			"reason", "no_matches", "candidates", len(candidates))
		return requestedKBID, "fallback_all", nil
	}

	threshold := ChatKBRouterMinConfidence(ctx, siteCfg)
	top := matches[0]
	if top.Confidence < threshold {
		observability.RecordKBRouterDecision("fallback_all")
		logctx.From(ctx).Info("rag.kb_router.fallback",
			"reason", "below_threshold",
			"top_kb_id", top.KBID,
			"top_confidence", top.Confidence,
			"threshold", threshold)
		return requestedKBID, "fallback_all", nil
	}

	outcome := "single"
	if len(matches) > 1 && matches[1].Confidence >= threshold {
		// Two or more candidates clear the threshold. Today the
		// resolver still picks top-1 (single-KB query path); the
		// fan-out is recorded for telemetry so operators can spot
		// queries where the router would have benefited from
		// shadow searches once Phase B's RRF infrastructure lands.
		outcome = "fanout"
	}
	observability.RecordKBRouterDecision(outcome)
	logctx.From(ctx).Info("rag.kb_router.pick",
		"requested_kb_id", requestedKBID,
		"resolved_kb_id", top.KBID,
		"top_confidence", top.Confidence,
		"outcome", outcome)
	return top.KBID, outcome, nil
}
