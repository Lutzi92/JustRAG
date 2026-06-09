package chat

import (
	"context"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/longmem"
	"github.com/justrag/go-backend/internal/observability"
)

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
