// Package chat: deterministic tabular-question cue classifier.
//
// DetectTabularCues scans a chat query for signals that it is a structured
// spreadsheet question rather than a prose lookup: aggregation cues ("wie
// viele", "summe", "how many"), filter/comparison cues ("über", "zwischen",
// "more than"), and literal values that could be looked up against ingested
// sheet cells (quoted phrases, identifier-shaped tokens, capitalised
// multi-word spans, and bare numbers). It is the first step of the Phase 3
// tabular router: PrepareChatContext consults TabularCues.Fired() to decide
// whether to attempt a stored-value lookup before falling back to normal
// retrieval, and PromoteIdentifierPhrases rewrites the query so an
// identifier literal becomes an exact keyword phrase for the BM25 arm.
package chat

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/tabular/profile"
)

// TabularLiteral is a candidate value extracted from a chat query for a
// stored-value lookup against ingested spreadsheet cells.
type TabularLiteral struct {
	Text string
	// Kind is "quoted" | "id" | "span" | "word" | "number", in descending
	// order of how deliberate the user was about it. Only "id" fires on its
	// own (Fired below); "word" and "number" are the weak kinds and need an
	// EXACT stored-value match to fire the router (anyHitFires).
	Kind string
}

// TabularCues summarizes the deterministic signals DetectTabularCues found
// in a chat query.
type TabularCues struct {
	Aggregation bool
	Filter      bool
	Literals    []TabularLiteral // deduped, question order
}

// Fired reports whether any cue is present (aggregation, filter, or an
// identifier literal). Quoted/span/number literals alone do NOT fire — they
// are only used for value lookup once another cue fires — except when a
// literal equals a stored value (the router checks that after lookup).
func (c TabularCues) Fired() bool {
	if c.Aggregation || c.Filter {
		return true
	}
	for _, lit := range c.Literals {
		if lit.Kind == "id" {
			return true
		}
	}
	return false
}

// boundary wraps a raw alternation with Unicode-aware word-boundary groups.
// Go's regexp \b is ASCII-only, so "über"/"größte"/"Fläche" wouldn't match
// correctly with it; (^|[^\p{L}\p{N}]) / ([^\p{L}\p{N}]|$) work on runes.
func boundary(alternation string) string {
	return `(?i)(^|[^\p{L}\p{N}])(` + alternation + `)([^\p{L}\p{N}]|$)`
}

var (
	tabularAggRe = regexp.MustCompile(boundary(
		`wie viele|anzahl|summe|gesamt|insgesamt|durchschnitt|mittelwert|mittel|` +
			`größte|kleinste|höchste|niedrigste|maximal|minimal|pro|je|gruppiert|verteilung|` +
			`how many|count|sum|total|average|mean|largest|smallest|highest|lowest|per|grouped|distribution`))

	// tabularAggCompoundRe covers German CLOSED COMPOUNDS that
	// tabularAggRe structurally cannot see: boundary() demands a
	// non-letter on both sides, so "gesamt" inside "Gesamtbetrag" and
	// "summe" inside "Jahressumme" never match, even though each word
	// names an aggregation. This is why the acceptance run skipped
	// sst-q17 ("Wie hoch ist der Gesamtbetrag … aller Gebäude?") with
	// skipped_no_cue. Two deliberately narrow shapes:
	//
	//   - an aggregating MODIFIER as the FIRST element, followed by a head
	//     noun of >= 3 letters: Gesamtbetrag, Gesamtfläche, Gesamtkosten,
	//     Durchschnittsmiete. The >= 3 keeps the inflected adjective
	//     ("gesamte", "gesamten" — "der gesamten Anlage") out.
	//   - an aggregation HEAD NOUN as the LAST element: Jahressumme,
	//     Gesamtanzahl, Flächendurchschnitt, Monatsmittelwert.
	//
	// Bare "…zahl" is deliberately NOT a head noun: it would make every
	// "Postleitzahl"/"Hausnummer"-style pure lookup an aggregation.
	tabularAggCompoundRe = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])(` +
		`(?:gesamt|durchschnitts)\p{L}{3,}|` +
		`\p{L}{2,}(?:summe|anzahl|durchschnitt|mittelwert)` +
		`)([^\p{L}\p{N}]|$)`)

	tabularFilterRe = regexp.MustCompile(boundary(
		`über|unter|mehr als|weniger als|zwischen|mindestens|höchstens|alle .{1,40}? mit|` +
			`ab \d+|bis \d+|seit \d+|vor \d+|` +
			`greater|less|more than|fewer than|between|at least|at most|all .{1,40}? with|since \d+|before \d+`))

	// tabularNumberRe matches a plain (optionally decimal) number literal
	// candidate; callers additionally require >= 3 total digits.
	tabularNumberRe = regexp.MustCompile(`\d+([.,]\d+)?`)

	// tabularQuotedRe variants: straight double quotes, German „…“ low-high
	// quotes, and single quotes. Bounded to 2-80 runes between the marks so
	// an unmatched opening quote doesn't swallow the rest of the query.
	tabularQuotedDoubleRe = regexp.MustCompile(`"([^"]{2,80})"`)
	tabularQuotedGermanRe = regexp.MustCompile(`„([^“]{2,80})“`)
	tabularQuotedSingleRe = regexp.MustCompile(`'([^']{2,80})'`)

	// tabularIDShapeRe matches uppercase-letter-prefixed identifier tokens
	// like "4711-A" or "HRZ-0815". Deliberately case-sensitive.
	tabularIDShapeRe = regexp.MustCompile(`^[A-Z]{1,4}-?\d{2,}[A-Za-z0-9._\-/]*$`)
)

