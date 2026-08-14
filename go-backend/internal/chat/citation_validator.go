package chat

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CitationStatus is the per-citation verdict produced by ValidateCitations.
// Verified=true means at least one cited source met the verification check.
// Method records which check passed; Reason explains a non-verified result.
type CitationStatus struct {
	// N is the 1-based citation number as it appears in the answer text
	// (e.g. 3 for "[3]"). For multi-cite markers like "[1, 2]" each N gets
	// its own status entry.
	N int `json:"n"`
	// Verified is true when at least one verification tier (n-gram or, when
	// the orchestrator runs it, semantic similarity) passed for this
	// citation.
	Verified bool `json:"verified"`
	// Method records HOW the citation was verified when Verified=true.
	//   "ngram"    — content-token n-gram overlap (default fast path)
	//   "semantic" — cosine similarity of sentence window vs source chunk
	//                (>= site_config citation_validation_semantic_threshold)
	//   ""         — Verified=false (see Reason)
	Method string `json:"method,omitempty"`
	// Reason explains a non-verified result. Stable string keys for
	// frontend tooltips and metric labels:
	//   "out_of_range"  — citation N exceeds len(sources)
	//   "no_overlap"    — chunk(s) loaded but no n-gram match (and the
	//                     semantic fallback either was disabled or did not
	//                     pass threshold)
	Reason string `json:"reason,omitempty"`
}

// citationMarkerRe captures both single ([3]) and multi-cite ([1, 2, 5])
// markers. Numbers are captured as one comma-separated group; downstream
// splits on the comma.
var citationMarkerRe = regexp.MustCompile(`\[(\d+(?:\s*,\s*\d+)*)\]`)

// stopwords deliberately covers both DE and EN — the validator must work on
// any KB language without per-call language detection. Function words have
// near-zero discriminative power in n-grams; keeping them in the set would
// produce false positives like "in der" / "in the" matching anywhere.
//
// Trade-off: KBs in other languages (FR, ES, IT, …) run through the same
// validator but retain their own function words as n-gram tokens. Those
// tokens produce spurious overlap hits, so the validator is less effective
// at flagging unsupported citations in non-EN/DE answers. This is
// intentional — per-call language detection would add cost for a feature
// that is itself a cheap post-hoc check. Extend this map with additional
// languages when a deployment warrants it.
var stopwords = map[string]struct{}{
	// English
	"the": {}, "a": {}, "an": {}, "and": {}, "or": {}, "but": {}, "of": {},
	"in": {}, "on": {}, "at": {}, "to": {}, "for": {}, "with": {}, "by": {},
	"is": {}, "are": {}, "was": {}, "were": {}, "be": {}, "been": {}, "being": {},
	"have": {}, "has": {}, "had": {}, "do": {}, "does": {}, "did": {},
	"this": {}, "that": {}, "these": {}, "those": {}, "it": {}, "its": {},
	"from": {}, "as": {}, "if": {}, "then": {}, "than": {}, "so": {},
	"not": {}, "no": {}, "yes": {}, "also": {}, "such": {},
	// German (entries already in the EN list — "an", "in" — are not repeated)
	"der": {}, "die": {}, "das": {}, "den": {}, "dem": {}, "des": {},
	"ein": {}, "eine": {}, "einer": {}, "eines": {}, "einen": {}, "einem": {},
	"und": {}, "oder": {}, "aber": {}, "von": {}, "zu": {}, "bei": {}, "mit": {},
	"ist": {}, "sind": {}, "war": {}, "waren": {}, "sein": {}, "gewesen": {},
	"hat": {}, "haben": {}, "hatte": {}, "hatten": {},
	"dies": {}, "diese": {}, "dieser": {}, "dieses": {},
	"für": {}, "auf": {}, "im": {},
	"nicht": {}, "kein": {}, "keine": {}, "auch": {},
}

