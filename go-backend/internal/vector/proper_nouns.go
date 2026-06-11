package vector

import (
	"strings"
	"unicode"
)

// interrogativeStopwords are sentence-leading German + English question words
// that would otherwise appear capitalized at the start of a query and cause
// false-positive proper-noun phrases ("Welche Rolle ..." would otherwise
// extract "Welche Rolle" since both are title-cased in German). These are
// dropped from the extraction stream; if a real proper noun follows another
// stopword, the second sequence still gets picked up.
var interrogativeStopwords = map[string]bool{
	// German
	"Welche": true, "Welcher": true, "Welches": true, "Welchem": true, "Welchen": true,
	"Wer": true, "Wen": true, "Wem": true, "Wessen": true,
	"Was": true, "Wo": true, "Wohin": true, "Woher": true,
	"Wann": true, "Warum": true, "Wieso": true, "Weshalb": true, "Wofür": true, "Wofur": true,
	"Wie": true,
	// English
	"Which": true, "What": true, "Where": true, "When": true,
	"Who": true, "Whom": true, "Whose": true, "Why": true, "How": true,
}

// extractProperNounPhrases scans `query` for runs of two or more consecutive
// title-cased tokens (each starting with an uppercase letter, length ≥ 2)
// that are NOT interrogative stopwords, and returns each run joined with a
// single space. Sentence-ending and list-separating punctuation (".?!,;:")
// terminates a run; whitespace within a run is preserved.
//
// The intent is to surface multi-token proper-noun phrases ("Eberhard Kurz",
// "Marcus Enger") so the keyword-search SQL can OR an additional
// phraseto_tsquery against them — the production websearch_to_tsquery
// otherwise ANDs the query verb stems together with the name stems and zero
// chunks match for entity-role lookups whose verbs don't co-occur with the
// entity in the source documents.
//
// Conservative on purpose:
//   - Single-word title-cased tokens are NOT extracted (too noisy on German
//     corpora where every common noun is capitalized).
//   - Punctuation breaks runs, so "Eberhard, du bist..." does not extract.
//   - Lowercase particles ("von", "van", "de") break runs — known limitation;
//     "Otto von Bismarck" -> nothing extracted. Acceptable miss for this KB.
func extractProperNounPhrases(query string) []string {
	var out []string
	var current []string
	flush := func() {
		if len(current) >= 2 {
			out = append(out, strings.Join(current, " "))
		}
		current = nil
	}

	for _, raw := range strings.Fields(query) {
		clean, breakAfter := trimTokenPunct(raw)
		if isProperNounToken(clean) && !interrogativeStopwords[clean] {
			current = append(current, clean)
		} else {
			flush()
		}
		if breakAfter {
			flush()
		}
	}
	flush()
	return out
}

// trimTokenPunct strips leading and trailing punctuation from a whitespace-
// separated token. It returns the cleaned token and a flag indicating whether
// the trailing punctuation was a phrase-breaking character (.,;:!? and
// closing brackets / quotes) — break-after means the run ends at this token
// even if subsequent tokens are title-cased (different sentence / list item).
func trimTokenPunct(s string) (clean string, breakAfter bool) {
	clean = s
	for len(clean) > 0 {
		// Inspect last byte cheaply — all break-after punct is ASCII.
		last := clean[len(clean)-1]
		switch last {
		case '.', ',', ';', ':', '!', '?', ')', ']', '}', '"', '\'':
			breakAfter = true
			clean = clean[:len(clean)-1]
			continue
		}
		break
	}
	for len(clean) > 0 {
		first := clean[0]
		switch first {
		case '(', '[', '{', '"', '\'':
			clean = clean[1:]
			continue
		}
		break
	}
	return clean, breakAfter
}

// isProperNounToken returns true when s is at least 2 runes long and starts
// with an uppercase letter. Hyphens and digits inside the token are
// allowed ("Digital-PPM" qualifies as a single title-cased token; whether
// it forms a phrase depends on its neighbours).
func isProperNounToken(s string) bool {
	runes := []rune(s)
	if len(runes) < 2 {
		return false
	}
	return unicode.IsUpper(runes[0])
}

// entityQueryPrefixes are first-token German + English interrogatives that
// strongly indicate an entity-role lookup ("who is X").
var entityQueryPrefixes = []string{
	"wer ", "wem ", "wen ", "wessen ",
	"who ", "whom ", "whose ",
}

// entityQueryPatterns are multi-token role/function phrases that can appear
// anywhere in the query.
var entityQueryPatterns = []string{
	"welche rolle", "welche funktion", "welche aufgabe",
	"welche position", "welche stellung", "in welcher rolle",
	"what role", "what function", "what position",
}

// isEntityAskingQuery returns true when the query is asking about an entity's
// identity / role / function. Used to gate the proper-noun phrase fallback
// in runKeywordSearch: broadening BM25 with phraseto_tsquery for every
// adjacent capitalized-word pair was too aggressive on German, where common
// nouns are capitalized too ("Künstliche Intelligenz", "Best Practices",
// "Digital Portfolio"). This gate restricts the broadening to queries whose
// shape matches the failure mode it was designed to fix.
func isEntityAskingQuery(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return false
	}
	for _, pfx := range entityQueryPrefixes {
		if strings.HasPrefix(q, pfx) {
			return true
		}
	}
	for _, pat := range entityQueryPatterns {
		if strings.Contains(q, pat) {
			return true
		}
	}
	return false
}
