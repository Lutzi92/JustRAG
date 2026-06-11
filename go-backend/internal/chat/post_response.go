package chat

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/safego"
)

// detachedContext returns a fresh background context that carries any
// OTel span attached to parent. The request context may already be
// canceled when an analytics insert kicks off (SSE drained), but the
// trace it belonged to should still see the work — otherwise distributed
// tracing loses the tail of the request.
func detachedContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	span := trace.SpanFromContext(parent)
	bg := trace.ContextWithSpan(context.Background(), span)
	return context.WithTimeout(bg, timeout)
}

// ---------------------------------------------------------------------------
// runPostResponseTasks — parallel follow-ups + factcheck + citation validator
// ---------------------------------------------------------------------------

// runPostResponseTasks runs follow-up generation, the LLM-based factchecker,
// and the deterministic citation validator concurrently after the main LLM
// response is complete. They have no data dependency on each other, so
// parallelizing keeps the post-stream wait at max(slowest), not their sum.
//
// Both verification subsystems are independently gated by site config:
//   - factcheck → site_config "factcheck_in_chat" (default on)
//   - citation validator → site_config "citation_validation_enabled" (default off)
//
// Their outputs are merged into a single MessageVerification that is
// persisted on the message and emitted to the frontend as the existing
// `{"verification": ...}` SSE payload. The wrapper is emitted whenever AT
// LEAST ONE subsystem produced output, so admins who run only the citation
// validator still get UI badges. Factcheck-only fields stay zero-valued
// when the factchecker didn't run; the frontend treats their absence as
// "not run" rather than "failed".
// runPostResponseTasks returns followUps, the merged verification blob,
// and the refined answer body when the AP-A1 gate produced one
// (otherwise empty string). Callers in the streaming path pass the
// refined string into AP-A2's diff/SSE flow; the non-streaming path
// substitutes it for the original answer in the JSON response. An
// empty refinedAnswer is the common case (gate off, verifier clean,
// LLM error, or no-op refine) — callers must treat empty as "use
// original".
//
// emit (AP-A2): SSE write callback used to stream refine_start /
// refine_complete trajectory events while the refine LLM call runs.
// Streaming callers pass `func(p) { writeSSE(w, p) }`; non-streaming
// callers pass nil. Emit is called from the same goroutine that owns
// the SSE writer — refine + the v2 verifier are deliberately moved
// out of the parallel wg into the post-wait main path so the writer
// stays single-owner and we don't need a mutex around writeSSE.

// Per-goroutine result containers for the parallel post-response work.
// Each goroutine owns exactly one of these and writes into its own fields;
// the parent merges after wg.Wait. Splitting the captured state by writer
// makes ownership compile-time visible — a second writer can't silently
// reach into another goroutine's namespace as it would with shared
// closure-captured variables.
type (
	followUpResult struct {
		followUps []string
	}
	factcheckOutcome struct {
		result *ai.FactCheckResult
	}
	verifierOutcome struct {
		citationStatuses []CitationStatus
		factualityClaims []ai.FlaggedClaim
		selfRAGResult    *ai.SelfRAGResult
		// verifierRan tracks whether either AP-A1 (Factuality) or
		// AP-D2 (Self-RAG) actually completed a verification call,
		// regardless of whether the result flagged anything. AP-E2
		// uses it to decide whether to emit an online_faithfulness
		// observation — a clean-running verifier should still
		// contribute a 1.0 observation.
		verifierRan bool
	}
)

// postResponseTimeout bounds the detached post-response work below. Two
// minutes covers the worst case (verifier LLM + refine LLM sequentially)
// while capping how long an abandoned turn can keep spending tokens.
const postResponseTimeout = 2 * time.Minute

