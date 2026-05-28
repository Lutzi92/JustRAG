package vector

import (
	"slices"
	"strings"
)

// Deduplicate removes chunks that are near-copies of another already-kept
// chunk FROM THE SAME SOURCE FILE. Keeps the higher-scored document when a
// near-duplicate pair is found. threshold is the Jaccard similarity
// threshold (default 0.85). docs must already be sorted by score descending.
//
// Only same-file pairs are considered. Two chunks from different source
// files are independent evidence and must not be collapsed — even when
// they share a templated section (e.g. each project page carries a
// "mentioned persons" summary with identical boilerplate). Collapsing
// those cross-file "duplicates" is what caused the person-to-projects
// query to return only one project.
func Deduplicate(docs []RankedDoc, threshold float64) []RankedDoc {
	kept := make([]RankedDoc, 0, len(docs))
	// Pre-compute a sorted unique-word slice per kept doc so the inner
	// Jaccard comparison runs as a merge-join instead of two map allocations
	// per pair. The hot path (search-time) sees up to ~200 candidates × ~k
	// kept docs, so the per-pair allocation savings compound.
	keptWords := make([][]string, 0, len(docs))

	for _, candidate := range docs {
		var candidateWords []string
		isDuplicate := false
		for i, keptDoc := range kept {
			// Only consider candidates duplicates of each other when they
			// come from the same source file (or when FileID is unknown
			// on either side — fall back to the original content-only
			// behavior to preserve existing callers/tests that never set
			// FileID). Cross-file pairs with known different FileIDs are
			// independent evidence and must not be collapsed.
			if candidate.FileID != "" && keptDoc.FileID != "" && candidate.FileID != keptDoc.FileID {
				continue
			}
			if candidateWords == nil {
				candidateWords = sortedUniqueWords(candidate.Content)
			}
			if jaccardSorted(candidateWords, keptWords[i]) > threshold {
				isDuplicate = true
				break
			}
		}
		if !isDuplicate {
			kept = append(kept, candidate)
			if candidateWords == nil {
				candidateWords = sortedUniqueWords(candidate.Content)
			}
			keptWords = append(keptWords, candidateWords)
		}
	}

	return kept
}

// sortedUniqueWords lowercases text, splits on whitespace, sorts and dedups.
// Returns a slice suitable for jaccardSorted.
func sortedUniqueWords(text string) []string {
	words := strings.Fields(strings.ToLower(text))
	if len(words) == 0 {
		return nil
	}
	slices.Sort(words)
	// In-place dedup of adjacent equals.
	n := 1
	for i := 1; i < len(words); i++ {
		if words[i] != words[i-1] {
			words[n] = words[i]
			n++
		}
	}
	return words[:n]
}

// jaccardSorted computes the Jaccard similarity of two sorted, deduplicated
// word slices via a merge-join. O(m+n) time, zero allocations.
func jaccardSorted(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	intersection := 0
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			intersection++
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
