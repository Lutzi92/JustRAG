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

// --- W4-R1: tolerant answer-relevance score parsing ---

func TestAnswerRelevance_AcceptsNumericStringScore(t *testing.T) {
	j := NewJudge(&scriptedCompleter{responses: []string{`{"score":"5","reasoning":"x"}`}})
	q := Question{ID: "q", Question: "why?", Language: "en"}

	got, err := j.answerRelevance(context.Background(), q, "answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1.0 {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestAnswerRelevance_AcceptsNumericFloatString(t *testing.T) {
	j := NewJudge(&scriptedCompleter{responses: []string{`{"score":"4.0","reasoning":"x"}`}})
	q := Question{ID: "q", Question: "why?", Language: "en"}

	got, err := j.answerRelevance(context.Background(), q, "answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0.75 {
		t.Errorf("expected 0.75, got %f", got)
	}
}

func TestAnswerRelevance_UnparseableScoreErrors(t *testing.T) {
	j := NewJudge(&scriptedCompleter{responses: []string{`{"score":"abc","reasoning":"x"}`}})
	q := Question{ID: "q", Question: "why?", Language: "en"}

	_, err := j.answerRelevance(context.Background(), q, "answer")
	if err == nil {
		t.Fatal("expected error for unparseable score")
	}
}

func TestJudgeEvaluate_AnswerRelevanceUnparseableScoreRecordsErrorNotFatal(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		`{"claims":[]}`,
		`{"score":"abc","reasoning":"x"}`,
	}}
	q := Question{ID: "q", Question: "why?", Language: "en"}
	got := NewJudge(completer).Evaluate(context.Background(), q, "answer", nil, nil)

	if got.AnswerRelevance != nil {
		t.Errorf("expected nil AnswerRelevance, got %+v", got.AnswerRelevance)
	}
	if len(got.JudgeErrors) != 1 || !strings.Contains(got.JudgeErrors[0], "answer_relevance") {
		t.Errorf("expected 1 answer_relevance error, got %v", got.JudgeErrors)
	}
}

// --- W4-R1: tolerant context-precision boolean-count parsing ---

func TestContextPrecision_TruncatesExtraBooleansWithWarning(t *testing.T) {
	j := NewJudge(&scriptedCompleter{responses: []string{`{"relevant":[true,true,false,true]}`}})
	q := Question{ID: "q", Question: "why?", Language: "en"}
	contents := []string{"c1", "c2", "c3"}

	got, warnings, err := j.contextPrecision(context.Background(), q, contents)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got < 0.666 || got > 0.667 {
		t.Errorf("expected ~0.666, got %f", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "expected 3") {
		t.Errorf("expected 1 warning mentioning 'expected 3', got %v", warnings)
	}
}

func TestContextPrecision_PadsMissingBooleansWithWarning(t *testing.T) {
	j := NewJudge(&scriptedCompleter{responses: []string{`{"relevant":[true,false]}`}})
	q := Question{ID: "q", Question: "why?", Language: "en"}
	contents := []string{"c1", "c2", "c3"}

	got, warnings, err := j.contextPrecision(context.Background(), q, contents)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got < 0.333 || got > 0.334 {
		t.Errorf("expected ~0.333, got %f", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "expected 3") {
		t.Errorf("expected 1 warning mentioning 'expected 3', got %v", warnings)
	}
}

func TestContextPrecision_UnparseableJSONErrors(t *testing.T) {
	j := NewJudge(&scriptedCompleter{responses: []string{`not json`}})
	q := Question{ID: "q", Question: "why?", Language: "en"}
	contents := []string{"c1", "c2", "c3"}

	got, warnings, err := j.contextPrecision(context.Background(), q, contents)
	if err == nil {
		t.Fatal("expected error for unparseable JSON")
	}
	if got != 0 {
		t.Errorf("expected 0 on error, got %f", got)
	}
	if warnings != nil {
		t.Errorf("expected nil warnings on error, got %v", warnings)
	}
}

// --- W4-R5: coverage judge over optional expected_points ---

func TestJudgeEvaluate_CoverageScoresAgainstExpectedPoints(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		`{"claims":[]}`,
		`{"score":"3","reasoning":"ok"}`,
		`{"covered":[true,false,true]}`,
	}}
	q := Question{
		ID: "q", Question: "why?", Language: "en",
		ExpectedPoints: []string{"point A", "point B", "point C"},
	}

	got := NewJudge(completer).Evaluate(context.Background(), q, "answer", nil, nil)

	if got.Coverage == nil {
		t.Fatal("expected non-nil Coverage")
	}
	if *got.Coverage < 0.666 || *got.Coverage > 0.667 {
		t.Errorf("expected coverage ~0.666 (2/3), got %f", *got.Coverage)
	}
	if len(got.JudgeErrors) != 0 {
		t.Errorf("expected no errors, got %v", got.JudgeErrors)
	}
}

