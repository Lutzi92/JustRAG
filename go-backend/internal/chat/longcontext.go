package chat

import (
	"context"
	"strings"
	"unicode"
)

// T2-1 long-context (System 1 / System 2) routing
//
// Background: for *global synthesis* questions — "summarise all
// findings across these documents", "what are every contradiction
// in this corpus" — the standard retrieval pipeline underperforms.
// The reranker can't know which 15 chunks of 500 best answer "all";
// any narrow top-k loses 97 % of the corpus. The arXiv:2506.10408
// "System 1 / System 2 RAG" framework and the January 2025
// long-context-vs-RAG study both converge on the same finding: for
// these queries, route to a long-context pass that hands the LLM a
// much wider chunk pool (up to ~100k tokens) and skip the
// MMR/score-filter narrowing.
//
// Cost realities. A 1 M-token long-context call costs ~1 250× a
// normal RAG query. Even at 100 k tokens the per-turn cost is
// roughly 30× normal. So the routing MUST be conservative: only
// fire when the operator explicitly enabled it AND the query
// classifier is confident the question is genuinely global.
//
// This file implements the conservative *keyword-heuristic*
// classifier (the Phase A cut). An LLM-based classifier is a
// follow-up — flag-gated separately so an operator can audit the
// keyword firing rate before paying for per-turn classification
// LLM calls.

// globalSynthesisTriggersEN are English phrases / patterns that
// strongly signal a global-synthesis intent. Matches are
// case-insensitive substring (after lowercase + whitespace
// normalisation). Phrases are intentionally specific — single
// keywords ("all", "every") are too permissive (a "what is the
// capital of every German Bundesland" query is decidedly NOT
// global synthesis, even though "every" appears).
//
// Calibrated to err on the side of NOT firing: the cost of a
// missed global-synthesis query (slightly worse answer) is much
// smaller than the cost of an accidental fire (30× more LLM cost
// and a context window stuffed with irrelevant chunks).
var globalSynthesisTriggersEN = []string{
	"summarise all",
	"summarize all",
	"summarise the entire",
	"summarize the entire",
	"summary of all",
	"across all",
	"across every",
	"all of the documents",
	"all documents",
	"every document",
	"entire corpus",
	"whole corpus",
	"compare all",
	"contradictions in",
	"common themes",
	"overall picture",
	"big picture",
	"high-level overview",
	"high level overview",
	"give me an overview",
	"give me a complete overview",
}

// globalSynthesisTriggersDE are German equivalents. The match runs
// against the lower-cased query so ‹Faßt› would normalise to
// ‹fasst› (locale-aware lower in `unicode.ToLower`); we still keep
// the most common modern German spellings here.
var globalSynthesisTriggersDE = []string{
	"fasse alle",
	"zusammenfassung aller",
	"fasse die gesamten",
	"überblick über alle",
	"überblick über die gesamten",
	"alle dokumente",
	"jedes dokument",
	"gesamte korpus",
	"über alle hinweg",
	"vergleiche alle",
	"widersprüche in",
	"gemeinsame themen",
	"gesamtbild",
}

// IsGlobalSynthesisQuery returns true when the query matches the
// language-aware keyword heuristic for global synthesis intent.
// Both English and German triggers are checked regardless of `lang`
// — operators occasionally mix languages in a single chat. The cost
// of double-checking is two substring scans, trivial.
//
// The function is a pure heuristic — no LLM call, no DB access — so
// callers can use it at zero latency. Empty or whitespace-only
// queries return false (no signal to act on).
func IsGlobalSynthesisQuery(query string) bool {
	q := normaliseForSynthesisMatch(query)
	if q == "" {
		return false
	}
	for _, t := range globalSynthesisTriggersEN {
		if strings.Contains(q, t) {
			return true
		}
	}
	for _, t := range globalSynthesisTriggersDE {
		if strings.Contains(q, t) {
			return true
		}
	}
	return false
}

// normaliseForSynthesisMatch collapses runs of whitespace to a
// single space, lower-cases the result, and trims leading/trailing
// whitespace. Operator queries with stray newlines or tab
// indentation (yes, this happens — copy-paste from documentation)
// still match the triggers above without us listing newline
// variants. unicode.IsSpace handles Unicode whitespace correctly
// (German non-breaking space, full-width CJK space, etc.).
func normaliseForSynthesisMatch(q string) string {
	if strings.TrimSpace(q) == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(q))
	prevSpace := true // skip leading space
	for _, r := range q {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(unicode.ToLower(r))
		prevSpace = false
	}
	// Drop trailing space if any.
	out := b.String()
	if len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return out
}

// ShouldRouteLongContext is the call-site contract: returns true
// only when ALL gating conditions are met. Centralised so every
// dispatch path uses the same predicate, and so the gating order is
// audit-grep-able.
//
// Order of checks:
//  1. Operator flag `chat_longcontext_enabled` is on.
//  2. Query type is `complex_reasoning` — lookup / enumeration
//     have dedicated paths that ignore global synthesis.
//  3. The keyword classifier fires on the (condensed) query text.
//
// All three must be true. A query that the classifier would match
// but the operator hasn't enabled the gate for stays on the
// normal retrieval path.
func ShouldRouteLongContext(ctx context.Context, reader SiteConfigReader, queryType, condensedQuery string) bool {
	if !ChatLongContextEnabled(ctx, reader) {
		return false
	}
	if queryType != "complex_reasoning" {
		return false
	}
	return IsGlobalSynthesisQuery(condensedQuery)
}
