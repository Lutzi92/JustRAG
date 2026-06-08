package chat

import (
	"regexp"
	"strings"
)

// Corpus-comparison query detection.
//
// Fires on queries that ask for a *comparison or list across many documents
// rendered as a table* — the NotebookLM-style prompt that standard top-k
// retrieval answers incompletely (coverage gap) and that mid-range self-hosted
// models then truncate (Context Rot). A positive routes the turn to
// RunCorpusTableChat (map-reduce per file). This is the cheap first stage of a
// two-stage gate; an LLM confirmation is the precision stage (later task).
//
// Precision matters because a false positive triggers ~N per-file LLM calls.
// We therefore require BOTH a comparison/enumeration cue AND a table cue,
// except for a few unambiguous single phrases.

var corpusTableStrongPhrasesEN = []string{
	"comparative report", "comparison table", "compare all", "compare every",
	"compare each", "table comparing", "table listing all", "table of all",
	"table with all",
}

var corpusTableStrongPhrasesDE = []string{
	"vergleichstabelle", "vergleichsbericht", "vergleiche alle", "vergleiche jede",
	"tabelle aller", "tabelle mit allen", "für jede", "für jeden",
	"liste aller", "alle dokumente", "jedes dokument",
}

var tableCueReEN = regexp.MustCompile(`(?i)\b(table|tabular|spreadsheet|columns?|excel)\b`)
var tableCueReDE = regexp.MustCompile(`(?i)\b(tabelle|tabellarisch|spalten?|excel)\b`)

var comparisonCueReEN = regexp.MustCompile(`(?i)\b(compare (?:all|every|each|across)|comparison|comparative|across all|across every|each (?:case|study|document|file|farm)|all (?:the )?(?:farms|documents|files|cases|case studies|studies))\b`)
var comparisonCueReDE = regexp.MustCompile(`(?i)\b(vergleich|vergleiche|vergleichend|über alle|jede(?:s|n)? (?:fall|studie|dokument|datei)|alle (?:höfe|dokumente|dateien|fälle|fallstudien))\b`)

// IsCorpusComparisonQuery reports whether the query asks for a corpus-wide
// comparison/list rendered as a table. Both EN and DE patterns are always
// checked (operators mix languages); lang is accepted for signature symmetry
// with the sibling classifiers.
func IsCorpusComparisonQuery(query, lang string) bool {
	q := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if q == "" {
		return false
	}
	for _, p := range corpusTableStrongPhrasesEN {
		if strings.Contains(q, p) && (tableCueReEN.MatchString(q) || strings.Contains(p, "table") || strings.Contains(p, "list")) {
			return true
		}
	}
	for _, p := range corpusTableStrongPhrasesDE {
		if strings.Contains(q, p) && (tableCueReDE.MatchString(q) || strings.Contains(p, "tabelle") || strings.Contains(p, "liste")) {
			return true
		}
	}
	hasTable := tableCueReEN.MatchString(q) || tableCueReDE.MatchString(q)
	hasCompare := comparisonCueReEN.MatchString(q) || comparisonCueReDE.MatchString(q)
	return hasTable && hasCompare
}
