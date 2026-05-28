package chat

import (
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// DiffChunk is one segment of the refine token-diff that the frontend
// renders. Kind is the per-segment classification:
//   - "kept"    — text present in both original and refined (most of the answer)
//   - "added"   — text introduced by the refine pass
//   - "removed" — text the refine pass dropped
//
// The frontend stitches the chunks back together in order; "removed"
// chunks render with a strikethrough, "added" chunks with the
// "korrigiert nach Faktencheck" yellow highlight + tooltip, "kept"
// chunks render as plain text.
type DiffChunk struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// computeRefineDiff produces a word-level diff between the original
// and refined answers. Word-level (not character or sentence): char
// diffs render as illegible noise on the frontend ("d~~elete~~replace"
// per word), sentence diffs lose information when refine touches just
// a clause inside a long sentence — the typical surgical case.
//
// Implementation uses the standard diffmatchpatch words-to-chars trick
// to coerce the algorithm's char-mode into word granularity: the
// original strings are tokenised into words, each word gets a
// single-rune sentinel, the diff runs in O(n) on the sentinel string,
// then the result is mapped back to words.
//
// The return is always a slice — empty if the inputs match exactly.
// Adjacent chunks of the same kind are merged so the frontend gets
// the longest possible runs of "kept"/"added"/"removed" instead of
// per-word fragments.
func computeRefineDiff(original, refined string) []DiffChunk {
	dmp := diffmatchpatch.New()
	wOrig, wNew, wArr := dmp.DiffLinesToRunes(
		insertSpacesAroundWords(original),
		insertSpacesAroundWords(refined),
	)
	rawDiffs := dmp.DiffMainRunes(wOrig, wNew, false)
	wordDiffs := dmp.DiffCharsToLines(rawDiffs, wArr)

	out := make([]DiffChunk, 0, len(wordDiffs))
	for _, d := range wordDiffs {
		// diffmatchpatch returns each word terminated by the original
		// `\n` separator that DiffLinesToRunes used. Convert back to
		// space so the frontend can simply concatenate kept+added
		// chunks to reconstruct the refined answer.
		text := strings.ReplaceAll(d.Text, "\n", " ")
		if strings.TrimSpace(text) == "" {
			continue
		}
		var kind string
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			kind = "kept"
		case diffmatchpatch.DiffInsert:
			kind = "added"
		case diffmatchpatch.DiffDelete:
			kind = "removed"
		default:
			continue
		}
		// Merge with previous if same kind — diffmatchpatch sometimes
		// emits adjacent chunks of the same type after the chars-to-
		// lines step, which would force the frontend to re-merge.
		if n := len(out); n > 0 && out[n-1].Kind == kind {
			out[n-1].Text += text
			continue
		}
		out = append(out, DiffChunk{Kind: kind, Text: text})
	}
	return out
}

// insertSpacesAroundWords splits the input on whitespace, then puts
// each word on its own line so DiffLinesToRunes treats words as the
// atomic unit. Tabs and newlines collapse to single spaces — the
// frontend re-inserts whitespace around each chunk on render, so we
// don't need to preserve original whitespace verbatim. (The "kept"
// majority of the diff comes through with single-space separation,
// which is what the frontend expects.)
func insertSpacesAroundWords(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, "\n") + "\n"
}
