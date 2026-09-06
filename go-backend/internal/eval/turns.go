package eval

import (
	"fmt"
	"strings"
)

// Turn kinds (docs/superpowers/specs/2026-09-05-rag-sota-review-triage-and-roadmap.md §4 Wave 2).
const (
	TurnKindCorpus      = "corpus"       // a normal retrieval question (usually the opening turn)
	TurnKindPronounRef  = "pronoun_ref"  // refers to the previous subject by pronoun/ellipsis ("und wer leitet es?")
	TurnKindTopicShift  = "topic_shift"  // unrelated new subject; condensation must NOT drag the old subject in
	TurnKindAnswerRef   = "answer_ref"   // refers to the previous ANSWER, retrieval-free reformat ("das als Tabelle")
	TurnKindPostAbstain = "post_abstain" // follows an abstention; the new question must retrieve normally
)

// ValidTurnKind reports whether k is one of the five turn kinds.
func ValidTurnKind(k string) bool {
	switch k {
	case TurnKindCorpus, TurnKindPronounRef, TurnKindTopicShift, TurnKindAnswerRef, TurnKindPostAbstain:
		return true
	}
	return false
}

// isBlank reports whether s is empty or all whitespace.
func isBlank(s string) bool {
	return strings.TrimSpace(s) == ""
}

// validateTurns checks a conversation row. The top-level question /
// must_cite fields are not required when turns are present: ground truth
// lives per turn.
func validateTurns(q Question) error {
	for i, t := range q.Turns {
		if !ValidTurnKind(t.Kind) {
			return fmt.Errorf("question %q turn %d: invalid kind %q", q.ID, i+1, t.Kind)
		}
		if isBlank(t.Question) {
			return fmt.Errorf("question %q turn %d: question is required", q.ID, i+1)
		}
		if err := validateQueryType(t.QueryType); err != nil {
			return fmt.Errorf("question %q turn %d: %w", q.ID, i+1, err)
		}
		if t.Kind == TurnKindAnswerRef {
			if i == 0 {
				return fmt.Errorf("question %q turn %d: answer_ref cannot be the first turn", q.ID, i+1)
			}
			prev := q.Turns[i-1]
			if isBlank(prev.Answer) || len(prev.AnswerSources) == 0 {
				return fmt.Errorf("question %q turn %d: answer_ref needs the previous turn's answer and answer_sources", q.ID, i+1)
			}
			continue
		}
		if len(t.MustCiteFileNames) == 0 {
			return fmt.Errorf("question %q turn %d: must_cite_file_names is required for kind %s", q.ID, i+1, t.Kind)
		}
		for _, n := range t.MustCiteFileNames {
			if isBlank(n) {
				return fmt.Errorf("question %q turn %d: blank must_cite_file_names entry", q.ID, i+1)
			}
		}
	}
	// Checked last so a single-element slice whose one turn is itself
	// invalid (e.g. an answer_ref turn with nothing before it) reports
	// the specific per-turn problem rather than the generic length one.
	if len(q.Turns) < 2 {
		return fmt.Errorf("question %q: turns needs at least two turns", q.ID)
	}
	return nil
}

// ExpandTurns turns every conversation row into one Question per turn.
// Turn n carries the history of turns 1..n-1 (user question, then the
// authored assistant answer with its sources) so a replay can condense
// exactly the way production does, without any chat rows. Single-turn
// rows pass through unchanged. The input is not mutated.
func ExpandTurns(qs []Question) []Question {
	out := make([]Question, 0, len(qs))
	for _, q := range qs {
		if len(q.Turns) == 0 {
			out = append(out, q)
			continue
		}
		var history []HistoryEntry
		for i, t := range q.Turns {
			tq := Question{
				ID:        fmt.Sprintf("%s#t%d", q.ID, i+1),
				Question:  t.Question,
				KbID:      q.KbID,
				Language:  q.Language,
				QueryType: t.QueryType,
				Notes:     t.Notes,
				TurnKind:  t.Kind,
				History:   append([]HistoryEntry(nil), history...),
			}
			tq.MustCiteFileNames = append([]string(nil), t.MustCiteFileNames...)
			if t.Kind == TurnKindAnswerRef && len(tq.MustCiteFileNames) == 0 && i > 0 {
				tq.MustCiteFileNames = append([]string(nil), q.Turns[i-1].AnswerSources...)
			}
			out = append(out, tq)
			history = append(history, HistoryEntry{Role: "user", Content: t.Question})
			if !isBlank(t.Answer) {
				history = append(history, HistoryEntry{Role: "ai", Content: t.Answer, Sources: append([]string(nil), t.AnswerSources...)})
			}
		}
	}
	return out
}