// tokenSplitRe splits on any non-letter/digit run. Works fine for German
// umlauts because Go's regexp \p{L} class includes them.
var tokenSplitRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// tokenize lowercases s and returns its content tokens with stopwords
// removed. Tokens shorter than 3 runes are dropped — they have low
// discriminative power and amplify false positives.
func tokenize(s string) []string {
	s = strings.ToLower(s)
	parts := tokenSplitRe.Split(s, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		if _, drop := stopwords[p]; drop {
			continue
		}
		// Rune count, not byte length — German tokens often need this.
		if utf8.RuneCountInString(p) < 3 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ngramSet returns the set of consecutive n-grams of tokens, joined by '\x00'
// so the key can never collide with any real text.
func ngramSet(tokens []string, n int) map[string]struct{} {
	if n <= 0 || len(tokens) < n {
		return nil
	}
	out := make(map[string]struct{}, len(tokens)-n+1)
	for i := 0; i+n <= len(tokens); i++ {
		out[strings.Join(tokens[i:i+n], "\x00")] = struct{}{}
	}
	return out
}

// hasOverlap reports whether the two n-gram sets share at least one entry.
func hasOverlap(a, b map[string]struct{}) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	// Iterate the smaller side for cheaper lookups.
	if len(a) > len(b) {
		a, b = b, a
	}
	for k := range a {
		if _, ok := b[k]; ok {
			return true
		}
	}
	return false
}

// sentenceWithMarker returns answer trimmed to the sentence containing the
// rune offset markerStart, plus one neighbour sentence on each side. The
// goal is "the local context the user reads next to [N]" — broader than
// one sentence (a citation often closes a sentence about a fact stated in
// the previous one), narrower than the whole paragraph (which would let
// every sentence with a real fact validate every citation).
//
// Sentence boundaries are heuristic: a period, exclamation, or question
// mark followed by whitespace. Good enough for chat answers; we don't
// need linguistic correctness — only locality.
func sentenceWithMarker(answer string, markerStart int) string {
	if markerStart < 0 || markerStart >= len(answer) {
		return answer
	}

	// Walk left for sentence starts, collecting up to 2 boundaries.
	leftBoundaries := findSentenceBoundariesBefore(answer, markerStart, 2)
	start := 0
	if len(leftBoundaries) > 0 {
		start = leftBoundaries[len(leftBoundaries)-1]
	}

	// Walk right for sentence ends, up to 2 boundaries.
	rightBoundaries := findSentenceBoundariesAfter(answer, markerStart, 2)
	end := len(answer)
	if len(rightBoundaries) > 0 {
		end = rightBoundaries[len(rightBoundaries)-1]
	}

	return answer[start:end]
}

// findSentenceBoundariesBefore returns up to maxN positions where a sentence
// starts (i.e. one byte after a sentence-end punctuation), walking backward
// from offset. The returned slice is in reverse-encounter order: the first
// element is the closest boundary, the last is the furthest.
func findSentenceBoundariesBefore(s string, offset, maxN int) []int {
	out := make([]int, 0, maxN)
	i := offset - 1
	for i > 0 && len(out) < maxN {
		if isSentenceEnd(s[i]) && i+1 < len(s) && isSpace(s[i+1]) {
			out = append(out, i+2) // start of next sentence
		}
		i--
	}
	return out
}

// findSentenceBoundariesAfter returns up to maxN positions just after a
// sentence-end punctuation, walking forward from offset.
func findSentenceBoundariesAfter(s string, offset, maxN int) []int {
	out := make([]int, 0, maxN)
	i := offset
	for i < len(s) && len(out) < maxN {
		if isSentenceEnd(s[i]) {
			// Include the punctuation in the slice so "fact." stays intact.
			end := i + 1
			out = append(out, end)
		}
		i++
	}
	return out
}

func isSentenceEnd(b byte) bool { return b == '.' || b == '!' || b == '?' }
func isSpace(b byte) bool       { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// ValidateCitations parses the answer for [N] and [N, M] citation markers
// and checks each cited source for n-gram overlap with the local sentence
// window around the marker.
//
// Algorithm per marker:
//  1. Determine the local window (sentence containing the marker ±1).
//  2. Tokenize the window (lowercase, stopword-filtered, len ≥ 3).
//  3. For each cited N:
//     a. If N > len(sources): out_of_range, verified=false.
//     b. Tokenize the cited source content.
//     c. 4-gram overlap check; on miss, fall back to 3-gram.
//     d. Multi-cite [1, 2]: any source overlap → both Ns are marked verified.
//
// Determinism: same answer + same sources → same result, always. No I/O,
// no LLM, no randomness.
//
// Returns nil when the answer has no citation markers — the caller should
// treat that as "nothing to validate", not as an error.
func ValidateCitations(answer string, sources []ChatSource) []CitationStatus {
	spans := ExtractCitationSpans(answer)
	if len(spans) == 0 {
		return nil
	}

	type pendingMarker struct {
		ns           []int // 1-based citation numbers in this marker
		windowTokens []string
	}

	pending := make([]pendingMarker, 0, len(spans))
	for _, s := range spans {
		window := sentenceWithMarker(answer, s.Start)
		pending = append(pending, pendingMarker{
			ns:           s.Numbers,
			windowTokens: tokenize(window),
		})
	}

	// Cache per-source n-gram sets so we don't re-tokenize the same source
	// for every citation pointing at it.
	type srcGrams struct {
		four, three map[string]struct{}
	}
	srcCache := make(map[int]srcGrams, len(sources))

	getGrams := func(n int) srcGrams {
		if g, ok := srcCache[n]; ok {
			return g
		}
		idx := n - 1 // citations are 1-based
		if idx < 0 || idx >= len(sources) {
			srcCache[n] = srcGrams{}
			return srcCache[n]
		}
		// Phase F: for RAPTOR summary citations, union the
		// summary's own prose with every descendant leaf body
		// before tokenising. The summary is paraphrased, so its
		// n-grams often don't match the answer text directly; the
		// descendant leaves typically do. The caller
		// (runPostResponseTasks) populates DescendantContents via
		// GetRaptorDescendantLeafContents before invoking the
		// validator. When the field is empty (non-summary, or
		// resolver failed open) the behaviour is unchanged from
		// the legacy leaf-only path.
		sourceText := sources[idx].Content
		if sources[idx].NodeKind == "summary" && len(sources[idx].DescendantContents) > 0 {
			var sb strings.Builder
			sb.WriteString(sources[idx].Content)
			for _, d := range sources[idx].DescendantContents {
				sb.WriteByte('\n')
				sb.WriteString(d)
			}
			sourceText = sb.String()
		}
		toks := tokenize(sourceText)
		g := srcGrams{
			four:  ngramSet(toks, 4),
			three: ngramSet(toks, 3),
		}
		srcCache[n] = g
		return g
	}

	// Aggregate: a single N can be verified by any of its appearances. We
	// emit one status per UNIQUE N so the frontend doesn't render duplicate
	// markers for "[3]" mentioned three times in one answer. Multi-cite
	// "[1, 2]" → two entries, one per N.
	verified := make(map[int]bool)
	reason := make(map[int]string)
	order := make([]int, 0, len(pending))

	addStatus := func(n int, ok bool, why string) {
		if _, seen := reason[n]; !seen {
			order = append(order, n)
		}
		// Once verified, stay verified — a single successful occurrence
		// is enough.
		if !verified[n] {
			verified[n] = ok
			if ok {
				reason[n] = ""
			} else {
				// Keep the most informative reason. out_of_range is
				// terminal; no_overlap may flip to verified later.
				if existing, hasReason := reason[n]; !hasReason || existing == "" {
					reason[n] = why
				}
			}
		}
	}

	for _, p := range pending {
		// Out-of-range Ns short-circuit: no chunk to compare against.
		validNs := p.ns[:0]
		for _, n := range p.ns {
			if n < 1 || n > len(sources) {
				addStatus(n, false, "out_of_range")
				continue
			}
			validNs = append(validNs, n)
		}
		if len(validNs) == 0 {
			continue
		}

		// Multi-cite "any source overlap" semantics: try 4-gram across all
		// cited sources first, fall back to 3-gram, only then mark each N.
		windowFour := ngramSet(p.windowTokens, 4)
		windowThree := ngramSet(p.windowTokens, 3)

		anyOverlap := false
		for _, n := range validNs {
			g := getGrams(n)
			if hasOverlap(windowFour, g.four) || hasOverlap(windowThree, g.three) {
				anyOverlap = true
				break
			}
		}

		for _, n := range validNs {
			if anyOverlap {
				addStatus(n, true, "")
			} else {
				addStatus(n, false, "no_overlap")
			}
		}
	}

	out := make([]CitationStatus, 0, len(order))
	for _, n := range order {
		method := ""
		if verified[n] {
			method = "ngram"
		}
		out = append(out, CitationStatus{
			N:        n,
			Verified: verified[n],
			Method:   method,
			Reason:   reason[n],
		})
	}
	return out
}

// parseCitationNumbers splits "1, 2, 5" into [1, 2, 5]. Garbage entries
// are skipped silently — the regex already constrained input to digits and
// commas.
func parseCitationNumbers(s string) []int {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n <= 0 {
			continue
		}
		out = append(out, n)
	}
	return out
}
