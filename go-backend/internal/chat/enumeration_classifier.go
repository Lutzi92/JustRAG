package chat

import (
	"regexp"
	"strings"
)

// Enumeration-query detection.
//
// Pipeline rationale: mid-range LLMs (Gemma-class, self-hosted) show
// Context-Rot patterns on enumeration-style prompts when 10-30 semantically
// similar chunks are in context — they reliably pick a subset to summarize
// instead of the full list. The retrieval side is fine (verified in logs);
// the bottleneck is prose-generation completeness at this model scale.
// A structured-extraction pre-pass (see ai.ExtractEnumerationMatches) is
// the 2026-state-of-the-art remedy, but only worth its extra LLM call on
// the narrow band of queries where it helps. This classifier identifies
// that band.
//
// Precision over recall: a false positive costs one extra LLM call (no
// answer-quality harm); a false negative just keeps the existing behavior.
// We err on the side of activating when any cue is present.

var enumerationPatternsDE = []*regexp.Regexp{
	// "in welchen X ist / arbeitet / …"; "in welchen Projekten ist Person Y"
	regexp.MustCompile(`(?i)\bin\s+welch(?:en|er|em|e|es)\b`),
	// "welche X …" at sentence start or after punctuation ("Welche Projekte…")
	regexp.MustCompile(`(?i)(?:^|[.?!]\s+)welch(?:e|er|es|en|em)\b`),
	// "wer arbeitet / hat / ist …"
	regexp.MustCompile(`(?i)\bwer\s+(?:arbeitet|ist|war|hat|leitet|gehört|macht|betreut|betreibt)\b`),
	// "alle X die/der/das …" — "alle Projekte, die X haben"
	regexp.MustCompile(`(?i)\balle\s+\S+\s+(?:die|der|das|mit|von|zu|in|bei|aus)\b`),
	// Enumeration noun keywords — any occurrence signals list-intent.
	regexp.MustCompile(`(?i)\b(?:auflistung|aufzählung|auflisten|aufzählen)\b`),
	// Imperative "liste/nenne/zeige" + determinative (alle, aller, jede,
	// die, …). Covers "Liste alle Projekte", "Liste aller Meilensteine",
	// "Nenne alle Beteiligten", "Zeige mir alle Steckbriefe".
	regexp.MustCompile(`(?i)\b(?:liste|nenne|zeige)\s+(?:mir\s+)?(?:alle|aller|allen|jede|jeder|jedes|jeden|die|der|das)\b`),
	// "welche X gibt es …"
	regexp.MustCompile(`(?i)\bwelche\s+\S+\s+gibt\s+es\b`),
}

var enumerationPatternsEN = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bin\s+which\b`),
	regexp.MustCompile(`(?i)(?:^|[.?!]\s+)which\s+\S+\b`),
	regexp.MustCompile(`(?i)\bwho\s+(?:works|is|was|has|leads|runs|owns|reports|manages|belongs)\b`),
	regexp.MustCompile(`(?i)\ball\s+\S+\s+(?:that|which|with|for|containing|matching)\b`),
	regexp.MustCompile(`(?i)\b(?:list|enumerate|name|show)\s+(?:all|every|each)\b`),
	regexp.MustCompile(`(?i)\bhow\s+many\s+\S+\s+(?:are|were|have|contain)\b`),
}

// IsEnumerationQuery reports whether the query asks for an exhaustive list
// of matching entities ("in which projects is X?", "wer arbeitet an Y?",
// "list all documents that mention Z", …). When true, the chat pipeline
// runs a structured-extraction pre-pass over the retrieved chunks before
// generating the prose answer; when false, it uses the direct chat flow.
//
// lang is the conversation language; German patterns fire for "de",
// English for "en". Any other language falls back to English patterns —
// most non-German-speaking users write enumeration queries in English, and
// false positives are cheap.
func IsEnumerationQuery(query, lang string) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return false
	}
	patterns := enumerationPatternsEN
	if lang == "de" {
		patterns = enumerationPatternsDE
	}
	for _, re := range patterns {
		if re.MatchString(q) {
			return true
		}
	}
	return false
}
