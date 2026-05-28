package vector

import "math"

// crossSourceContentWeight dampens content-Jaccard when two candidates come
// from DIFFERENT source files. Templated chunks that legitimately represent
// different documents (e.g. the synthetic "persons mentioned on page X"
// section emitted during Confluence ingestion) score high on Jaccard even
// though they're genuinely independent answers. Under pure content-only
// MMR those get incorrectly collapsed and queries like "what projects is
// Alice on?" return partial lists.
//
// Intuition: "redundant chunk" = "same source AND same words". Different
// source files are treated as mostly non-redundant regardless of vocabulary.
const crossSourceContentWeight = 0.2

// MMR diversity proxy: same-file chunks use the full Jaccard word overlap
// so within-document diversity still works; cross-file chunks use a
// dampened overlap (crossSourceContentWeight) so templated cross-document
// redundancy no longer crushes relevance ranking. Inlined into ApplyMMR
// below so the pre-tokenized word slices can be reused.

// ApplyMMR re-ranks docs using Maximal Marginal Relevance to balance
// relevance against diversity. Returns up to k selected docs in MMR order.
//
// Formula: MMR(d) = λ·rel(d) − (1−λ)·max{sim(d, s) : s ∈ Selected}
//   - rel(d) = doc.Score (the existing fused score)
//   - sim(d, s) = chunkSimilarity — Jaccard on content for same-file pairs,
//     sharply reduced Jaccard for cross-file pairs (see crossSourceContentWeight).
//
// λ is clamped to [0.0, 1.0]. λ=1.0 short-circuits to the input order
// (pure relevance, MMR is a no-op). λ=0.0 picks for maximum diversity
// after the first (highest-score) doc is seeded.
//
// docs must be sorted by Score descending. Caller is responsible for that.
func ApplyMMR(docs []RankedDoc, lambda float64, k int) []RankedDoc {
	if k <= 0 {
		if docs == nil {
			return nil
		}
		return docs[:0]
	}
	if len(docs) == 0 {
		return docs
	}
	if k > len(docs) {
		k = len(docs)
	}

	if lambda < 0 {
		lambda = 0
	}
	if lambda > 1 {
		lambda = 1
	}

	if lambda >= 1.0 {
		out := make([]RankedDoc, k)
		copy(out, docs[:k])
		return out
	}

	selected := make([]RankedDoc, 0, k)
	remaining := make([]RankedDoc, len(docs))
	copy(remaining, docs)

	// Pre-compute sorted unique-word slices once per doc — the selection
	// loop revisits each remaining candidate up to k times, so without
	// memoization the inner Jaccard work would re-tokenize+resort the same
	// content O(k·n) times. The slices stay aligned with `remaining` via
	// the same swap-remove operations applied below.
	remainingWords := make([][]string, len(remaining))
	for i, d := range remaining {
		remainingWords[i] = sortedUniqueWords(d.Content)
	}

	// maxSim[i] tracks the running max similarity between remaining[i] and
	// any already-selected doc. Updated incrementally on every pick: instead
	// of recomputing the full O(k) similarity-to-selected scan for every
	// candidate on every iteration (O(k²·n) total), we compute O(n) new
	// similarities to the just-picked doc and fold them into maxSim. Brings
	// the loop down to O(k·n) total Jaccard calls.
	maxSim := make([]float64, len(remaining))

	pick := func(idx int) {
		selected = append(selected, remaining[idx])
		pickedWords := remainingWords[idx]
		pickedFileID := remaining[idx].FileID

		// Swap-remove the picked entry from `remaining`, `remainingWords`,
		// and `maxSim` — three parallel slices. Order is irrelevant because
		// every iteration re-evaluates each candidate.
		last := len(remaining) - 1
		remaining[idx] = remaining[last]
		remaining = remaining[:last]
		remainingWords[idx] = remainingWords[last]
		remainingWords = remainingWords[:last]
		maxSim[idx] = maxSim[last]
		maxSim = maxSim[:last]

		// Fold the just-picked doc's similarity into every remaining
		// candidate's running max. Cross-file pairs get the dampened
		// Jaccard (see crossSourceContentWeight).
		for i := range remaining {
			js := jaccardSorted(remainingWords[i], pickedWords)
			sim := js
			if !(remaining[i].FileID == "" || pickedFileID == "" || remaining[i].FileID == pickedFileID) {
				sim = js * crossSourceContentWeight
			}
			if sim > maxSim[i] {
				maxSim[i] = sim
			}
		}
	}

	pick(0)

	for len(selected) < k && len(remaining) > 0 {
		bestIdx := -1
		bestScore := math.Inf(-1)
		for i, candidate := range remaining {
			score := lambda*candidate.Score - (1-lambda)*maxSim[i]
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			break
		}
		pick(bestIdx)
	}
	return selected
}
