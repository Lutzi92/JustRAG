package eval

import (
	"strings"
	"testing"
)

func convo() Question {
	return Question{
		ID: "MT01", KbID: "kb", Language: "de",
		Turns: []Turn{
			{Question: "Wer leitet das Stud.IP-Update?", Kind: TurnKindCorpus, QueryType: "lookup",
				MustCiteFileNames: []string{"Stud.IP-Update.md"},
				Answer:            "Das Stud.IP-Update leitet Frau Muster.", AnswerSources: []string{"Stud.IP-Update.md"}},
			{Question: "und auf welche Version?", Kind: TurnKindPronounRef, QueryType: "lookup",
				MustCiteFileNames: []string{"Stud.IP-Update.md"},
				Answer:            "Auf Version 5.4.", AnswerSources: []string{"Stud.IP-Update.md"}},
			{Question: "das als Tabelle", Kind: TurnKindAnswerRef},
		},
	}
}

func TestValidateQuestion_TurnsRelaxTopLevelFields(t *testing.T) {
	q := convo()
	if err := validateQuestion(q); err != nil {
		t.Fatalf("conversation row must validate without top-level question/must_cite: %v", err)
	}
}

func TestValidateQuestion_TurnRules(t *testing.T) {
	cases := []struct {
		name string
		mut  func(q *Question)
		want string
	}{
		{"unknown kind", func(q *Question) { q.Turns[0].Kind = "chit_chat" }, "kind"},
		{"empty question", func(q *Question) { q.Turns[1].Question = " " }, "question"},
		{"corpus turn without ground truth", func(q *Question) { q.Turns[0].MustCiteFileNames = nil }, "must_cite"},
		{"bad query_type", func(q *Question) { q.Turns[0].QueryType = "trivia" }, "query_type"},
		{"answer_ref as first turn", func(q *Question) { q.Turns = q.Turns[2:] }, "answer_ref"},
		{"answer_ref after turn without answer", func(q *Question) { q.Turns[1].Answer = ""; q.Turns[1].AnswerSources = nil }, "answer_ref"},
		{"single turn conversation", func(q *Question) { q.Turns = q.Turns[:1] }, "at least two"},
	}
	for _, c := range cases {
		q := convo()
		c.mut(&q)
		err := validateQuestion(q)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", c.name, c.want, err)
		}
	}
}

func TestExpandTurns_ProducesPerTurnQuestionsWithHistory(t *testing.T) {
	single := Question{ID: "Q1", Question: "x", KbID: "kb", Language: "de", MustCiteFileNames: []string{"a.md"}}
	out := ExpandTurns([]Question{single, convo()})
	if len(out) != 4 {
		t.Fatalf("want 1 single + 3 turns = 4, got %d", len(out))
	}
	if out[0].ID != "Q1" || len(out[0].History) != 0 || out[0].TurnKind != "" {
		t.Fatalf("single-turn row must pass through unchanged: %+v", out[0])
	}
	t1, t2, t3 := out[1], out[2], out[3]
	if t1.ID != "MT01#t1" || t2.ID != "MT01#t2" || t3.ID != "MT01#t3" {
		t.Fatalf("ids: %s %s %s", t1.ID, t2.ID, t3.ID)
	}
	if len(t1.History) != 0 || t1.TurnKind != TurnKindCorpus || t1.QueryType != "lookup" || t1.Question != "Wer leitet das Stud.IP-Update?" {
		t.Fatalf("turn 1: %+v", t1)
	}
	if len(t2.History) != 2 || t2.History[0].Role != "user" || t2.History[1].Role != "ai" || t2.History[1].Sources[0] != "Stud.IP-Update.md" {
		t.Fatalf("turn 2 history must be [user t1, ai t1]: %+v", t2.History)
	}
	if len(t3.History) != 4 || t3.TurnKind != TurnKindAnswerRef {
		t.Fatalf("turn 3 history must be 4 entries: %+v", t3)
	}
	if got := t3.MustCiteFileNames; len(got) != 1 || got[0] != "Stud.IP-Update.md" {
		t.Fatalf("answer_ref must default must_cite to previous answer_sources, got %v", got)
	}
	if t1.KbID != "kb" || t1.Language != "de" {
		t.Fatalf("kb/language must be inherited: %+v", t1)
	}
}

func TestExpandTurns_LeavesInputUntouched(t *testing.T) {
	in := []Question{convo()}
	_ = ExpandTurns(in)
	if in[0].Turns[2].MustCiteFileNames != nil {
		t.Fatal("ExpandTurns must not mutate the input conversation")
	}
}