const (
	// tabularMinWordLiteralRunes is the shortest lone capitalised word
	// taken as a "word" literal. Short capitalised tokens are mostly
	// abbreviations and articles at a clause start, and every literal
	// costs one stored-value lookup.
	tabularMinWordLiteralRunes = 4
	// tabularMaxWordLiterals bounds how many lone capitalised words one
	// question contributes — German questions are full of capitalised
	// nouns, and each one is a separate tabular_column_values query.
	tabularMaxWordLiterals = 4
)

// tabularSpanStopWords are capitalised words that must not start a span
// literal (interrogatives / imperatives that happen to be capitalised
// because they lead a clause, not because they name something).
var tabularSpanStopWords = map[string]bool{
	"Wie": true, "Welche": true, "Was": true, "Wo": true, "Gibt": true,
	"Zeige": true, "Liste": true,
	"How": true, "What": true, "Which": true, "Show": true, "List": true,
}

// runeSpan is a half-open [Start, End) range of rune offsets into the
// original query, used to prevent later extraction passes from re-claiming
// text already consumed by an earlier one (e.g. a quoted id shouldn't also
// surface as a bare "number" literal).
type runeSpan struct {
	Start, End int
}

func overlaps(claimed []runeSpan, s runeSpan) bool {
	for _, c := range claimed {
		if s.Start < c.End && c.Start < s.End {
			return true
		}
	}
	return false
}

type candidate struct {
	span runeSpan
	lit  TabularLiteral
}

