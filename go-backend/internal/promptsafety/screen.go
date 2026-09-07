package promptsafety

import (
	"regexp"
	"unicode/utf8"
)

// Finding is one screening verdict: which rule fired, where, and enough
// surrounding text for a human to judge it. It is persisted verbatim into
// files.injection_detail (jsonb) and rendered in the admin file list, so the
// JSON field names and the rule vocabulary are a data contract — renaming
// either is a migration-shaped change, not a refactor.
type Finding struct {
	// Rule is the stable identifier of the pattern that fired (see
	// screenRules below). Never the raw matched text: the rule name is what
	// the UI groups and translates on.
	Rule string `json:"rule"`
	// Position is the RUNE offset of the match in the screened text — not a
	// byte offset. Parsed German prose is full of multi-byte runes and a
	// byte offset would point into the middle of one for any caller that
	// slices on it.
	Position int `json:"position"`
	// Snippet is at most SnippetMaxRunes runes of context around the match,
	// the match included. Untrusted text by construction: it is shown as
	// data (a tooltip), never fed back into a prompt.
	Snippet string `json:"snippet"`
}

const (
	// SnippetMaxRunes bounds Finding.Snippet. The snippet is stored in a
	// jsonb column and rendered in a tooltip, so it must not scale with the
	// document.
	SnippetMaxRunes = 300
	// snippetLeadRunes is how much of the snippet sits BEFORE the match, so
	// a reader sees what the injection is attached to and not just the
	// injection. Clamped at the document start.
	snippetLeadRunes = 100
	// DefaultWindowRunes is the fallback for an unusable windowRunes; it
	// mirrors the ingest_screening_window_runes default.
	DefaultWindowRunes = 600
	// minWindowRunes is the smallest window that can still hold the longest
	// pattern in screenRules with room on both sides of a half-window step.
	// Anything below it would silently screen nothing at all, so it falls
	// back to DefaultWindowRunes instead. The site_config key clamps at 100,
	// so this floor only guards direct callers.
	minWindowRunes = 64
)

// screenRules are the instruction patterns ScreenText slides over the text.
//
// They are instructionRe's alternatives MINUS its `https?://` one, and that
// omission is the whole point of this separate pattern set. instructionRe
// guards spreadsheet cell text, where a bare URL in a machine-written column
// description is itself a smell; a screened document is an RSS item, a
// crawled page, a Confluence page or a source file, and one without a single
// URL barely exists. Inheriting that alternative would flag essentially every
// external file, which is the same as flagging none.
//
// One regexp per rule rather than a single alternation with capture groups:
// the rule name is a data contract (it lands in files.injection_detail), and
// deriving it from a submatch index would make adding an alternative a
// silent renaming of every rule after it.
var screenRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"ignore_previous", regexp.MustCompile(`(?i)ignore (all|any|the|previous|prior|above)`)},
	{"disregard", regexp.MustCompile(`(?i)disregard (all|the|previous)`)},
	{"system_prompt", regexp.MustCompile(`(?i)system prompt`)},
	{"role_override", regexp.MustCompile(`(?i)you are (now|an?|the)\b`)},
	{"assistant_turn", regexp.MustCompile(`(?i)assistant:`)},
	{"chat_template", regexp.MustCompile(`(?i)<\|im_start\|>`)},
	{"do_not_follow", regexp.MustCompile(`(?i)do not follow`)},
	{"new_instructions", regexp.MustCompile(`(?i)new instructions`)},
}

// ScreenText slides a windowRunes-wide window over text in half-window steps
// and reports the first rule that fires, with its rune position and a bounded
// snippet. ok is false when nothing matched.
//
// The half-window step is what makes the windowing honest: a phrase that
// straddles the end of one window is fully contained in the next, so no
// pattern can hide on a boundary. A windowRunes too small to hold the
// longest pattern falls back to DefaultWindowRunes.
//
// Cheap heuristic, not a classifier — false negatives are expected (a
// determined injection dodges this list) and false positives are cheap by
// design: the only effect of a hit is a badge, never a change to what gets
// ingested or retrieved.
//
// Deliberately allocation-light: text can be a multi-megabyte parsed
// document, so this walks it with utf8 decoding and byte-offset anchors
// rather than materialising a []rune (4 bytes per rune) of the whole thing.
func ScreenText(text string, windowRunes int) (Finding, bool) {
	if text == "" {
		return Finding{}, false
	}
	if windowRunes < minWindowRunes {
		windowRunes = DefaultWindowRunes
	}
	step := windowRunes / 2

	idx := newRuneIndex(text, step)

	for startRune := 0; startRune < idx.total; startRune += step {
		startByte := idx.byteAt(startRune)
		endByte := idx.byteAt(startRune + windowRunes)
		window := text[startByte:endByte]

		bestRule := ""
		bestByte := -1
		for _, r := range screenRules {
			loc := r.re.FindStringIndex(window)
			if loc == nil {
				continue
			}
			if bestByte < 0 || loc[0] < bestByte {
				bestByte, bestRule = loc[0], r.name
			}
		}
		if bestByte < 0 {
			continue
		}

		pos := startRune + utf8.RuneCountInString(window[:bestByte])
		snipStart := pos - snippetLeadRunes
		if snipStart < 0 {
			snipStart = 0
		}
		snipEnd := snipStart + SnippetMaxRunes
		return Finding{
			Rule:     bestRule,
			Position: pos,
			Snippet:  text[idx.byteAt(snipStart):idx.byteAt(snipEnd)],
		}, true
	}
	return Finding{}, false
}

// runeIndex is a sparse rune-offset → byte-offset map over a string: one
// anchor every `step` runes. Converting a rune offset to a byte offset then
// costs one short forward walk from the nearest anchor instead of a []rune
// copy of the whole document.
type runeIndex struct {
	text  string
	step  int
	total int
	// anchors[k] is the byte offset of rune k*step.
	anchors []int
}

func newRuneIndex(text string, step int) *runeIndex {
	idx := &runeIndex{text: text, step: step, anchors: []int{0}}
	n := 0
	for byteOff := range text { // ranges over rune start offsets
		if n > 0 && n%step == 0 {
			idx.anchors = append(idx.anchors, byteOff)
		}
		n++
	}
	idx.total = n
	return idx
}

// byteAt returns the byte offset of the given rune index, clamped to
// len(text) for any index at or past the end.
func (i *runeIndex) byteAt(runeIdx int) int {
	if runeIdx <= 0 {
		return 0
	}
	if runeIdx >= i.total {
		return len(i.text)
	}
	k := runeIdx / i.step
	if k >= len(i.anchors) {
		return len(i.text)
	}
	off := i.anchors[k]
	for rem := runeIdx - k*i.step; rem > 0; rem-- {
		_, size := utf8.DecodeRuneInString(i.text[off:])
		if size == 0 {
			return len(i.text)
		}
		off += size
	}
	return off
}