func TestCoverage_TruncatesExtraBooleansWithWarning(t *testing.T) {
	// 4 booleans returned for 3 expected points.
	j := NewJudge(&scriptedCompleter{responses: []string{`{"covered":[true,true,false,true]}`}})
	q := Question{ID: "q", Question: "why?", Language: "en", ExpectedPoints: []string{"a", "b", "c"}}

	got, warnings, err := j.coverage(context.Background(), q, "answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got < 0.666 || got > 0.667 {
		t.Errorf("expected ~0.666 (2/3), got %f", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "expected 3") {
		t.Errorf("expected 1 warning mentioning 'expected 3', got %v", warnings)
	}
}

func TestCoverage_PadsMissingBooleansWithWarning(t *testing.T) {
	j := NewJudge(&scriptedCompleter{responses: []string{`{"covered":[true]}`}})
	q := Question{ID: "q", Question: "why?", Language: "en", ExpectedPoints: []string{"a", "b", "c"}}

	got, warnings, err := j.coverage(context.Background(), q, "answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got < 0.333 || got > 0.334 {
		t.Errorf("expected ~0.333 (1/3), got %f", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "expected 3") {
		t.Errorf("expected 1 warning mentioning 'expected 3', got %v", warnings)
	}
}

func TestCoverage_UnparseableJSONErrors(t *testing.T) {
	j := NewJudge(&scriptedCompleter{responses: []string{`not json`}})
	q := Question{ID: "q", Question: "why?", Language: "en", ExpectedPoints: []string{"a"}}

	got, warnings, err := j.coverage(context.Background(), q, "answer")
	if err == nil {
		t.Fatal("expected error for unparseable JSON")
	}
	if got != 0 {
		t.Errorf("expected 0 on error, got %f", got)
	}
	if warnings != nil {
		t.Errorf("expected nil warnings on error, got %v", warnings)
	}
}

func TestJudgeEvaluate_NoExpectedPointsSkipsCoverageAndDoesNotCallCompleter(t *testing.T) {
	// Only 2 scripted responses (faithfulness, answer_relevance); no
	// contents so context_precision is also skipped. If coverage were
	// called anyway, the completer would run out of responses and error,
	// which would surface as a JudgeError — asserting zero errors proves
	// no coverage call happened.
	completer := &scriptedCompleter{responses: []string{
		`{"claims":[]}`,
		`{"score":"3","reasoning":"ok"}`,
	}}
	q := Question{ID: "q", Question: "why?", Language: "en"} // no ExpectedPoints

	got := NewJudge(completer).Evaluate(context.Background(), q, "answer", nil, nil)

	if got.Coverage != nil {
		t.Errorf("expected nil Coverage when ExpectedPoints is empty, got %+v", got.Coverage)
	}
	if len(got.JudgeErrors) != 0 {
		t.Errorf("expected no errors, got %v", got.JudgeErrors)
	}
	if completer.calls != 2 {
		t.Errorf("expected exactly 2 completer calls (faithfulness + answer_relevance), got %d — coverage must not have been called", completer.calls)
	}
}

func TestJudgeEvaluate_ContextPrecisionMismatchProducesWarningNotError(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		`{"claims":[]}`,
		`{"score":"3","reasoning":"ok"}`,
		`{"relevant":[true,true,false,true]}`,
	}}
	chunks := []RetrievedChunk{{FileID: "f1"}, {FileID: "f2"}, {FileID: "f3"}}
	contents := []string{"c1", "c2", "c3"}
	q := Question{ID: "q", Question: "why?", Language: "en"}

	got := NewJudge(completer).Evaluate(context.Background(), q, "answer", chunks, contents)

	if got.ContextPrecision == nil || *got.ContextPrecision < 0.666 || *got.ContextPrecision > 0.667 {
		t.Errorf("expected precision ~0.666, got %+v", got.ContextPrecision)
	}
	if len(got.JudgeErrors) != 0 {
		t.Errorf("expected no errors, got %v", got.JudgeErrors)
	}
	if len(got.JudgeWarnings) != 1 || !strings.Contains(got.JudgeWarnings[0], "expected 3") {
		t.Errorf("expected 1 warning, got %v", got.JudgeWarnings)
	}
}