// DetectTabularCues scans query for aggregation/filter cues and candidate
// value literals. Literals are deduped by lower-cased text and returned in
// the order they first appear in the query.
func DetectTabularCues(query string) TabularCues {
	cues := TabularCues{
		Aggregation: tabularAggRe.MatchString(query) || tabularAggCompoundRe.MatchString(query),
		Filter:      tabularFilterRe.MatchString(query),
	}

	var claimed []runeSpan
	var candidates []candidate

	// 1. Quoted spans (highest priority — explicit user delimiting).
	for _, re := range []*regexp.Regexp{tabularQuotedDoubleRe, tabularQuotedGermanRe, tabularQuotedSingleRe} {
		for _, m := range re.FindAllStringSubmatchIndex(query, -1) {
			outerStart, outerEnd := m[0], m[1]
			innerStart, innerEnd := m[2], m[3]
			sp := byteRangeToRuneSpan(query, outerStart, outerEnd)
			if overlaps(claimed, sp) {
				continue
			}
			claimed = append(claimed, sp)
			text := query[innerStart:innerEnd]
			candidates = append(candidates, candidate{span: sp, lit: TabularLiteral{Text: text, Kind: "quoted"}})
		}
	}

	// 2. Whitespace-delimited tokens: identifier-shaped values.
	tokens := tokenizeWithOffsets(query)
	prevEndedSentence := true // start of query counts as sentence-initial
	for i := range tokens {
		tokens[i].sentenceInit = prevEndedSentence
		prevEndedSentence = strings.ContainsAny(tokens[i].text, ".?!") &&
			strings.LastIndexAny(tokens[i].text, ".?!") == len(tokens[i].text)-1
	}

	for _, tok := range tokens {
		trimmed := strings.TrimRight(tok.text, ".,;:?!")
		if trimmed == "" {
			continue
		}
		trimEnd := tok.span.End - (len([]rune(tok.text)) - len([]rune(trimmed)))
		sp := runeSpan{Start: tok.span.Start, End: trimEnd}
		if overlaps(claimed, sp) {
			continue
		}
		leadingZero, longDigits, pattern := profile.LooksLikeIDValue(trimmed)
		isID := leadingZero || longDigits || pattern || tabularIDShapeRe.MatchString(trimmed)
		if !isID {
			continue
		}
		claimed = append(claimed, sp)
		candidates = append(candidates, candidate{span: sp, lit: TabularLiteral{Text: trimmed, Kind: "id"}})
	}

	// 3. Capitalised multi-word spans (1-4 words, optional trailing number).
	spanCandidates := detectSpans(tokens, claimed)
	for _, c := range spanCandidates {
		if overlaps(claimed, c.span) {
			continue
		}
		claimed = append(claimed, c.span)
		candidates = append(candidates, c)
	}

	// 4. Bare number literals (>= 3 total digits), skipping claimed ranges.
	for _, m := range tabularNumberRe.FindAllStringIndex(query, -1) {
		sp := byteRangeToRuneSpan(query, m[0], m[1])
		if overlaps(claimed, sp) {
			continue
		}
		text := query[m[0]:m[1]]
		digitCount := 0
		for _, r := range text {
			if unicode.IsDigit(r) {
				digitCount++
			}
		}
		if digitCount < 3 {
			continue
		}
		claimed = append(claimed, sp)
		candidates = append(candidates, candidate{span: sp, lit: TabularLiteral{Text: text, Kind: "number"}})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].span.Start < candidates[j].span.Start
	})

	seen := make(map[string]bool)
	for _, c := range candidates {
		key := strings.ToLower(c.lit.Text)
		if seen[key] {
			continue
		}
		seen[key] = true
		cues.Literals = append(cues.Literals, c.lit)
	}

	return cues
}

// tabularToken is a whitespace-delimited token with its rune-offset span
// into the original query.
type tabularToken struct {
	text         string
	span         runeSpan
	sentenceInit bool
}

func tokenizeWithOffsets(query string) []tabularToken {
	var out []tabularToken
	runes := []rune(query)
	i := 0
	for i < len(runes) {
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
		if i >= len(runes) {
			break
		}
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) {
			i++
		}
		out = append(out, tabularToken{text: string(runes[start:i]), span: runeSpan{Start: start, End: i}})
	}
	return out
}

