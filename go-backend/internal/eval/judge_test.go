package eval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/justrag/go-backend/internal/observability"
)

type scriptedCompleter struct {
	responses []string
	// calls counts scripted responses actually consumed (pre-existing
	// semantics — most tests rely on it to mean "N successful reads off
	// responses").
	calls int
	// invocations counts every Complete call, including one made after the
	// script is exhausted (which returns an error WITHOUT incrementing
	// calls). Retry-boundedness tests ("never a third call") must assert on
	// this, not on calls: a caller that ignores the "no more scripted
	// responses" error and calls a third time would leave calls unchanged
	// (still 2, since nothing new was consumed) while invocations would be
	// 3 — the whole point of the assertion. Verified by mutation (task-4
	// review B1): adding a real third completeJSON call left calls-based
	// assertions green.
	invocations int
	// prompts records the user prompt passed on every Complete call, in
	// order (including a call that runs out of scripted responses), so a
	// retry test can inspect what the second call actually asked (W6-R4).
	prompts []string
}

func (s *scriptedCompleter) Complete(_ context.Context, prompt, _ string) (string, error) {
	s.invocations++
	s.prompts = append(s.prompts, prompt)
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
		// Valid JSON but an unparseable score — deliberately NOT a decoder
		// failure, since W6-R4 gives a decoder failure one bounded retry
		// that would consume the next scripted response (meant for
		// context_precision below) rather than leaving this a single-call
		// failure.
		`{"score":"abc","reasoning":"x"}`,
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

	got, _, err := j.answerRelevance(context.Background(), q, "answer")
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

	got, _, err := j.answerRelevance(context.Background(), q, "answer")
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

	_, _, err := j.answerRelevance(context.Background(), q, "answer")
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
	// A single scripted response that fails to decode now triggers W6-R4's
	// bounded retry (any unmarshalStrict failure, not just a
	// brace-balanced-but-invalid object) — the retry's own call then runs
	// out of scripted responses and fails too, so the retry was genuinely
	// ATTEMPTED and the warning (N7) is expected here, not nil.
	completer := &scriptedCompleter{responses: []string{`not json`}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}
	contents := []string{"c1", "c2", "c3"}

	got, warnings, err := j.contextPrecision(context.Background(), q, contents)
	if err == nil {
		t.Fatal("expected error for unparseable JSON")
	}
	if got != 0 {
		t.Errorf("expected 0 on error, got %f", got)
	}
	if len(warnings) != 1 || warnings[0] != "retry:context_precision" {
		t.Errorf("expected [retry:context_precision] (a retry was attempted), got %v", warnings)
	}
	if completer.invocations != 2 {
		t.Errorf("expected exactly 2 calls (the retry), got %d", completer.invocations)
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
	// Same reasoning as TestContextPrecision_UnparseableJSONErrors: the
	// single-response fixture now exercises the bounded retry rather than a
	// single unretried failure, so a retry warning (N7) is expected.
	completer := &scriptedCompleter{responses: []string{`not json`}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en", ExpectedPoints: []string{"a"}}

	got, warnings, err := j.coverage(context.Background(), q, "answer")
	if err == nil {
		t.Fatal("expected error for unparseable JSON")
	}
	if got != 0 {
		t.Errorf("expected 0 on error, got %f", got)
	}
	if len(warnings) != 1 || warnings[0] != "retry:coverage" {
		t.Errorf("expected [retry:coverage] (a retry was attempted), got %v", warnings)
	}
	if completer.invocations != 2 {
		t.Errorf("expected exactly 2 calls (the retry), got %d", completer.invocations)
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

// --- fix wave, item 1: a null score is a missing verdict, not a 1 ---

func TestAnswerRelevance_NullScoreErrors(t *testing.T) {
	// `json.Unmarshal("null", &float64)` succeeds and leaves the zero value,
	// so a judge that answered {"score":null} used to be scored 0 → clamped
	// to the Likert minimum 1 → recorded as a real "barely relevant" verdict.
	// It is a missing verdict: drop the sample instead.
	j := NewJudge(&scriptedCompleter{responses: []string{`{"score":null,"reasoning":"x"}`}})
	q := Question{ID: "q", Question: "why?", Language: "en"}

	if _, _, err := j.answerRelevance(context.Background(), q, "answer"); err == nil {
		t.Fatal("expected an error for a null score")
	}
}

func TestJudgeEvaluate_NullScoreLeavesAnswerRelevanceNil(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		`{"claims":[]}`,
		`{"score":null,"reasoning":"x"}`,
	}}
	q := Question{ID: "q", Question: "why?", Language: "en"}

	got := NewJudge(completer).Evaluate(context.Background(), q, "answer", nil, nil)

	if got.AnswerRelevance != nil {
		t.Errorf("expected nil AnswerRelevance, got %v", *got.AnswerRelevance)
	}
	if len(got.JudgeErrors) != 1 || !strings.Contains(got.JudgeErrors[0], "answer_relevance") {
		t.Errorf("expected 1 answer_relevance error, got %v", got.JudgeErrors)
	}
}

func TestParseJudgeScore_RejectsNullAndNonNumbers(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[]`, `true`, `"abc"`, ``} {
		if _, err := parseJudgeScore([]byte(raw)); err == nil {
			t.Errorf("expected an error for %q", raw)
		}
	}
}

// --- fix wave, item 5: coverage is safe when called directly ---

func TestCoverage_NoExpectedPointsErrorsWithoutCallingTheCompleter(t *testing.T) {
	// Evaluate skips coverage when ExpectedPoints is empty; this guards the
	// method itself, whose covered/len(points) would be 0/0 = NaN. An error
	// (rather than a 0.0) keeps a misuse out of mean_coverage.
	completer := &scriptedCompleter{responses: []string{`{"covered":[true]}`}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}

	if _, _, err := j.coverage(context.Background(), q, "answer"); err == nil {
		t.Fatal("expected an error when there are no expected points")
	}
	if completer.calls != 0 {
		t.Errorf("expected no completer call, got %d", completer.calls)
	}
}

// --- W6-R4 / W6-R17: judge JSON-hygiene retry ---
//
// The Wave-5 fix wave established that live judge parse failures are
// brace-balanced objects the decoder rejects (a raw newline or unescaped
// quote inside a string, or a trailing comma) — not truncation, not a code
// fence. The judge now re-asks exactly once with the decoder error appended,
// deterministically, and gives up after that: a second failure must not
// trigger a third call.

// faithfulnessInvalidJSON is brace-balanced (so unmarshalStrict does not
// report it as truncated) but has a trailing comma before the closing `]`,
// which the standard-library decoder rejects.
const faithfulnessInvalidJSON = `{"claims":[{"text":"a","supported":true},]}`

func TestFaithfulnessRetry_InvalidThenValidSucceeds(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		faithfulnessInvalidJSON,
		`{"claims":[{"text":"a","supported":true}]}`,
	}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}

	got, warnings, err := j.faithfulness(context.Background(), q, "answer", "context")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1.0 {
		t.Errorf("expected 1.0, got %f", got)
	}
	if len(warnings) != 1 || warnings[0] != "retry:faithfulness" {
		t.Errorf("expected [retry:faithfulness], got %v", warnings)
	}
	if completer.invocations != 2 {
		t.Errorf("expected exactly 2 calls, got %d", completer.invocations)
	}
	assertRetryPromptNamesTheFailure(t, completer, faithfulnessInvalidJSON, &struct {
		Claims []struct {
			Text      string `json:"text"`
			Supported bool   `json:"supported"`
		} `json:"claims"`
	}{})
}

func TestFaithfulnessRetry_InvalidTwiceFailsAfterOneRetry(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{faithfulnessInvalidJSON, faithfulnessInvalidJSON}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}

	_, warnings, err := j.faithfulness(context.Background(), q, "answer", "context")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "after retry") {
		t.Errorf("expected error to mention 'after retry', got %v", err)
	}
	// N7: a retry was ATTEMPTED even though it then failed too, so the
	// warning must still be present alongside the "after retry" error.
	if len(warnings) != 1 || warnings[0] != "retry:faithfulness" {
		t.Errorf("expected [retry:faithfulness] alongside the error, got %v", warnings)
	}
	if completer.invocations != 2 {
		t.Errorf("expected exactly 2 calls (never 3), got %d", completer.invocations)
	}
}

func TestFaithfulnessRetry_ValidFirstNeedsNoRetry(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{`{"claims":[{"text":"a","supported":true}]}`}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}

	_, warnings, err := j.faithfulness(context.Background(), q, "answer", "context")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
	if completer.invocations != 1 {
		t.Errorf("expected exactly 1 call, got %d", completer.invocations)
	}
}

// transportErrorCompleter always fails the underlying call with a transport
// error (never a decoder failure): completeJSON's sketch treats that as
// non-retryable ("return false, err" before any retry logic runs). Untested
// before task-4 review N2 — mutation-verified: making the first call retry
// on a transport error left the suite green without this test.
type transportErrorCompleter struct {
	calls int
}

func (c *transportErrorCompleter) Complete(_ context.Context, _, _ string) (string, error) {
	c.calls++
	return "", errorsNew("connection refused")
}

func TestFaithfulnessRetry_TransportErrorOnFirstCallDoesNotRetry(t *testing.T) {
	completer := &transportErrorCompleter{}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}

	_, _, err := j.faithfulness(context.Background(), q, "answer", "context")
	if err == nil {
		t.Fatal("expected an error")
	}
	if completer.calls != 1 {
		t.Errorf("expected exactly 1 call (a transport error must not retry), got %d", completer.calls)
	}
}

// answerRelevanceInvalidJSON has a trailing comma after the score field.
const answerRelevanceInvalidJSON = `{"score":"5",}`

func TestAnswerRelevanceRetry_InvalidThenValidSucceeds(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		answerRelevanceInvalidJSON,
		`{"score":"5","reasoning":"x"}`,
	}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}

	got, warnings, err := j.answerRelevance(context.Background(), q, "answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1.0 {
		t.Errorf("expected 1.0, got %f", got)
	}
	if len(warnings) != 1 || warnings[0] != "retry:answer_relevance" {
		t.Errorf("expected [retry:answer_relevance], got %v", warnings)
	}
	if completer.invocations != 2 {
		t.Errorf("expected exactly 2 calls, got %d", completer.invocations)
	}
	assertRetryPromptNamesTheFailure(t, completer, answerRelevanceInvalidJSON, &struct {
		Score     json.RawMessage `json:"score"`
		Reasoning string          `json:"reasoning"`
	}{})
}

func TestAnswerRelevanceRetry_InvalidTwiceFailsAfterOneRetry(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{answerRelevanceInvalidJSON, answerRelevanceInvalidJSON}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}

	_, warnings, err := j.answerRelevance(context.Background(), q, "answer")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "after retry") {
		t.Errorf("expected error to mention 'after retry', got %v", err)
	}
	if len(warnings) != 1 || warnings[0] != "retry:answer_relevance" {
		t.Errorf("expected [retry:answer_relevance] alongside the error, got %v", warnings)
	}
	if completer.invocations != 2 {
		t.Errorf("expected exactly 2 calls (never 3), got %d", completer.invocations)
	}
}

func TestAnswerRelevanceRetry_ValidFirstNeedsNoRetry(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{`{"score":"5","reasoning":"x"}`}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}

	_, warnings, err := j.answerRelevance(context.Background(), q, "answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
	if completer.invocations != 1 {
		t.Errorf("expected exactly 1 call, got %d", completer.invocations)
	}
}

// TestAnswerRelevanceRetry_RetriedThenScoreParseFailureStillWarns pins the
// task-4 review's N3 finding: a retry that DID happen (the first response
// was a decoder failure) but whose corrected JSON then fails the downstream
// score parse (not a decoder failure — parseJudgeScore, not unmarshalStrict)
// must still surface "retry:answer_relevance" in the returned warnings
// alongside the error. Before the fix, the warning was built AFTER the
// score-parse error return and so was silently dropped on this path.
func TestAnswerRelevanceRetry_RetriedThenScoreParseFailureStillWarns(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		answerRelevanceInvalidJSON,        // decoder failure -> triggers the retry
		`{"score":"xyz","reasoning":"y"}`, // valid JSON, but an unparseable score
	}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}

	_, warnings, err := j.answerRelevance(context.Background(), q, "answer")
	if err == nil {
		t.Fatal("expected an error (unparseable score)")
	}
	if len(warnings) != 1 || warnings[0] != "retry:answer_relevance" {
		t.Errorf("expected [retry:answer_relevance] preserved alongside the error, got %v", warnings)
	}
	if completer.invocations != 2 {
		t.Errorf("expected exactly 2 calls (the retry, not a second retry), got %d", completer.invocations)
	}
}

// TestJudgeEvaluate_RetriedThenScoreParseFailureWarningReachesJudgeWarnings
// is the Evaluate-level half: JudgeWarnings must carry the retry marker even
// though AnswerRelevance itself is nil (the metric failed).
func TestJudgeEvaluate_RetriedThenScoreParseFailureWarningReachesJudgeWarnings(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		answerRelevanceInvalidJSON,
		`{"score":"xyz","reasoning":"y"}`,
	}}
	q := Question{ID: "q", Question: "why?", Language: "en"}
	// Mismatched chunks/contents (1 vs 0) makes Evaluate take the
	// "chunk/content length mismatch" branch for faithfulness, which
	// records an error WITHOUT calling the completer — so the two scripted
	// responses above are reserved for answer_relevance, the metric this
	// test targets.
	chunks := []RetrievedChunk{{FileID: "f1"}}

	got := NewJudge(completer).Evaluate(context.Background(), q, "answer", chunks, nil)

	if got.AnswerRelevance != nil {
		t.Errorf("expected nil AnswerRelevance (score parse failed), got %v", *got.AnswerRelevance)
	}
	found := false
	for _, w := range got.JudgeWarnings {
		if w == "retry:answer_relevance" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected JudgeWarnings to contain retry:answer_relevance even though the metric errored, got %v", got.JudgeWarnings)
	}
}

// coverageInvalidJSON has a trailing comma inside the array.
const coverageInvalidJSON = `{"covered":[true,]}`

func TestCoverageRetry_InvalidThenValidSucceeds(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		coverageInvalidJSON,
		`{"covered":[true]}`,
	}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en", ExpectedPoints: []string{"p1"}}

	got, warnings, err := j.coverage(context.Background(), q, "answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1.0 {
		t.Errorf("expected 1.0, got %f", got)
	}
	if len(warnings) != 1 || warnings[0] != "retry:coverage" {
		t.Errorf("expected [retry:coverage], got %v", warnings)
	}
	if completer.invocations != 2 {
		t.Errorf("expected exactly 2 calls, got %d", completer.invocations)
	}
	assertRetryPromptNamesTheFailure(t, completer, coverageInvalidJSON, &struct {
		Covered []bool `json:"covered"`
	}{})
}

func TestCoverageRetry_InvalidTwiceFailsAfterOneRetry(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{coverageInvalidJSON, coverageInvalidJSON}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en", ExpectedPoints: []string{"p1"}}

	_, warnings, err := j.coverage(context.Background(), q, "answer")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "after retry") {
		t.Errorf("expected error to mention 'after retry', got %v", err)
	}
	if len(warnings) != 1 || warnings[0] != "retry:coverage" {
		t.Errorf("expected [retry:coverage] alongside the error, got %v", warnings)
	}
	if completer.invocations != 2 {
		t.Errorf("expected exactly 2 calls (never 3), got %d", completer.invocations)
	}
}

func TestCoverageRetry_ValidFirstNeedsNoRetry(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{`{"covered":[true]}`}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en", ExpectedPoints: []string{"p1"}}

	_, warnings, err := j.coverage(context.Background(), q, "answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
	if completer.invocations != 1 {
		t.Errorf("expected exactly 1 call, got %d", completer.invocations)
	}
}

// contextPrecisionInvalidJSON has a trailing comma inside the array. This
// path had no dedicated retry-warning test (task-4 review N1): the other
// three metrics' "retry:<metric>" strings were each pinned, leaving
// context_precision the one warning constant of the four with no test able
// to catch a typo in it.
const contextPrecisionInvalidJSON = `{"relevant":[true,]}`

func TestContextPrecisionRetry_InvalidThenValidSucceeds(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		contextPrecisionInvalidJSON,
		`{"relevant":[true]}`,
	}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}
	contents := []string{"c1"}

	got, warnings, err := j.contextPrecision(context.Background(), q, contents)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1.0 {
		t.Errorf("expected 1.0, got %f", got)
	}
	if len(warnings) != 1 || warnings[0] != "retry:context_precision" {
		t.Errorf("expected [retry:context_precision], got %v", warnings)
	}
	if completer.invocations != 2 {
		t.Errorf("expected exactly 2 calls, got %d", completer.invocations)
	}
	assertRetryPromptNamesTheFailure(t, completer, contextPrecisionInvalidJSON, &struct {
		Relevant []bool `json:"relevant"`
	}{})
}

func TestContextPrecisionRetry_InvalidTwiceFailsAfterOneRetry(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{contextPrecisionInvalidJSON, contextPrecisionInvalidJSON}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}
	contents := []string{"c1"}

	_, warnings, err := j.contextPrecision(context.Background(), q, contents)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "after retry") {
		t.Errorf("expected error to mention 'after retry', got %v", err)
	}
	if len(warnings) != 1 || warnings[0] != "retry:context_precision" {
		t.Errorf("expected [retry:context_precision] alongside the error, got %v", warnings)
	}
	if completer.invocations != 2 {
		t.Errorf("expected exactly 2 calls (never 3), got %d", completer.invocations)
	}
}

func TestContextPrecisionRetry_ValidFirstNeedsNoRetry(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{`{"relevant":[true]}`}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}
	contents := []string{"c1"}

	_, warnings, err := j.contextPrecision(context.Background(), q, contents)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
	if completer.invocations != 1 {
		t.Errorf("expected exactly 1 call, got %d", completer.invocations)
	}
}

// TestJudgeEvaluate_FaithfulnessRetryFailureRecordsErrorWithAfterRetry pins
// the JudgeErrors shape at the Evaluate level: a metric that fails twice
// surfaces as one "<metric>: ... after retry ..." entry.
func TestJudgeEvaluate_FaithfulnessRetryFailureRecordsErrorWithAfterRetry(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{faithfulnessInvalidJSON, faithfulnessInvalidJSON}}
	q := Question{ID: "q", Question: "why?", Language: "en"}
	chunks := []RetrievedChunk{{FileID: "f1"}}
	contents := []string{"c1"}

	got := NewJudge(completer).Evaluate(context.Background(), q, "answer", chunks, contents)

	foundError := false
	for _, e := range got.JudgeErrors {
		if strings.HasPrefix(e, "faithfulness:") && strings.Contains(e, "after retry") {
			foundError = true
		}
	}
	if !foundError {
		t.Errorf("expected a faithfulness error containing 'after retry', got %v", got.JudgeErrors)
	}
	// N7: the retry warning must be present alongside the error — a retry
	// was attempted (and failed), which is different from "no retry".
	foundWarning := false
	for _, w := range got.JudgeWarnings {
		if w == "retry:faithfulness" {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected JudgeWarnings to contain retry:faithfulness alongside the error, got %v", got.JudgeWarnings)
	}
}

// TestFaithfulnessRetry_IncrementsMetricFromTheJudgePath asserts the
// production call site (judge.go's completeJSON -> observability.
// RecordJudgeRetry), not just the RecordJudgeRetry helper in isolation
// (which internal/observability/metrics_test.go already covers). Reading
// the counter through JudgeRetryTotalForTest before/after proves the judge
// actually calls it, with the right label, exactly once per failed first
// parse (task-4 review B2).
func TestFaithfulnessRetry_IncrementsMetricFromTheJudgePath(t *testing.T) {
	counter := observability.JudgeRetryTotalForTest()
	before := testutil.ToFloat64(counter.WithLabelValues("faithfulness"))

	completer := &scriptedCompleter{responses: []string{
		faithfulnessInvalidJSON,
		`{"claims":[{"text":"a","supported":true}]}`,
	}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}
	if _, _, err := j.faithfulness(context.Background(), q, "answer", "context"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after := testutil.ToFloat64(counter.WithLabelValues("faithfulness"))
	if after != before+1 {
		t.Errorf("expected faithfulness retry counter +1, got before=%v after=%v", before, after)
	}
}

// TestFaithfulnessRetry_CleanRunDoesNotIncrementMetric is the negative half
// of the above: a first response that parses cleanly must not touch the
// counter at all.
func TestFaithfulnessRetry_CleanRunDoesNotIncrementMetric(t *testing.T) {
	counter := observability.JudgeRetryTotalForTest()
	before := testutil.ToFloat64(counter.WithLabelValues("faithfulness"))

	completer := &scriptedCompleter{responses: []string{`{"claims":[{"text":"a","supported":true}]}`}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "why?", Language: "en"}
	if _, _, err := j.faithfulness(context.Background(), q, "answer", "context"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after := testutil.ToFloat64(counter.WithLabelValues("faithfulness"))
	if after != before {
		t.Errorf("expected no change on a clean run, got before=%v after=%v", before, after)
	}
}

// TestFaithfulnessRetry_GermanQuestionGetsGermanRetryInstruction pins the
// task-4 review's N4 localization fix: q.Language == "de" must produce a
// German correction instruction (matching every SystemPrompt in
// internal/prompts, which are already bilingual), not the English sketch
// text tacked onto a German exchange.
func TestFaithfulnessRetry_GermanQuestionGetsGermanRetryInstruction(t *testing.T) {
	completer := &scriptedCompleter{responses: []string{
		faithfulnessInvalidJSON,
		`{"claims":[{"text":"a","supported":true}]}`,
	}}
	j := NewJudge(completer)
	q := Question{ID: "q", Question: "warum?", Language: "de"}

	if _, _, err := j.faithfulness(context.Background(), q, "answer", "context"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completer.prompts) != 2 {
		t.Fatalf("expected 2 recorded prompts, got %d", len(completer.prompts))
	}
	retryPrompt := completer.prompts[1]
	if !strings.Contains(retryPrompt, "kein gültiges JSON") {
		t.Errorf("retry prompt missing German instruction: %s", retryPrompt)
	}
	if strings.Contains(retryPrompt, "was not valid JSON") {
		t.Errorf("retry prompt leaked the English instruction for a German question: %s", retryPrompt)
	}
	var probe struct {
		Claims []struct {
			Text      string `json:"text"`
			Supported bool   `json:"supported"`
		} `json:"claims"`
	}
	wantErr := unmarshalStrict(faithfulnessInvalidJSON, &probe)
	if wantErr == nil {
		t.Fatal("test payload was not actually invalid JSON")
	}
	if !strings.Contains(retryPrompt, wantErr.Error()) {
		t.Errorf("German retry prompt still missing the decoder error text %q: %s", wantErr.Error(), retryPrompt)
	}
}

// assertRetryPromptNamesTheFailure checks that the second recorded prompt
// (the retry) tells the model its previous reply was not valid JSON and
// carries the actual decoder diagnostic — computed independently via
// unmarshalStrict against the same invalid payload, rather than duplicating
// the exact wording, so this test does not silently pass if the wording
// changes but the diagnostic is dropped.
func assertRetryPromptNamesTheFailure(t *testing.T, completer *scriptedCompleter, invalidPayload string, probe any) {
	t.Helper()
	if len(completer.prompts) != 2 {
		t.Fatalf("expected 2 recorded prompts, got %d", len(completer.prompts))
	}
	retryPrompt := completer.prompts[1]
	if !strings.Contains(retryPrompt, "was not valid JSON") {
		t.Errorf("retry prompt missing 'was not valid JSON': %s", retryPrompt)
	}
	wantErr := unmarshalStrict(invalidPayload, probe)
	if wantErr == nil {
		t.Fatalf("test payload %q was not actually invalid JSON", invalidPayload)
	}
	if !strings.Contains(retryPrompt, wantErr.Error()) {
		t.Errorf("retry prompt missing decoder error text %q: %s", wantErr.Error(), retryPrompt)
	}
}
