package eval

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
)

// stubTurnSearcher is a bare turnSearcher stub so MultiTurnAdapter's own
// branching logic can be unit-tested without a real search pipeline.
type stubTurnSearcher struct {
	calls  []stubCall
	chunks []RetrievedChunk
	err    error
}

type stubCall struct {
	searchQuery string
	rawQuery    string
}

func (s *stubTurnSearcher) SearchWithQuery(_ context.Context, _ Question, _ int, searchQuery, rawQuery string) ([]RetrievedChunk, error) {
	s.calls = append(s.calls, stubCall{searchQuery: searchQuery, rawQuery: rawQuery})
	if s.err != nil {
		return nil, s.err
	}
	return s.chunks, nil
}

func (s *stubTurnSearcher) ContentsForQuestion(string, int) ([]string, []string, bool) {
	return nil, nil, false
}

func (s *stubTurnSearcher) ChatContextForQuestion(string) (*chat.ChatContext, bool) {
	return nil, false
}

func TestMultiTurnAdapter_AnswerRefReturnsPriorSources(t *testing.T) {
	stub := &stubTurnSearcher{}
	m := &MultiTurnAdapter{inner: stub}
	q := Question{
		ID:       "MT01#t2",
		Question: "das als Tabelle",
		TurnKind: TurnKindAnswerRef,
		History: []HistoryEntry{
			{Role: "user", Content: "Wer leitet das Stud.IP-Update?"},
			{Role: "ai", Content: "Team X leitet es.", Sources: []string{"Stud.IP-Update.md"}},
		},
	}

	got, err := m.Search(context.Background(), q, 10)
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("inner must NOT be called for a detected answer_ref turn; got %d call(s): %+v", len(stub.calls), stub.calls)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 retrieved chunk (one per prior source), got %d: %+v", len(got), got)
	}
	if got[0].FileName != "Stud.IP-Update.md" || got[0].Score != 1 {
		t.Fatalf("got %+v, want RetrievedChunk{FileName: Stud.IP-Update.md, Score: 1}", got[0])
	}
}

func TestMultiTurnAdapter_NoHistoryDelegatesVerbatim(t *testing.T) {
	stub := &stubTurnSearcher{}
	m := &MultiTurnAdapter{inner: stub}
	q := Question{
		ID:       "Q1",
		Question: "Wer leitet das Stud.IP-Update?",
		TurnKind: TurnKindCorpus,
	}

	if _, err := m.Search(context.Background(), q, 10); err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("want 1 inner call, got %d", len(stub.calls))
	}
	if stub.calls[0].searchQuery != q.Question {
		t.Fatalf("searchQuery = %q, want verbatim %q", stub.calls[0].searchQuery, q.Question)
	}
	if stub.calls[0].rawQuery != "" {
		t.Fatalf("rawQuery = %q, want empty (no history to diverge from)", stub.calls[0].rawQuery)
	}
}

func TestMultiTurnAdapter_CondensesAndKeepsRaw(t *testing.T) {
	orig := condenseFn
	defer func() { condenseFn = orig }()
	const condensedText = "Wer leitet das Stud.IP-Update?"
	condenseFn = func(_ context.Context, _ *ai.ConfigResolver, _ []ai.ChatHistoryEntry, _, _, _ string) (string, error) {
		return condensedText, nil
	}

	history := []HistoryEntry{
		{Role: "user", Content: "Wer leitet das Stud.IP-Update?"},
		{Role: "ai", Content: "Team X leitet es.", Sources: []string{"Stud.IP-Update.md"}},
	}
	q := Question{
		ID:       "MT01#t2",
		Question: "und wer leitet es?",
		TurnKind: TurnKindPronounRef,
		History:  history,
	}

	t.Run("keepRaw=true", func(t *testing.T) {
		keep := true
		stub := &stubTurnSearcher{}
		m := &MultiTurnAdapter{inner: stub, keepRaw: &keep}

		if _, err := m.Search(context.Background(), q, 10); err != nil {
			t.Fatalf("Search error: %v", err)
		}
		if len(stub.calls) != 1 {
			t.Fatalf("want 1 inner call, got %d", len(stub.calls))
		}
		if stub.calls[0].searchQuery != condensedText {
			t.Fatalf("searchQuery = %q, want condensed %q", stub.calls[0].searchQuery, condensedText)
		}
		if stub.calls[0].rawQuery != q.Question {
			t.Fatalf("rawQuery = %q, want raw utterance %q (keepRaw=true)", stub.calls[0].rawQuery, q.Question)
		}
		if got := m.CondensedQueryForQuestion(q.ID); got != condensedText {
			t.Fatalf("CondensedQueryForQuestion = %q, want %q", got, condensedText)
		}
	})

	t.Run("keepRaw=false", func(t *testing.T) {
		keep := false
		stub := &stubTurnSearcher{}
		m := &MultiTurnAdapter{inner: stub, keepRaw: &keep}

		if _, err := m.Search(context.Background(), q, 10); err != nil {
			t.Fatalf("Search error: %v", err)
		}
		if len(stub.calls) != 1 {
			t.Fatalf("want 1 inner call, got %d", len(stub.calls))
		}
		if stub.calls[0].searchQuery != condensedText {
			t.Fatalf("searchQuery = %q, want condensed %q", stub.calls[0].searchQuery, condensedText)
		}
		if stub.calls[0].rawQuery != "" {
			t.Fatalf("rawQuery = %q, want empty (keepRaw=false)", stub.calls[0].rawQuery)
		}
		if got := m.CondensedQueryForQuestion(q.ID); got != condensedText {
			t.Fatalf("CondensedQueryForQuestion = %q, want %q", got, condensedText)
		}
	})
}

func TestAggregateByTurnKind_NilWithoutKinds(t *testing.T) {
	if got := AggregateByTurnKind(nil, 10); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
	reports := []QuestionReport{
		{Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.5}},
	}
	if got := AggregateByTurnKind(reports, 10); got != nil {
		t.Fatalf("expected nil when no report carries a TurnKind, got %v", got)
	}
}

func TestAggregateByTurnKind_BucketsByKind(t *testing.T) {
	reports := []QuestionReport{
		{Question: Question{TurnKind: TurnKindPronounRef}, Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.8, ReciprocalRank: 1.0}},
		{Question: Question{TurnKind: TurnKindPronounRef}, Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.6, ReciprocalRank: 0.5}},
		{Question: Question{TurnKind: TurnKindAnswerRef}, Metrics: PerQuestionMetrics{K: 10, RecallAtK: 1.0, ReciprocalRank: 1.0}},
		{Question: Question{TurnKind: TurnKindPronounRef}, Error: "boom"},
	}
	got := AggregateByTurnKind(reports, 10)
	if len(got) != 2 {
		t.Fatalf("want 2 buckets, got %d (%v)", len(got), got)
	}
	if got[TurnKindPronounRef].Count != 2 {
		t.Fatalf("pronoun_ref.Count = %d, want 2 (errored row excluded)", got[TurnKindPronounRef].Count)
	}
	if got[TurnKindAnswerRef].Count != 1 {
		t.Fatalf("answer_ref.Count = %d, want 1", got[TurnKindAnswerRef].Count)
	}
}
