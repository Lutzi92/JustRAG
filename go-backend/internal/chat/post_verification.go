package chat

import (
	"github.com/justrag/go-backend/internal/ai"
)

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
