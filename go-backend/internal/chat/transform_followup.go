package chat

import (
	"regexp"
	"strings"

	"github.com/justrag/go-backend/internal/prompts"
)

// Transform-follow-up detection.
//
// Fires on follow-ups that ask to *transform the previous answer* — reformat
// as a table, summarize, shorten, translate, bullet-point — rather than ask
// the KB something new. These turns must NOT re-retrieve: a fresh corpus
// search redefines the scope (e.g. "das als Tabelle" after a filtered event
// list would table every event in the KB), and the single-turn answer prompt
// has no previous answer to transform. A positive routes the turn to a
// retrieval-free completion whose system prompt embeds the previous AI
// answer verbatim (BuildTransformChatContext) and whose sources are carried
// over from that answer.
//
// Heuristic, intentionally conservative (mirrors the corpus-table keyword
// gate): a transform cue alone is not enough — the message must either be a
// bare imperative (≤ 4 words, "als Tabelle bitte") or contain an anaphoric
// reference to the previous answer ("das", "this", "deine Antwort"). Any
// interrogative marker means a new content question and disqualifies the
// route. Misroutes degrade gracefully: the model sees the previous answer
// plus an instruction to say so when the request cannot be fulfilled from it.
//
// NOTE on \b and umlauts: Go's \b is ASCII-only, so umlaut-initial terms
// ("übersetz…") are listed without a leading \b — a space-to-ü transition is
// not a word boundary.

var transformCueRe = regexp.MustCompile(`(?i)\b(tabell\w*|table|spreadsheet|excel|xlsx?|csv|zusammenfass\w*|fasse\b.*\bzusammen|summar(?:y|ize|ise)\w*|tl;?dr|kürzer|kürze\b|shorter|shorten|stichpunkte?|stichworte?|aufzählung|bullet points?|als liste|as a list|translate|ins (?:englische|deutsche)|auf (?:englisch|deutsch)|in (?:english|german)|umformulier\w*|rephrase|reword|vereinfach\w*|simplif\w*|formatier\w*|reformat)\b|übersetz\w*`)

var transformAnaphorRe = regexp.MustCompile(`(?i)\b(das|dies|diese[srnm]?|davon|daraus|es|it|this|that|deine antwort|die antwort|letzte antwort|your (?:answer|response|reply)|previous answer|the above|obige[srnm]?)\b`)

var transformInterrogativeRe = regexp.MustCompile(`(?i)\b(welche[srnm]?|wie|wieso|warum|weshalb|wann|wer|wo|was|which|what|why|when|where|who|how)\b`)

// transformPrevAnswerMaxRunes caps the previous answer embedded into the
// transform system prompt. Generous on purpose: the whole point is that the
// model sees the FULL prior answer (long event tables included), unlike the
// per-message answer-history cap.
const transformPrevAnswerMaxRunes = 24000

// IsTransformFollowUpQuery reports whether the message asks to transform the
// previous answer rather than ask a new question. Both EN and DE patterns
// are always checked (operators mix languages).
func IsTransformFollowUpQuery(message string) bool {
	q := strings.ToLower(strings.Join(strings.Fields(message), " "))
	if q == "" {
		return false
	}
	if transformInterrogativeRe.MatchString(q) {
		return false
	}
	if !transformCueRe.MatchString(q) {
		return false
	}
	words := len(strings.Fields(q))
	if words <= 4 {
		// Bare imperative ("als Tabelle bitte", "kürzer bitte") — the
		// previous answer is the only possible referent.
		return true
	}
	return words <= 14 && transformAnaphorRe.MatchString(q)
}

// lastAIMessage returns the most recent assistant turn, or nil when the
// conversation has none (first turn, or user-only rows).
func lastAIMessage(rows []MessageRow) *MessageRow {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Role == "ai" {
			return &rows[i]
		}
	}
	return nil
}

// historyRowsExcluding returns rows minus the row with the given ID. The
// transform route uses it so the previous answer — embedded in FULL in the
// system prompt — is not duplicated into the char-capped history block.
func historyRowsExcluding(rows []MessageRow, id string) []MessageRow {
	out := make([]MessageRow, 0, len(rows))
	for _, r := range rows {
		if r.ID == id {
			continue
		}
		out = append(out, r)
	}
	return out
}

// BuildTransformChatContext assembles the retrieval-free ChatContext for a
// transform follow-up: the system prompt carries the KB prompt, the
// transform instruction, and the previous answer verbatim (capped); Sources
// are carried over from the previous answer so citations stay clickable; and
// Context is the previous answer so post-response factcheck verifies the
// transform against it.
func BuildTransformChatContext(prev MessageRow, kbSystemPrompt, lang string) *ChatContext {
	answer := truncateRunes(prev.Content, transformPrevAnswerMaxRunes)
	var sb strings.Builder
	if kbSystemPrompt != "" {
		sb.WriteString(kbSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompts.TransformFollowUpSystem(lang))
	sb.WriteString("\n\nPREVIOUS ANSWER (authoritative — transform exactly this content):\n")
	sb.WriteString(answer)
	return &ChatContext{
		SystemPrompt: sb.String(),
		Sources:      prev.DecodedSources(),
		Context:      answer,
	}
}
