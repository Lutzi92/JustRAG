package eval

import (
	"context"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
)

// condenseFn is a package-level var so tests can swap in a stub without a
// live LLM call. Defaults to the exported chat.CondenseFromHistory (ruling
// W2-R1 — condensation driven by the fixture's History, no chat rows).
var condenseFn = chat.CondenseFromHistory

// condensedQueryTracer is the optional surface RunEval uses to record the
// per-question condensed query onto QuestionReport.CondensedQuery. Mirrors
// the agentTracer pattern in runner.go: adapters that don't condense
// (legacy, plain ProductionContextAdapter) simply don't implement it.
type condensedQueryTracer interface {
	CondensedQueryForQuestion(id string) string
}

// turnSearcher is the minimal surface MultiTurnAdapter needs from its inner
// adapter: run a search against an explicit (searchQuery, rawQuery) pair
// rather than deriving both from Question.Question, plus the two judge-mode
// accessors it delegates verbatim. Satisfied by *ProductionContextAdapter;
// kept as a small interface so tests can stub it without a real search
// pipeline.
type turnSearcher interface {
	SearchWithQuery(ctx context.Context, q Question, k int, searchQuery, rawQuery string) ([]RetrievedChunk, error)
	ContentsForQuestion(questionID string, k int) (contents []string, fileNames []string, ok bool)
	ChatContextForQuestion(questionID string) (*chat.ChatContext, bool)
}

// MultiTurnAdapter replays a multi-turn golden-set conversation (the
// per-turn Questions eval.ExpandTurns produces — see Wave 2 Task 1) through
// the standard chat.PrepareChatContext path, exactly the way production
// answers a follow-up:
//
//   - answer_ref turns detected as a retrieval-free reformat
//     (chat.IsTransformFollowUpQuery) short-circuit straight to the
//     previous assistant turn's sources — production carries those over
//     verbatim (BuildTransformChatContext) rather than re-retrieving
//     (ruling W2-R3).
//   - every other turn with history is condensed into a standalone query
//     via chat.CondenseFromHistory, mirroring the production condense step
//     (ruling W2-R1), then delegated with the rewrite⊕raw lane applied the
//     same way http_send.go applies it (chat.RawQueryForRetrieval).
//   - a turn with no history (single-turn questions, and turn 1 of every
//     conversation) delegates the verbatim question with no raw lane.
type MultiTurnAdapter struct {
	inner      turnSearcher
	aiResolver *ai.ConfigResolver
	cfg        chat.SiteConfigReader
	// keepRaw overrides chat_condense_keep_raw_enabled for this eval run:
	// nil reads the site_config (production behaviour, same as
	// http_send.go); non-nil pins the decision regardless of site_configs
	// (backs cmd/eval's --keep-raw on|off, ruling W2-R10).
	keepRaw *bool

	condensed map[string]string
}

// NewMultiTurnAdapter constructs a MultiTurnAdapter over a
// ProductionContextAdapter, which is what actually runs the standard
// PrepareChatContext retrieval once MultiTurnAdapter has resolved the turn's
// (searchQuery, rawQuery) pair.
func NewMultiTurnAdapter(inner *ProductionContextAdapter, aiResolver *ai.ConfigResolver, cfg chat.SiteConfigReader, keepRaw *bool) *MultiTurnAdapter {
	return &MultiTurnAdapter{
		inner:      inner,
		aiResolver: aiResolver,
		cfg:        cfg,
		keepRaw:    keepRaw,
		condensed:  make(map[string]string),
	}
}

// Search implements Searcher for a single expanded turn Question.
func (m *MultiTurnAdapter) Search(ctx context.Context, q Question, k int) ([]RetrievedChunk, error) {
	if q.TurnKind == TurnKindAnswerRef && chat.IsTransformFollowUpQuery(q.Question) {
		if prior := lastAIHistoryEntry(q.History); prior != nil {
			out := make([]RetrievedChunk, 0, len(prior.Sources))
			for _, name := range prior.Sources {
				out = append(out, RetrievedChunk{FileName: name, Score: 1})
			}
			return out, nil
		}
	}

	condensedQuery := q.Question
	if len(q.History) > 0 {
		condensedQuery, _ = condenseFn(ctx, m.aiResolver, toAIHistory(q.History), q.Question, q.KbID, q.Language)
	}
	m.recordCondensed(q.ID, condensedQuery)

	rawQuery := chat.RawQueryForRetrieval(m.keepRawEnabled(ctx), q.Question, condensedQuery)
	return m.inner.SearchWithQuery(ctx, q, k, condensedQuery, rawQuery)
}

// ContentsForQuestion delegates to the inner adapter.
func (m *MultiTurnAdapter) ContentsForQuestion(questionID string, k int) (contents []string, fileNames []string, ok bool) {
	return m.inner.ContentsForQuestion(questionID, k)
}

// ChatContextForQuestion delegates to the inner adapter.
func (m *MultiTurnAdapter) ChatContextForQuestion(questionID string) (*chat.ChatContext, bool) {
	return m.inner.ChatContextForQuestion(questionID)
}

// ConflictsForQuestion satisfies conflictTracer by reading the inner
// adapter's cached ChatContext, so a multi-turn replay carries the same
// per-turn `conflicts` array a single-turn run does. Without this the
// wrapper would hide the inner adapter's implementation from RunEval's type
// assertion and every replayed turn would report no conflicts.
func (m *MultiTurnAdapter) ConflictsForQuestion(questionID string) []chat.MessageConflict {
	c, ok := m.inner.ChatContextForQuestion(questionID)
	if !ok || c == nil {
		return nil
	}
	return chat.ConflictsForWire(c.Conflicts)
}

// CondensedQueryForQuestion returns the condensed (standalone) query
// computed for questionID's turn, or "" if Search hasn't run for it yet.
// Satisfies condensedQueryTracer so RunEval can surface it on
// QuestionReport.CondensedQuery.
func (m *MultiTurnAdapter) CondensedQueryForQuestion(id string) string {
	return m.condensed[id]
}

func (m *MultiTurnAdapter) recordCondensed(id, condensed string) {
	if m.condensed == nil {
		m.condensed = make(map[string]string)
	}
	m.condensed[id] = condensed
}

// keepRawEnabled resolves the rewrite⊕raw lane decision: the CLI override
// when set, otherwise the live site_config (mirrors production's
// ChatCondenseKeepRawEnabled read in http_send.go).
func (m *MultiTurnAdapter) keepRawEnabled(ctx context.Context) bool {
	if m.keepRaw != nil {
		return *m.keepRaw
	}
	return chat.ChatCondenseKeepRawEnabled(ctx, m.cfg)
}

// lastAIHistoryEntry returns the most recent "ai" entry in history, or nil
// if there is none. answer_ref turns need the LAST assistant reply, not
// necessarily the immediately preceding history entry (though ExpandTurns
// always appends user then ai in order, so in practice it is the same
// entry) — walking from the end keeps the intent explicit.
func lastAIHistoryEntry(history []HistoryEntry) *HistoryEntry {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "ai" {
			return &history[i]
		}
	}
	return nil
}

// toAIHistory adapts eval.HistoryEntry (fixture shape) to ai.ChatHistoryEntry
// (the shape chat.CondenseFromHistory / ai.CondenseQuestion need).
func toAIHistory(history []HistoryEntry) []ai.ChatHistoryEntry {
	out := make([]ai.ChatHistoryEntry, len(history))
	for i, h := range history {
		out[i] = ai.ChatHistoryEntry{Role: h.Role, Content: h.Content}
	}
	return out
}
