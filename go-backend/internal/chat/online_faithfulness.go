package chat

import (
	"github.com/justrag/go-backend/internal/ai"
)

// computeFaithfulnessScore is the AP-E2 single-number health
// indicator per turn. Subtractive starting from 1.0, with two
// independent penalty axes:
//
//   - Per refine-eligible flagged claim (unsupported /
//     contradicted): -0.2. Five flags drive the score to 0;
//     out_of_scope claims do NOT penalise (operator-policy
//     class, not a faithfulness concern).
//   - Self-RAG ISUSE = "no": -0.5 (the answer doesn't address
//     the question — biggest faithfulness failure mode).
//   - Self-RAG ISUSE = "partial": -0.2 (mid-tier penalty).
//
// Result is clamped to [0, 1]. NOT the strict Ragas definition —
// see the metric help text. Both verifier flavours feed the same
// scoring path so dashboards can graph them as one series:
// switch from factuality verifier to Self-RAG without trend
// discontinuity.
func computeFaithfulnessScore(flagged []ai.FlaggedClaim, selfRAG *ai.SelfRAGResult) float64 {
	score := 1.0

	// Count refine-eligible flags. ai.ClaimsTriggeringRefine
	// already applies the unsupported/contradicted filter; reuse
	// it so the metric and the refine gate agree on what counts.
	refineEligible := ai.ClaimsTriggeringRefine(flagged)
	score -= 0.2 * float64(len(refineEligible))

	if selfRAG != nil {
		switch selfRAG.Usefulness.Verdict {
		case "no":
			score -= 0.5
		case "partial":
			score -= 0.2
		}
	}

	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return score
}