func (h *Handler) runPostResponseTasks(
	ctx context.Context,
	userMessage, aiResponse, contextText, kbID, lang, aiMsgID string,
	sources []ChatSource,
	emit func(map[string]any),
) ([]string, *MessageVerification, string) {
	// Detach from client disconnect: the AI message is already persisted by
	// the time we run, so letting a disconnect cancel this work would
	// silently lose the verification badge and longmem memories of a
	// message that exists (the user sees it on reload). WithoutCancel keeps
	// every request-scoped value — logctx logger, auth user, OTel span —
	// unlike detachedContext, which only carries the span. The emit
	// callback tolerates a dead client (writeSSE logs and drops).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postResponseTimeout)
	defer cancel()

	var (
		fuRes  followUpResult
		fcRes  factcheckOutcome
		verRes verifierOutcome
		wg     sync.WaitGroup
	)

	// safego.GoCtx provides panic recovery: a panic in any of these AI/validation
	// calls (e.g. nil pointer in a model response, unexpected JSON shape) would
	// otherwise crash the process and bypass shutdown logic. The Ctx variant
	// threads the request-scoped logger so any recovered crash logs with
	// request_id / user_id / kb_id correlation. wg.Done is the inner-most
	// deferred call so it runs even if the work panics, before the recover
	// catches the panic.
	wg.Add(1)
	safego.GoCtx(ctx, func() {
		defer wg.Done()
		fuRes.followUps, _ = ai.GenerateFollowUpQuestions(ctx, h.aiResolver, userMessage, aiResponse, kbID, lang)
	})

	if FactcheckEnabled(ctx, h.siteConfigReader) {
		wg.Add(1)
		safego.GoCtx(ctx, func() {
			defer wg.Done()
			fcRes.result = ai.CheckFacts(ctx, h.aiResolver, userMessage, aiResponse, contextText, kbID, lang)
		})
	}

	// AP-D1 long-term memory write: fire-and-forget extraction +
	// store. Lives in the parallel waitgroup alongside follow-ups
	// + factcheck because (a) it has no data dependency on the
	// other post-response work and (b) the LLM call is the bulk
	// of latency, so we want it to run alongside its cost-peers.
	// Failures log + drop — never propagate to the user-facing
	// answer or block the SSE close.
	if h.longmemStore != nil && ChatLongmemEnabled(ctx, h.siteConfigReader) {
		user := auth.UserFromContext(ctx)
		if user != nil && user.ID != "" {
			wg.Add(1)
			safego.GoCtx(ctx, func() {
				defer wg.Done()
				h.runLongmemExtract(ctx, user.ID, kbID, lang, userMessage, aiResponse)
			})
		}
	}

	// Citation validator + factuality verifier share one goroutine so the
	// verifier can gate on the validator's output. The two run sequentially
	// inside a single safego.Go: validator first → if it raised at least
	// one suspect (Verified=false) AND the verifier is enabled, the
	// verifier fires; otherwise it records `skipped_no_citation_warn` and
	// exits. The whole bundle runs in parallel with the factchecker via
	// the outer wg, so the only added latency from gating is the validator
	// step itself, which is dominated by an embedding round-trip — well
	// under the LLM call we're trying to avoid.
	//
	// chat_factuality_verifier_always_run flips the gate off: the
	// verifier runs regardless of validator output. Intended for
	// high-stakes deployments that prefer the cost.
	validatorEnabled := CitationValidationEnabled(ctx, h.siteConfigReader)
	verifierEnabled := ChatFactualityVerifierEnabled(ctx, h.siteConfigReader)
	verifierAlwaysRun := ChatFactualityVerifierAlwaysRun(ctx, h.siteConfigReader)
	gateEnabled := ChatFactualityGateEnabled(ctx, h.siteConfigReader)
	gateMaxRefines := ChatFactualityGateMaxRefines(ctx, h.siteConfigReader)
	// AP-D2: when chat_self_rag_enabled is on, Self-RAG REPLACES the
	// legacy factuality verifier in the goroutine below. Mutually
	// exclusive — running both would duplicate cost. Self-RAG's
	// ISSUP output feeds factualityClaims unchanged so the existing
	// AP-A1 refine gate works without modification; ISUSE drives an
	// extra "force holistic" trigger documented near runRefineGate.
	selfRAGEnabled := ChatSelfRAGEnabled(ctx, h.siteConfigReader)
	var (
		refineStatus *RefineStatus
		// refinedAnswer carries the post-refine text out to the caller
		// so the SSE/JSON layer can surface it; AP-A2 will also stream
		// a diff event. Empty string means refine did not produce new
		// text (gate off, no eligible claims, LLM error, or unchanged).
		refinedAnswer string
	)
	// Phase F: telemetry on what RAPTOR contributed to the final
	// chunk pool. Independent of the validator/verifier gates
	// because operators want this signal even when those gates are
	// off ("did summaries even make it into the final pool?").
	for _, src := range sources {
		observability.RecordRaptorRetrieved(src.NodeKind)
	}

	// Phase F: if any source is a RAPTOR summary AND the
	// descendant resolver is wired, fetch descendant leaf bodies
	// once for the whole source set and populate
	// ChatSource.DescendantContents in place. The citation
	// validator's n-gram check ORs source content with descendants
	// so a paraphrased summary still validates when a leaf
	// supports the cited claim. Failure logs warn + continues —
	// the validator's semantic-similarity fallback is the last
	// line of defence.
	//
	// CONCURRENCY INVARIANT: the loop below writes sources[i].DescendantContents
	// in place. Every goroutine launched ABOVE this point (follow-ups, factcheck,
	// longmem) must NOT read sources — they don't, which is what keeps this
	// mutation race-free without a lock. The citation validator goroutine is
	// launched AFTER this mutation precisely so it observes the populated slice.
	// Do not introduce a sources-reading goroutine between the launches above
	// and this loop, or you create a silent data race.
	if h.raptorDescendants != nil {
		summaryIDs := summaryIDsFromSources(sources)
		if len(summaryIDs) > 0 {
			descs, dErr := h.raptorDescendants.GetRaptorDescendantLeafContentsAcrossDims(ctx, summaryIDs)
			if dErr != nil {
				logctx.From(ctx).Warn("raptor: descendant resolver failed", "kbId", kbID, "err", dErr)
			} else {
				for i := range sources {
					if sources[i].NodeKind == "summary" && sources[i].ChunkID != "" {
						sources[i].DescendantContents = descs[sources[i].ChunkID]
					}
				}
			}
		}
	}

	if validatorEnabled || verifierEnabled {
		wg.Add(1)
		safego.GoCtx(ctx, func() {
			defer wg.Done()
			hasCitationSuspect := false
			if validatorEnabled {
				// Phase 2 §B: the n-gram pass + semantic-similarity fallback.
				// SemanticConfig threshold below is the cosine floor; an embed
				// callback is bound to the per-KB embedding model so the
				// answer-window vector and the source-chunk vector live in
				// the same space. ai.GenerateEmbedding is symmetric — the
				// answer window is generated prose, not a query, so the
				// asymmetric query-side prefix would split the spaces.
				sem := &SemanticConfig{
					Embed: func(ctx context.Context, text string) ([]float64, error) {
						return ai.GenerateEmbedding(ctx, h.aiResolver, text, kbID, nil)
					},
					Threshold: CitationValidationSemanticThreshold(ctx, h.siteConfigReader),
				}
				verRes.citationStatuses = RunCitationValidation(ctx, aiResponse, sources, sem)
				for _, c := range verRes.citationStatuses {
					// Per-marker attribution telemetry: verified/(verified+
					// unverified) over a window is the attribution rate. Record
					// every marker (no early break) so the denominator is whole.
					observability.RecordCitationAttribution(c.Verified, c.Method)
					if !c.Verified {
						hasCitationSuspect = true
					}
				}
				// Phase F cite-by-kind telemetry: increment after
				// validation so we count only the citations the
				// validator accepted (Verified=true). The
				// .NodeKind label lets operators see whether
				// summaries contribute to the answer's actual
				// claims (vs. just sitting in the pool).
				for _, c := range verRes.citationStatuses {
					if !c.Verified {
						continue
					}
					idx := c.N - 1
					if idx >= 0 && idx < len(sources) {
						observability.RecordRaptorCited(sources[idx].NodeKind)
					}
				}
			}

			if !verifierEnabled && !selfRAGEnabled {
				return
			}
			// Cost gate: skip the verifier when (a) the validator
			// did NOT raise a suspect, OR (b) the validator wasn't
			// enabled (so we have no gate signal). always_run
			// bypasses both conditions. The same gate applies to
			// Self-RAG when it's the active verifier.
			if !verifierAlwaysRun && !hasCitationSuspect {
				if selfRAGEnabled {
					observability.RecordSelfRAG("skipped_no_citation_warn")
				} else {
					observability.RecordFactualityVerifier("skipped_no_citation_warn")
				}
				return
			}

			// AP-D2 path: Self-RAG produces ISREL + ISSUP + ISUSE in
			// one LLM call. ISSUP feeds the existing FlaggedClaim
			// pipeline so refine + verification merge work
			// unchanged.
			if selfRAGEnabled {
				res, srerr := ai.VerifySelfRAG(ctx, h.aiResolver,
					userMessage, aiResponse, contextText, kbID, lang,
					ChatSelfRAGModel(ctx, h.siteConfigReader))
				if srerr != nil {
					logctx.From(ctx).Warn("self_rag failed; proceeding without verdict", "error", srerr)
					return
				}
				verRes.factualityClaims = res.FlaggedClaims
				verRes.selfRAGResult = &res
				verRes.verifierRan = true
				return
			}

			// Legacy AP-A1 path: factuality verifier only.
			claims, verr := ai.VerifyFactuality(ctx, h.aiResolver,
				userMessage, aiResponse, contextText, kbID, lang,
				ChatFactualityVerifierModel(ctx, h.siteConfigReader))
			if verr != nil {
				logctx.From(ctx).Warn("factuality verifier failed; proceeding without verdict", "error", verr)
				return
			}
			verRes.factualityClaims = claims
			verRes.verifierRan = true
			// AP-A2 NOTE: refine + v2 verifier deliberately moved out
			// of this goroutine so they run sequentially in the main
			// path under the SSE writer's owning goroutine. That lets
			// us emit refine_start / refine_complete trajectory events
			// without locking writeSSE.
		})
	}

	// Phase 3 §G RAGAS sampling: with probability ragas_sampling_rate, enqueue
	// an asynq task that runs the offline judge against this response. Lives
	// outside the wg because the enqueue is microseconds — the actual judge
	// calls happen in the worker on QueueBatch. Defaults make this a no-op:
	// rate=0.0 means no enqueue, master switch ragas_sampling_enabled=false
	// short-circuits before the dice roll.
	if h.asynqClient != nil && RAGASSamplingEnabled(ctx, h.siteConfigReader) {
		rate := RAGASSamplingRate(ctx, h.siteConfigReader)
		if rate > 0 && rand.Float64() < rate {
			h.enqueueRAGASSample(ctx, kbID, lang, aiMsgID, userMessage, aiResponse, sources)
		}
	}

	wg.Wait()

	// After wg.Wait the result structs have been fully written. Take
	// a working copy of factualityClaims so the refine gate (which may
	// overwrite it with v2 claims) doesn't have to mutate verRes
	// in place — keeps verRes immutable from here on.
	factualityClaims := verRes.factualityClaims

	// AP-A1/A2 refine gate: runs in the main goroutine after wg.Wait
	// so the SSE emit callback has single-owner semantics. Eligibility
	// requires (a) the gate is on, (b) EITHER the verifier ran AND
	// produced at least one refine-triggering claim OR the AP-D2
	// Self-RAG verifier set ISUSE != "yes" (answer doesn't address
	// the question). emit may be nil for non-streaming callers —
	// emitTrajectory handles that.
	if gateEnabled && gateMaxRefines >= 1 {
		triggering := ai.ClaimsTriggeringRefine(factualityClaims)
		isuseTriggered := verRes.selfRAGResult != nil && verRes.selfRAGResult.Usefulness.Verdict != "" && verRes.selfRAGResult.Usefulness.Verdict != "yes"
		if len(triggering) > 0 || isuseTriggered {
			// AP-D2: when ISUSE failed, force holistic mode regardless
			// of claim count — the answer needs a full rewrite, not a
			// per-sentence patch. When ISSUP claims exist alongside,
			// the same holistic call addresses both.
			forceHolistic := isuseTriggered
			refinedAnswer, refineStatus, factualityClaims = h.runRefineGate(
				ctx, emit,
				userMessage, aiResponse, contextText,
				kbID, lang, aiMsgID,
				factualityClaims, triggering, forceHolistic,
			)
		}
	}

	verification := mergeVerification(fcRes.result, verRes.citationStatuses, factualityClaims, refineStatus, verRes.selfRAGResult)
	if verification != nil {
		if err := h.store.UpdateMessageVerification(ctx, aiMsgID, verification); err != nil {
			logctx.From(ctx).Warn("failed to persist message verification", "messageId", aiMsgID, "error", err)
		}
	}

	// AP-E2 online faithfulness: emit a single per-turn score so
	// operators can dashboard / alert on production trend without
	// running the offline eval harness. Fires whenever EITHER
	// verifier (AP-A1 factuality or AP-D2 Self-RAG) actually
	// completed — including the clean case (zero flags, ISUSE=yes
	// → score 1.0). Turns where no verifier ran don't contribute
	// (no signal). Refined answers count against the ORIGINAL
	// flags — the metric is "what did we ship to the user before
	// refine fixed it", not "how good was the final text".
	if verRes.verifierRan {
		score := computeFaithfulnessScore(factualityClaims, verRes.selfRAGResult)
		observability.ObserveOnlineFaithfulness(kbID, score)
	}

	return fuRes.followUps, verification, refinedAnswer
}
