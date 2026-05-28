package vector

import (
	"strings"
	"unicode"
)

// quoteOpeners and quoteClosers are the rune sets recognised as unambiguous
// phrase delimiters. ASCII `'` is handled separately by the apostrophe-aware
// heuristic (see scanAsciiSingleQuote).
//
// Any opener can be paired with any closer — this avoids guessing the user's
// keyboard layout (German "„…"" vs English "'…'" etc.).
var quoteOpeners = map[rune]struct{}{
	'‘': {}, // ' LEFT SINGLE QUOTATION MARK
	'’': {}, // ' RIGHT SINGLE QUOTATION MARK (also used as apostrophe-style closer)
	'‚': {}, // ‚ SINGLE LOW-9 QUOTATION MARK
	'“': {}, // " LEFT DOUBLE QUOTATION MARK
	'”': {}, // " RIGHT DOUBLE QUOTATION MARK
	'„': {}, // „ DOUBLE LOW-9 QUOTATION MARK (German opening double)
}

func isQuoteCloser(r rune) bool {
	_, ok := quoteOpeners[r]
	return ok || r == '"'
}

// extractQuotedPhrases scans query for substrings wrapped in any recognised
// quote pair, returning the phrases (without the quote runes) and the
// remainder (whitespace-collapsed, trimmed).
//
// Recognised pairs:
//   - ASCII "..."          (existing behaviour)
//   - Curly quotes: any opener in quoteOpeners + any closer in {quoteOpeners ∪ '"'}.
//     Pairing is permissive on purpose; mismatched left/right runes are common
//     (German „…" pairs U+201E with U+201C).
//   - ASCII '...' is treated as a phrase delimiter only when:
//     1. opener follows whitespace or is at start-of-string,
//     2. a closer exists later that is followed by whitespace, end-of-string,
//     or punctuation (any non-letter, non-digit), and
//     3. the phrase contains at least one whitespace character.
//     If any condition fails the apostrophe is left in place to be tokenised
//     normally downstream (boundary char in keyword_query.isTokenBoundary).
//
// Unmatched openers fall through verbatim into the remainder, mirroring the
// pre-existing behaviour for unmatched ASCII `"`.
func extractQuotedPhrases(query string) (phrases []string, remainder string) {
	var out strings.Builder
	out.Grow(len(query))

	runes := []rune(query)
	i := 0
	for i < len(runes) {
		r := runes[i]

		if r == '"' {
			closeIdx := indexRune(runes, i+1, '"')
			if closeIdx < 0 {
				out.WriteString(string(runes[i:]))
				break
			}
			phrase := strings.TrimSpace(string(runes[i+1 : closeIdx]))
			if phrase != "" {
				phrases = append(phrases, phrase)
			}
			i = closeIdx + 1
			out.WriteByte(' ')
			continue
		}

		if _, isOpener := quoteOpeners[r]; isOpener {
			closeIdx := indexAnyCloser(runes, i+1)
			if closeIdx < 0 {
				out.WriteString(string(runes[i:]))
				break
			}
			phrase := strings.TrimSpace(string(runes[i+1 : closeIdx]))
			if phrase != "" {
				phrases = append(phrases, phrase)
			}
			i = closeIdx + 1
			out.WriteByte(' ')
			continue
		}

		if r == '\'' {
			if phrase, advance, ok := scanAsciiSingleQuote(runes, i); ok {
				phrases = append(phrases, phrase)
				i += advance
				out.WriteByte(' ')
				continue
			}
			// Otherwise treat as ordinary character (apostrophe).
		}

		out.WriteRune(r)
		i++
	}

	remainder = strings.TrimSpace(strings.Join(strings.Fields(out.String()), " "))
	return phrases, remainder
}

// scanAsciiSingleQuote tries to consume a `'…'` phrase starting at runes[start]
// (which must be `'`). Returns (phrase, runes consumed, true) on success.
//
// Required preconditions on the input position:
//   - runes[start] == '\”
//   - start == 0 OR runes[start-1] is whitespace.
//
// Required preconditions on the closer:
//   - There is a later runes[end] == '\” such that runes[end+1] (if any) is
//     not a letter or digit.
//
// Required precondition on the content:
//   - runes[start+1 : end] contains at least one whitespace rune.
func scanAsciiSingleQuote(runes []rune, start int) (string, int, bool) {
	if start > 0 && !unicode.IsSpace(runes[start-1]) {
		return "", 0, false
	}
	for end := start + 1; end < len(runes); end++ {
		if runes[end] != '\'' {
			continue
		}
		// Closer must be followed by EOS or non-(letter|digit).
		if end+1 < len(runes) {
			next := runes[end+1]
			if unicode.IsLetter(next) || unicode.IsDigit(next) {
				continue
			}
		}
		// Content must contain whitespace (rules out `it's`).
		hasSpace := false
		for _, c := range runes[start+1 : end] {
			if unicode.IsSpace(c) {
				hasSpace = true
				break
			}
		}
		if !hasSpace {
			return "", 0, false
		}
		phrase := strings.TrimSpace(string(runes[start+1 : end]))
		if phrase == "" {
			return "", 0, false
		}
		return phrase, (end - start) + 1, true
	}
	return "", 0, false
}

func indexRune(runes []rune, from int, target rune) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

func indexAnyCloser(runes []rune, from int) int {
	for i := from; i < len(runes); i++ {
		if isQuoteCloser(runes[i]) {
			return i
		}
	}
	return -1
}