// detectSpans finds capitalised-word runs (1-4 words, each a Unicode
// upper-initial word of >= 2 runes) optionally followed by a trailing bare
// number token, excluding a run that starts at a sentence-initial position
// or on the stop-word list.
func detectSpans(tokens []tabularToken, claimed []runeSpan) []candidate {
	var out []candidate
	words := 0
	isCapWord := func(t tabularToken) (string, bool) {
		trimmed := strings.Trim(t.text, `"'„“.,;:?!()`)
		rs := []rune(trimmed)
		if len(rs) < 2 {
			return "", false
		}
		if !unicode.IsUpper(rs[0]) {
			return "", false
		}
		for _, r := range rs {
			if !unicode.IsLetter(r) && r != 'ß' {
				return "", false
			}
		}
		return trimmed, true
	}
	isNumberTok := func(t tabularToken) (string, bool) {
		trimmed := strings.TrimRight(t.text, ".,;:?!")
		if trimmed == "" {
			return "", false
		}
		for _, r := range trimmed {
			if !unicode.IsDigit(r) {
				return "", false
			}
		}
		return trimmed, true
	}

	i := 0
	for i < len(tokens) {
		word, ok := isCapWord(tokens[i])
		if !ok || overlaps(claimed, tokens[i].span) {
			i++
			continue
		}
		if tokens[i].sentenceInit || tabularSpanStopWords[word] {
			i++
			continue
		}
		runStart := i
		j := i + 1
		wordCount := 1
		for wordCount < 4 && j < len(tokens) {
			_, ok2 := isCapWord(tokens[j])
			if !ok2 || overlaps(claimed, tokens[j].span) {
				break
			}
			wordCount++
			j++
		}
		runEnd := j
		hasTrailingNumber := false
		// Optional trailing number token immediately after the run.
		if j < len(tokens) {
			if _, ok3 := isNumberTok(tokens[j]); ok3 && !overlaps(claimed, tokens[j].span) {
				runEnd = j + 1
				hasTrailingNumber = true
			}
		}
		// A lone capitalised word (e.g. any German noun) is far too common
		// to treat as an identifying SPAN on its own — a span fires the
		// router on any match quality, so it needs either a second
		// capitalised word or a trailing number to qualify.
		//
		// It is still worth LOOKING UP, though, as the weaker "word" kind:
		// "Wie groß ist die Fläche der Bibliothek?" names a row by its
		// label, and before this the question produced no literal at all,
		// so the stored-value gate had nothing to look up and the router
		// skipped with skipped_no_cue (acceptance run: sst-q36, sst-q37).
		// A "word" literal never fires TabularCues.Fired() by itself and
		// only fires via anyHitFires on an EXACT stored-value match, so a
		// noun that happens to be a cell value fires and an ordinary prose
		// noun does not. Capped per query because each literal costs one
		// tabular_column_values lookup.
		if wordCount < 2 && !hasTrailingNumber {
			if words < tabularMaxWordLiterals && utf8.RuneCountInString(word) >= tabularMinWordLiteralRunes {
				out = append(out, candidate{span: tokens[i].span, lit: TabularLiteral{Text: word, Kind: "word"}})
				words++
			}
			i++
			continue
		}
		startSpan := tokens[runStart].span
		endSpan := tokens[runEnd-1].span
		sp := runeSpan{Start: startSpan.Start, End: endSpan.End}
		text := string([]rune(strings.Join(tokenTexts(tokens[runStart:runEnd]), " ")))
		out = append(out, candidate{span: sp, lit: TabularLiteral{Text: text, Kind: "span"}})
		i = runEnd
	}
	return out
}

func tokenTexts(tokens []tabularToken) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = strings.TrimRight(strings.TrimLeft(t.text, `"'„“(`), `"'“.,;:?!)`)
	}
	return out
}

// byteRangeToRuneSpan converts a [byteStart, byteEnd) range in s into rune
// offsets.
func byteRangeToRuneSpan(s string, byteStart, byteEnd int) runeSpan {
	start := -1
	runeIdx := 0
	for byteIdx := range s {
		if byteIdx == byteStart {
			start = runeIdx
		}
		if byteIdx == byteEnd {
			return runeSpan{Start: start, End: runeIdx}
		}
		runeIdx++
	}
	if start == -1 {
		start = runeIdx
	}
	if byteEnd >= len(s) {
		return runeSpan{Start: start, End: runeIdx}
	}
	return runeSpan{Start: start, End: runeIdx}
}

// PromoteIdentifierPhrases returns query with every "id"-kind literal that
// is not already inside double quotes wrapped in double quotes, so the
// keyword arm's extractQuotedPhrases treats it as an exact phrase. Only the
// first occurrence of each literal's exact text is replaced.
func PromoteIdentifierPhrases(query string, lits []TabularLiteral) string {
	result := query
	for _, lit := range lits {
		if lit.Kind != "id" || lit.Text == "" {
			continue
		}
		idx := strings.Index(result, lit.Text)
		if idx < 0 {
			continue
		}
		afterIdx := idx + len(lit.Text)
		alreadyQuoted := idx > 0 && result[idx-1] == '"' &&
			afterIdx < len(result) && result[afterIdx] == '"'
		if alreadyQuoted {
			continue
		}
		result = result[:idx] + `"` + lit.Text + `"` + result[afterIdx:]
	}
	return result
}
