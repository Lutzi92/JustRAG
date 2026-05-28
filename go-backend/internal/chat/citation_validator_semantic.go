package chat

import (
	"context"
	"math"
)

// SemanticConfig configures the semantic-similarity fallback for
// RunCitationValidation. When set, RunCitationValidation runs the
// deterministic n-gram pass first; for any citation marked Reason="no_overlap"
// it then embeds the local sentence window and the cited source chunk via
// Embed and checks cosine similarity against Threshold.
//
// nil = no fallback; only the n-gram pass runs.
type SemanticConfig struct {
	// Embed produces an embedding for the given text. Typically bound to
	// ai.GenerateEmbedding(ctx, resolver, text, kbID) — but a unit-test
	// double can supply deterministic vectors for free. Note the answer
	// window is NOT a query, so do NOT use ai.EncodeQuery (which prepends
	// the asymmetric Qwen3 instruction prefix); use the symmetric path.
	Embed func(ctx context.Context, text string) ([]float64, error)
	// Threshold is the cosine-similarity floor for flipping a no_overlap
	// citation to Verified=true, Method="semantic". Typical value 0.85.
	Threshold float64
}

// RunCitationValidation orchestrates the full citation-validation pipeline.
// Pure n-gram pass first (always); semantic fallback second (when sem != nil).
//
// Failure modes are fail-open: any embedding error, zero-magnitude vector,
// or below-threshold similarity preserves the n-gram verdict. Out-of-range
// citations are never retried — there's no source to compare against.
//
// Returns the same []CitationStatus shape as ValidateCitations; the only
// difference is that some entries previously marked Reason="no_overlap"
// may now be Verified=true with Method="semantic".
func RunCitationValidation(
	ctx context.Context,
	answer string,
	sources []ChatSource,
	sem *SemanticConfig,
) []CitationStatus {
	statuses := ValidateCitations(answer, sources)
	if sem == nil || sem.Embed == nil || sem.Threshold <= 0 || len(statuses) == 0 {
		return statuses
	}

	// Build N -> first-seen window. The validator dedupes by N; we mirror
	// that here so each citation is checked once even if [N] appears
	// multiple times.
	windows := windowsByN(answer)

	// Per-call cache of source-chunk embeddings. A single answer often
	// cites the same source from multiple sentences; embed once.
	srcCache := make(map[int][]float64, len(sources))
	getSrcVec := func(n int) ([]float64, bool) {
		if v, ok := srcCache[n]; ok {
			return v, len(v) > 0
		}
		idx := n - 1
		if idx < 0 || idx >= len(sources) {
			srcCache[n] = nil
			return nil, false
		}
		content := sources[idx].Content
		if content == "" {
			srcCache[n] = nil
			return nil, false
		}
		v, err := sem.Embed(ctx, content)
		if err != nil || len(v) == 0 {
			srcCache[n] = nil
			return nil, false
		}
		srcCache[n] = v
		return v, true
	}

	// Per-call cache of window embeddings — markers that share a sentence
	// (e.g. "Both X [1] and Y [2] are true.") share a window string.
	winCache := make(map[string][]float64)
	getWinVec := func(text string) ([]float64, bool) {
		if v, ok := winCache[text]; ok {
			return v, len(v) > 0
		}
		v, err := sem.Embed(ctx, text)
		if err != nil || len(v) == 0 {
			winCache[text] = nil
			return nil, false
		}
		winCache[text] = v
		return v, true
	}

	for i, s := range statuses {
		// Only retry the no_overlap case. out_of_range has no source;
		// already-verified entries don't need a second opinion.
		if s.Verified || s.Reason != "no_overlap" {
			continue
		}
		win, ok := windows[s.N]
		if !ok {
			continue
		}
		srcVec, ok := getSrcVec(s.N)
		if !ok {
			continue
		}
		winVec, ok := getWinVec(win)
		if !ok {
			continue
		}
		sim := cosineSimilarity(winVec, srcVec)
		if sim >= sem.Threshold {
			statuses[i].Verified = true
			statuses[i].Method = "semantic"
			statuses[i].Reason = ""
		}
	}
	return statuses
}

// windowsByN re-parses answer the same way ValidateCitations does and
// returns a map of citation-number -> first-seen sentence window. Used by
// the semantic fallback to recover the per-marker window without changing
// the existing pure validator's API.
func windowsByN(answer string) map[int]string {
	matches := citationMarkerRe.FindAllStringSubmatchIndex(answer, -1)
	out := make(map[int]string, len(matches))
	for _, m := range matches {
		nums := parseCitationNumbers(answer[m[2]:m[3]])
		if len(nums) == 0 {
			continue
		}
		win := sentenceWithMarker(answer, m[0])
		for _, n := range nums {
			if _, exists := out[n]; !exists {
				out[n] = win
			}
		}
	}
	return out
}

// citationWindows returns the deduped list of sentence windows in answer
// (one per marker, in order of appearance). Exposed for tests that need
// to align fake-embedding inputs with the windows the validator computes.
func citationWindows(answer string) []string {
	matches := citationMarkerRe.FindAllStringSubmatchIndex(answer, -1)
	out := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		win := sentenceWithMarker(answer, m[0])
		if _, dup := seen[win]; dup {
			continue
		}
		seen[win] = struct{}{}
		out = append(out, win)
	}
	return out
}

// cosineSimilarity returns the cosine similarity of two vectors, or 0
// when either is empty, mismatched in length, or has zero magnitude.
// Zero return on edge cases is intentional: it cannot pass any positive
// threshold, so a degenerate input never falsely flips a citation to
// verified.
func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
