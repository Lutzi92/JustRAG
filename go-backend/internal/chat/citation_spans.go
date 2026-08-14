package chat

// CitationSpan is one citation marker found in an answer: the byte range the
// marker occupies and the 1-based source numbers it references.
//
// Start/End are byte offsets, so answer[Start:End] is exactly the marker text
// ("[3]", "[1, 2, 5]"). Callers that need character offsets — the
// OpenAI-compatible surface, whose `annotations[].index` is defined in
// characters — must convert; byte and rune offsets diverge on the first
// umlaut.
type CitationSpan struct {
	Start   int
	End     int
	Numbers []int
}

// ExtractCitationSpans finds every citation marker in answer, in document
// order. Markers whose numbers are all unparseable or non-positive are
// skipped. Returns nil when the answer carries no citations.
//
// This is the single definition of the marker format: the citation validator
// and the OpenAI-compatible annotation builder both read markers through it,
// so the two can never drift apart on what counts as a citation.
func ExtractCitationSpans(answer string) []CitationSpan {
	matches := citationMarkerRe.FindAllStringSubmatchIndex(answer, -1)
	if len(matches) == 0 {
		return nil
	}

	spans := make([]CitationSpan, 0, len(matches))
	for _, m := range matches {
		// m[0]/m[1] = full marker bounds; m[2]/m[3] = inner number group.
		nums := parseCitationNumbers(answer[m[2]:m[3]])
		if len(nums) == 0 {
			continue
		}
		spans = append(spans, CitationSpan{Start: m[0], End: m[1], Numbers: nums})
	}
	if len(spans) == 0 {
		return nil
	}
	return spans
}
