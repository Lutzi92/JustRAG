package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/observability"
)

// fakeJudgeCompleter returns canned JSON responses keyed on a substring
// of the user prompt. The eval.Judge calls Complete(ctx, user, system)
// per metric (faithfulness/answer_relevance/context_precision), each
// with a distinct user-prompt shape; the substrings below pick the
// right canned reply for each call.
type fakeJudgeCompleter struct {
	responses map[string]string // substring → canned response
	err       error
}

func (f *fakeJudgeCompleter) Complete(_ context.Context, user, system string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	// Match against the system prompt — the judge's system prompts are the
	// distinguishing signal: faithfulness has "claims", answer relevance has
	// "score", context precision has "relevant".
	for needle, resp := range f.responses {
		if strings.Contains(system, needle) || strings.Contains(user, needle) {
			return resp, nil
		}
	}
	return "", errors.New("fakeJudgeCompleter: no canned response for prompt")
}

func TestRAGASSampleHandler_HappyPath_EmitsCompletedOutcome(t *testing.T) {
	completer := &fakeJudgeCompleter{
		responses: map[string]string{
			// Faithfulness prompt mentions "claims"; 1/1 supported = 1.0
			"claims": `{"claims":[{"text":"a","supported":true}]}`,
			// Answer relevance prompt mentions "score"; 5/5 → normalized 1.0
			"score": `{"score":5,"reasoning":"perfect"}`,
			// Context precision prompt mentions "relevant"; 1/1 relevant = 1.0
			"relevant": `{"relevant":[true]}`,
		},
	}

	payload := jobs.RAGASSamplePayload{
		KbID:     "kb1",
		Language: "en",
		Question: "What is the notice period?",
		Answer:   "Six weeks.",
		Chunks: []jobs.RAGASSampleChunk{
			{FileID: "f1", Score: 0.9, Content: "the notice period is six weeks"},
		},
	}
	body, _ := json.Marshal(payload)
	task := asynq.NewTask(jobs.TypeRAGASSample, body)

	counter := observability.RAGASSampleTotalForTest()
	beforeCompleted := testutil.ToFloat64(counter.WithLabelValues("completed"))

	handler := NewRAGASSampleHandler(completer)
	if err := handler(context.Background(), task); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	afterCompleted := testutil.ToFloat64(counter.WithLabelValues("completed"))
	if got := afterCompleted - beforeCompleted; got != 1 {
		t.Errorf("completed counter delta: got %v, want 1", got)
	}
}

func TestRAGASSampleHandler_AllPromptsFail_RecordsErrorOutcome(t *testing.T) {
	completer := &fakeJudgeCompleter{err: errors.New("simulated network error")}

	payload := jobs.RAGASSamplePayload{
		KbID:     "kb1",
		Language: "en",
		Question: "Q?",
		Answer:   "A.",
		Chunks: []jobs.RAGASSampleChunk{
			{FileID: "f1", Score: 0.9, Content: "c"},
		},
	}
	body, _ := json.Marshal(payload)
	task := asynq.NewTask(jobs.TypeRAGASSample, body)

	counter := observability.RAGASSampleTotalForTest()
	beforeError := testutil.ToFloat64(counter.WithLabelValues("error"))

	handler := NewRAGASSampleHandler(completer)
	// Worker handler should NOT return an error — that would cause asynq
	// to retry. Sampling is best-effort.
	if err := handler(context.Background(), task); err != nil {
		t.Errorf("handler should swallow completer errors, got: %v", err)
	}

	afterError := testutil.ToFloat64(counter.WithLabelValues("error"))
	if got := afterError - beforeError; got != 1 {
		t.Errorf("error counter delta: got %v, want 1", got)
	}
}

func TestRAGASSampleHandler_MalformedPayload_ReturnsError(t *testing.T) {
	// A malformed payload IS worth retrying — it might be a transient
	// Redis corruption (extremely rare). Returning an error preserves
	// asynq's MaxRetry semantics.
	task := asynq.NewTask(jobs.TypeRAGASSample, []byte("not json"))

	completer := &fakeJudgeCompleter{}
	handler := NewRAGASSampleHandler(completer)
	if err := handler(context.Background(), task); err == nil {
		t.Error("malformed payload should return error so asynq can retry")
	}
}
