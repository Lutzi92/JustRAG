package chat

import (
	"context"
	"encoding/json"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"go.opentelemetry.io/otel/trace"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/longmem"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
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
// summaryIDsFromSources returns the chunk ids of every source whose
// NodeKind is "summary" AND has a non-empty ChunkID. Used by the
// Phase F descendant-resolution step to batch the recursive CTE
// into one query.
func summaryIDsFromSources(sources []ChatSource) []string {
	var out []string
	for _, s := range sources {
		if s.NodeKind == "summary" && s.ChunkID != "" {
			out = append(out, s.ChunkID)
		}
	}
	return out
}

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

func (h *Handler) runPostResponseTasks(
	ctx context.Context,
	userMessage, aiResponse, contextText, kbID, lang, aiMsgID string,
	sources []ChatSource,
	emit func(map[string]any),
) ([]string, *MessageVerification, string) {
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
					if !c.Verified {
						hasCitationSuspect = true
						break
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

// enqueueRAGASSample serializes the chat context into a RAGASSamplePayload
// and enqueues an asynq task on QueueBatch. Failures are logged at warn
// level and dropped — sampling is a best-effort statistical signal; we
// never want a Redis hiccup to surface as a user-visible error.
func (h *Handler) enqueueRAGASSample(
	ctx context.Context,
	kbID, lang, msgID, userMessage, aiResponse string,
	sources []ChatSource,
) {
	chunks := make([]jobs.RAGASSampleChunk, 0, len(sources))
	for _, s := range sources {
		chunks = append(chunks, jobs.RAGASSampleChunk{
			FileID:  s.FileID,
			Score:   s.Score,
			Content: s.Content,
		})
	}
	payload := jobs.RAGASSamplePayload{
		KbID:      kbID,
		Language:  lang,
		Question:  userMessage,
		Answer:    aiResponse,
		Chunks:    chunks,
		MessageID: msgID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		logctx.From(ctx).Warn("ragas_sample: marshal failed", "error", err)
		return
	}
	if _, err := h.asynqClient.Enqueue(
		asynq.NewTask(jobs.TypeRAGASSample, body),
		asynq.Queue(jobs.QueueBatch),
		asynq.MaxRetry(1),
	); err != nil {
		logctx.From(ctx).Warn("ragas_sample: enqueue failed", "error", err)
	}
}

// runLongmemExtract is the AP-D1 write path's worker goroutine.
// Calls ai.ExtractSalientFacts on the (user message, assistant
// answer) pair, filters out facts below chat_longmem_min_salience,
// then inserts each survivor via the longmem store. Best-effort:
// LLM failures + DB failures both log and drop. Never propagates.
//
// Runs inline in the post-response wg goroutine (NOT detached),
// because we want the recall path of the NEXT turn to see today's
// facts. Detaching to a background goroutine would race the next
// chat request from the same user.
func (h *Handler) runLongmemExtract(ctx context.Context, userID, kbID, lang, userMessage, aiResponse string) {
	if h.longmemStore == nil {
		return
	}
	model := ChatLongmemExtractionModel(ctx, h.siteConfigReader)
	threshold := ChatLongmemMinSalience(ctx, h.siteConfigReader)
	// T1-2: when ANN recall is enabled, embed each fact's content so a
	// later RecallSemantic can find it. The embedding model is the same
	// one used for chunk + query embeddings (ai.GenerateEmbedding). When
	// the flag is off we pass nil and longmem stores the zero-vector
	// placeholder — back-compat with the v1 recall path.
	embedFacts := ChatLongmemRecallSemantic(ctx, h.siteConfigReader)
	// T1-3: conflict resolution depends on having embeddings to find
	// nearest candidates, so it's only applied when both flags are on.
	conflictResolution := embedFacts && ChatLongmemConflictResolution(ctx, h.siteConfigReader)
	conflictModel := ""
	conflictCandidatePoolSize := 0
	if conflictResolution {
		conflictModel = ChatLongmemConflictModel(ctx, h.siteConfigReader)
		conflictCandidatePoolSize = ChatLongmemConflictCandidates(ctx, h.siteConfigReader)
	}

	facts, err := ai.ExtractSalientFacts(ctx, h.aiResolver, userMessage, aiResponse, kbID, lang, model)
	if err != nil {
		// extractor already incremented the error counter; log
		// here so operators can correlate with their LLM provider.
		logctx.From(ctx).Warn("longmem: extract failed; no facts written this turn",
			"user_id", userID, "kb_id", kbID, "error", err)
		return
	}
	for _, f := range facts {
		if f.Salience < threshold {
			observability.RecordLongmemWrite("skip_low_salience")
			continue
		}
		var embedding []float64
		if embedFacts {
			// Document-side embedding (no instruction prefix) — matches
			// how chunks land in the vector store, so RecallSemantic's
			// query-side encoding (with the configured QueryInstruction)
			// asymmetric-aligns against fact content the same way chunk
			// search does. Failure is non-fatal: drop the embedding,
			// fall back to zero-vector. The fact still lands; only the
			// ANN-recall path loses it.
			emb, eerr := ai.GenerateEmbedding(ctx, h.aiResolver, f.Content, kbID, nil)
			if eerr != nil {
				logctx.From(ctx).Warn("longmem: embed-on-insert failed; storing without ANN-recall vector",
					"user_id", userID, "kind", f.Kind, "error", eerr)
			} else {
				embedding = emb
			}
		}

		// T1-3 conflict resolution. Only fires when the flag is on AND
		// we have an embedding to do the nearest-neighbour lookup with.
		// On any failure (lookup, LLM, parse) we degrade to plain
		// Insert — no fact is ever lost to a misbehaving classifier.
		if conflictResolution && len(embedding) > 0 {
			candidates, nerr := h.longmemStore.NearestMemories(ctx, userID, kbID, embedding, conflictCandidatePoolSize)
			if nerr == nil && len(candidates) > 0 {
				verdict, cerr := ai.ClassifyLongmemConflict(ctx,
					h.aiResolver,
					ai.LongmemCandidate{Kind: f.Kind, Content: f.Content},
					toAILongmemCandidates(candidates),
					kbID, lang, conflictModel,
				)
				if cerr != nil {
					logctx.From(ctx).Warn("longmem: conflict classify failed; falling back to plain insert",
						"user_id", userID, "kind", f.Kind, "error", cerr)
				} else {
					switch verdict.Action {
					case ai.LongmemConflictSkipRedundant:
						observability.RecordLongmemWrite("skip_redundant")
						continue
					case ai.LongmemConflictSupersede:
						if ierr := h.longmemStore.InsertWithSupersede(ctx, userID, kbID, f.Kind, f.Content, f.Salience, embedding, verdict.SupersedeIDs); ierr != nil {
							logctx.From(ctx).Warn("longmem: supersede insert failed; falling back to plain insert",
								"user_id", userID, "kind", f.Kind, "error", ierr)
							// fall through to plain Insert below
						} else {
							continue
						}
					case ai.LongmemConflictCreateNew:
						// fall through to plain Insert below
					}
				}
			} else if nerr != nil {
				// Nearest lookup failed (dim mismatch is the common case).
				// Fall back to plain insert — preserves the fact.
				logctx.From(ctx).Warn("longmem: nearest lookup failed; falling back to plain insert",
					"user_id", userID, "kind", f.Kind, "error", nerr)
			}
			// No candidates → no conflict possible → fall through to Insert.
		}

		if ierr := h.longmemStore.Insert(ctx, userID, kbID, f.Kind, f.Content, f.Salience, embedding); ierr != nil {
			logctx.From(ctx).Warn("longmem: insert failed; skipping fact",
				"user_id", userID, "kind", f.Kind, "error", ierr)
			continue
		}
	}
}

// toAILongmemCandidates projects longmem.Memory rows into the
// compact shape the ai conflict classifier accepts. The ID round-trips
// so the verdict's SupersedeIDs reference rows the chat layer can
// then pass back to InsertWithSupersede.
func toAILongmemCandidates(mems []longmem.Memory) []ai.LongmemCandidate {
	out := make([]ai.LongmemCandidate, len(mems))
	for i, m := range mems {
		out[i] = ai.LongmemCandidate{ID: m.ID, Kind: m.Kind, Content: m.Content}
	}
	return out
}

// recordAgentDecision logs one agent_decisions row for the admin metrics
// panel (Phase 1 §1.4). Fire-and-forget in a fresh background context so
// the per-chat insert can outlive the request without touching the user-
// visible response path. Caller passes the latency they actually observed
// for the user-facing wait.
//
// AP-B4: also pulls the per-turn ToolCallRecorder snapshot (set up at
// turn start by the chat handler) and forwards it to the recorder so
// agent_decisions.tool_calls captures which tools the orchestrator
// actually dispatched.
func (h *Handler) recordAgentDecision(ctx context.Context, kbID, mode, outcome string, hops, rounds int, latencyMs int64) {
	if h.decisionRecorder == nil {
		return
	}
	// Snapshot now (in the request goroutine) so the detached insert
	// doesn't race the recorder. By this point the orchestrator is
	// done — no concurrent Record calls remain.
	var toolCalls []ToolCallRecord
	if rec := ToolCallRecorderFromContext(ctx); rec != nil {
		toolCalls = rec.Snapshot()
	}
	safego.GoCtx(ctx, func() {
		// Detached context: the chat handler's request context is
		// canceled the moment SSE finishes, but the analytics insert
		// should keep going for its own duration budget. Trace context
		// is propagated so the insert remains attached to the request's
		// span in the distributed trace.
		bgCtx, cancel := detachedContext(ctx, 5*time.Second)
		defer cancel()
		h.decisionRecorder.Record(bgCtx, kbID, mode, outcome, hops, rounds, int(latencyMs), toolCalls)
	})
}

// agentOutcomeFromEvents inspects buffered orchestrator events to pull out
// the final outcome plus hop / round counts. Returns ("", 0, 0) when the
// trajectory doesn't carry an `answer`-stage event yet (e.g. the path
// failed before emit). Operators should treat that as "the chat ran but
// no decision is recorded for it" rather than coercing it to a default
// bucket; the helper keeps the caller in control.
func agentOutcomeFromEvents(events []map[string]any) (outcome string, hops int, rounds int) {
	for _, e := range events {
		if t, ok := e["agentTrajectory"].(TrajectoryEvent); ok && t.Stage == "answer" {
			outcome = t.Decision
			// Plan-execute uses Step as the round count; agentic uses it
			// as the hop count. The orchestrators only set one of the
			// two histograms (RecordPlanExecuteDecision vs
			// RecordAgenticChatDecision), so the caller chooses which
			// field to populate via the `mode` argument when they call
			// recordAgentDecision.
			hops = t.Step
			rounds = t.Step
		}
	}
	return outcome, hops, rounds
}

// mergeVerification combines factcheck output, citation-validator
// output, the Phase 3 §3.3 factuality-verifier output, and the AP-A1
// refine-gate metadata into the single MessageVerification blob the
// wire and DB expect. Returns nil when no subsystem produced anything
// to surface. Each subsystem runs independently — the verifier may
// produce results without the factchecker; the citation validator may
// fire without either; refine only fires when the verifier flagged
// ≥1 unsupported/contradicted claim AND the gate is on.
func mergeVerification(fc *ai.FactCheckResult, citations []CitationStatus, factualityClaims []ai.FlaggedClaim, refine *RefineStatus, selfRAG *ai.SelfRAGResult) *MessageVerification {
	if fc == nil && len(citations) == 0 && len(factualityClaims) == 0 && refine == nil && selfRAG == nil {
		return nil
	}
	v := &MessageVerification{Issues: []string{}, Citations: citations, Refine: refine}
	if fc != nil {
		v.Verified = fc.Verified
		v.Score = fc.Score
		if fc.Issues != nil {
			v.Issues = fc.Issues
		}
	}
	// AP-D2: when Self-RAG ran, surface its full output and SKIP
	// the legacy Factuality block (the FlaggedClaims live inside
	// SelfRAG.FlaggedClaims). When Self-RAG didn't run, fall back
	// to the legacy Factuality shape so existing UI keeps working.
	if selfRAG != nil {
		v.SelfRAG = &SelfRAGVerification{
			Relevance:     make([]ChunkRelevanceStatus, 0, len(selfRAG.Relevance)),
			FlaggedClaims: make([]FlaggedClaimStatus, 0, len(selfRAG.FlaggedClaims)),
			Usefulness: UsefulnessStatus{
				Verdict: selfRAG.Usefulness.Verdict,
				Reason:  selfRAG.Usefulness.Reason,
			},
		}
		for _, c := range selfRAG.Relevance {
			v.SelfRAG.Relevance = append(v.SelfRAG.Relevance,
				ChunkRelevanceStatus{N: c.N, Verdict: c.Verdict})
		}
		for _, c := range selfRAG.FlaggedClaims {
			v.SelfRAG.FlaggedClaims = append(v.SelfRAG.FlaggedClaims,
				FlaggedClaimStatus{ClaimText: c.ClaimText, Reason: c.Reason, CitedN: c.CitedN})
		}
	} else if len(factualityClaims) > 0 {
		statuses := make([]FlaggedClaimStatus, len(factualityClaims))
		for i, c := range factualityClaims {
			statuses[i] = FlaggedClaimStatus{ClaimText: c.ClaimText, Reason: c.Reason, CitedN: c.CitedN}
		}
		v.Factuality = &FactualityVerification{FlaggedClaims: statuses}
	}
	return v
}
