package ai

import (
	"strings"
	"unicode"
)

// ComplexityVerdict is the output of HeuristicComplexity.
type ComplexityVerdict int

const (
	// ComplexityUnknown means the heuristic cannot decide — caller should
	// fall back to the LLM classifier.
	ComplexityUnknown ComplexityVerdict = iota
	// ComplexitySimple means the query is obviously simple (short factual
	// lookup) and does not need HyDE / MultiQuery.
	ComplexitySimple
	// ComplexityComplex means the query contains strong complexity signals
	// (comparison, synthesis) and should enable HyDE / MultiQuery without
	// burning an LLM call on classification.
	ComplexityComplex
)

// complexMarkersDE and complexMarkersEN are word-boundary substrings whose
// presence strongly indicates a synthesis/comparison query. Matched on the
// lowercased, space-padded query so e.g. "vergleich" matches "vergleichen".
var (
	complexMarkersDE = []string{
		"vergleich", "unterschied", "gegenüber", "zusammenhang",
		"beziehung", "auswirkung", "vor- und nachteile", "warum",
		"erkläre", "erläutere", "analysiere", "bewerte",
	}
	complexMarkersEN = []string{
		"compare", "comparison", "contrast", "difference",
		"differences", "relationship", "relation between",
		"impact", "effect of", "pros and cons", "why", "how does",
		"explain", "analyze", "analyse", "evaluate",
	}
)

// HeuristicComplexity returns a verdict based on cheap syntactic signals.
// - Empty / very short (<= 6 words) non-question queries → Simple.
// - Contains a comparison/synthesis marker → Complex.
// - Everything else → Unknown (caller should run the LLM classifier).
//
// The heuristic is intentionally conservative: we only short-circuit when we
// are confident. Ambiguous queries still pay for the classifier call.
func HeuristicComplexity(query string) ComplexityVerdict {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return ComplexitySimple
	}

	// Complex markers win: short queries can still be complex (e.g. "why?").
	padded := " " + q + " "
	for _, m := range complexMarkersDE {
		if strings.Contains(padded, " "+m) {
			return ComplexityComplex
		}
	}
	for _, m := range complexMarkersEN {
		if strings.Contains(padded, " "+m) {
			return ComplexityComplex
		}
	}

	// Short, single-clause queries without complex markers are almost always
	// factual lookups ("Kündigungsfrist?", "Was ist §323c StGB?").
	wordCount := 0
	inWord := false
	for _, r := range q {
		if unicode.IsSpace(r) {
			inWord = false
			continue
		}
		if !inWord {
			wordCount++
			inWord = true
		}
	}

	// No conjunction = single clause. "and"/"und"/"oder"/"or" can connect
	// multiple concepts worth synthesizing.
	hasConjunction := strings.Contains(padded, " and ") ||
		strings.Contains(padded, " und ") ||
		strings.Contains(padded, " or ") ||
		strings.Contains(padded, " oder ")

	if wordCount <= 6 && !hasConjunction {
		return ComplexitySimple
	}

	return ComplexityUnknown
}
