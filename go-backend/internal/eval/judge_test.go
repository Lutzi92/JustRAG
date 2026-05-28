package eval

import (
	"context"
	"strings"
	"testing"
)

type scriptedCompleter struct {
	responses []string
	calls     int
}

func (s *scriptedCompleter) Complete(_ context.Context, _, _ string) (string, error) {
	if s.calls >= len(s.responses) {
		return "", errorsNew("no more scripted responses")
	}
	r := s.responses[s.calls]
	s.calls++
	return r, nil
}

func TestJudgeEvaluate_HappyPath(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		`{"claims":[{"text":"A","supported":true},{"text":"B","supported":true},{"text":"C","supported":false}]}`,
		`{"score":4,"reasoning":"mostly correct"}`,
		`{"relevant":[true,false,true]}`,
	}}
	chunks := []RetrievedChunk{{FileID: "f1", Score: 0.9}, {FileID: "f2", Score: 0.6}, {FileID: "f3", Score: 0.3}}
	contents := []string{"c1", "c2", "c3"}
	q := Question{ID: "q", Question: "why?", KbID: "k", Language: "en", MustCiteFileIDs: []string{"f1"}}

	got := NewJudge(completer).Evaluate(context.Background(), q, "stub answer", chunks, contents)

	if got.Faithfulness == nil || *got.Faithfulness < 0.66 || *got.Faithfulness > 0.67 {
		t.Errorf("expected faithfulness ~0.666, got %+v", got.Faithfulness)
	}
	if got.AnswerRelevance == nil || *got.AnswerRelevance != 0.75 {
		t.Errorf("expected relevance 0.75, got %+v", got.AnswerRelevance)
	}
	if got.ContextPrecision == nil || *got.ContextPrecision < 0.66 || *got.ContextPrecision > 0.67 {
		t.Errorf("expected precision ~0.666, got %+v", got.ContextPrecision)
	}
	if len(got.JudgeErrors) != 0 {
		t.Errorf("expected no errors, got %v", got.JudgeErrors)
	}
	if got.Answer != "stub answer" {
		t.Errorf("expected answer preserved, got %q", got.Answer)
	}
}

func TestJudgeEvaluate_PartialFailureIsCapturedNotFatal(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		`{"claims":[{"text":"X","supported":true}]}`,
		`not json`, // answer relevance fails
		`{"relevant":[true]}`,
	}}
	chunks := []RetrievedChunk{{FileID: "f1", Score: 0.9}}
	contents := []string{"c1"}
	q := Question{ID: "q", Question: "why?", KbID: "k", Language: "en", MustCiteFileIDs: []string{"f1"}}

	got := NewJudge(completer).Evaluate(context.Background(), q, "stub", chunks, contents)

	if got.Faithfulness == nil || *got.Faithfulness != 1.0 {
		t.Errorf("expected faithfulness 1.0, got %+v", got.Faithfulness)
	}
	if got.AnswerRelevance != nil {
		t.Errorf("expected relevance nil on failure, got %+v", got.AnswerRelevance)
	}
	if got.ContextPrecision == nil || *got.ContextPrecision != 1.0 {
		t.Errorf("expected precision 1.0, got %+v", got.ContextPrecision)
	}
	if len(got.JudgeErrors) != 1 {
		t.Errorf("expected 1 error, got %v", got.JudgeErrors)
	}
	if !strings.Contains(got.JudgeErrors[0], "answer_relevance") {
		t.Errorf("error should mention which judge failed, got %q", got.JudgeErrors[0])
	}
}

func TestJudgeEvaluate_NoChunksSkipsContextPrecision(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		`{"claims":[]}`,
		`{"score":1,"reasoning":"refused"}`,
	}}
	q := Question{ID: "q", Question: "why?", KbID: "k", Language: "en", MustCiteFileIDs: []string{"f1"}}
	got := NewJudge(completer).Evaluate(context.Background(), q, "I don't know", nil, nil)
	if got.ContextPrecision != nil {
		t.Errorf("expected precision nil when no chunks, got %+v", got.ContextPrecision)
	}
}
