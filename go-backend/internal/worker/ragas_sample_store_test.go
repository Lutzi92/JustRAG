package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/ragassamples"
)

// fakeSampleStore captures the rows the handler writes. Insert may be called
// from the handler goroutine only, but the mutex keeps -race honest if that
// ever changes.
type fakeSampleStore struct {
	mu      sync.Mutex
	rows    []ragassamples.Sample
	insertE error
}

func (f *fakeSampleStore) Insert(_ context.Context, s ragassamples.Sample) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, s)
	return f.insertE
}

func (f *fakeSampleStore) DailyStats(context.Context, time.Time) (map[string]ragassamples.DailyStats, error) {
	return nil, nil
}

func (f *fakeSampleStore) Prune(context.Context, time.Time) (int64, error) { return 0, nil }

func (f *fakeSampleStore) snapshot() []ragassamples.Sample {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ragassamples.Sample(nil), f.rows...)
}

// namedCompleter is a fakeJudgeCompleter that also reports the model its
// judge prompts ran against, exercising the judgeModelNamer path the
// production per-sample completer implements.
type namedCompleter struct {
	*fakeJudgeCompleter
	model string
}

func (c namedCompleter) JudgeModel(context.Context) string { return c.model }

func happyCompleter() *fakeJudgeCompleter {
	return &fakeJudgeCompleter{
		responses: map[string]string{
			"claims":   `{"claims":[{"text":"a","supported":true}]}`,
			"score":    `{"score":5,"reasoning":"perfect"}`,
			"relevant": `{"relevant":[true]}`,
		},
	}
}

func samplePayload(t *testing.T) *asynq.Task {
	t.Helper()
	body, err := json.Marshal(jobs.RAGASSamplePayload{
		KbID:      "11111111-1111-1111-1111-111111111111",
		MessageID: "22222222-2222-2222-2222-222222222222",
		Language:  "en",
		Question:  "What is the notice period?",
		Answer:    "Six weeks.",
		Chunks: []jobs.RAGASSampleChunk{
			{FileID: "f1", Score: 0.9, Content: "the notice period is six weeks"},
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return asynq.NewTask(jobs.TypeRAGASSample, body)
}

// Mutation: drop the store.Insert call from runJudge → this fails. Without
// the row the sampler stays Prometheus-only, which is the gap migration 0072
// exists to close.
func TestRAGASSampleHandler_WritesRow(t *testing.T) {
	store := &fakeSampleStore{}
	handler := NewRAGASSampleHandler(namedCompleter{happyCompleter(), "judge-model-x"}, store)

	if err := handler(context.Background(), samplePayload(t)); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	rows := store.snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows written = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.KbID == nil || *row.KbID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("kb_id = %v, want the payload's KbID", row.KbID)
	}
	if row.MessageID == nil || *row.MessageID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("message_id = %v, want the payload's MessageID", row.MessageID)
	}
	if row.Faithfulness == nil || row.AnswerRelevance == nil || row.ContextPrecision == nil {
		t.Fatalf("all three scores should be set, got f=%v ar=%v cp=%v",
			row.Faithfulness, row.AnswerRelevance, row.ContextPrecision)
	}
	if *row.Faithfulness != 1.0 {
		t.Errorf("faithfulness = %v, want 1.0", *row.Faithfulness)
	}
	// Coverage is always nil on the production-sampling path: the judge only
	// scores it when the question carries ExpectedPoints, which a sampled
	// production turn never does. The column exists for a future golden-point
	// source; a non-nil value here would mean the sampler invented one.
	if row.Coverage != nil {
		t.Errorf("coverage = %v, want nil (no golden points on a sampled turn)", *row.Coverage)
	}
	if row.JudgeModel != "judge-model-x" {
		t.Errorf("judge_model = %q, want the completer's model", row.JudgeModel)
	}
	if len(row.JudgeErrors) != 0 {
		t.Errorf("judge_errors = %v, want none on the happy path", row.JudgeErrors)
	}
	if row.SampledAt.IsZero() {
		t.Error("sampled_at is zero — the row would land with a meaningless timestamp")
	}
}

// A judge that fails every prompt still produces a row: the nil scores plus
// the recorded errors are how an operator sees that the judge model, not the
// answer, is what broke.
func TestRAGASSampleHandler_JudgeFailure_WritesRowWithNilScoresAndErrors(t *testing.T) {
	store := &fakeSampleStore{}
	completer := &fakeJudgeCompleter{err: errors.New("simulated network error")}
	handler := NewRAGASSampleHandler(completer, store)

	if err := handler(context.Background(), samplePayload(t)); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	rows := store.snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows written = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Faithfulness != nil || row.AnswerRelevance != nil || row.ContextPrecision != nil {
		t.Errorf("scores should all be nil, got f=%v ar=%v cp=%v",
			row.Faithfulness, row.AnswerRelevance, row.ContextPrecision)
	}
	if len(row.JudgeErrors) == 0 {
		t.Error("judge_errors is empty — the failure reason is lost")
	}
}

// A failing INSERT must not fail the task: asynq would retry it, and every
// retry re-runs the three (paid) judge prompts to write the same row.
func TestRAGASSampleHandler_StoreError_DoesNotFailTask(t *testing.T) {
	store := &fakeSampleStore{insertE: errors.New("db down")}
	handler := NewRAGASSampleHandler(happyCompleter(), store)

	if err := handler(context.Background(), samplePayload(t)); err != nil {
		t.Errorf("store error should be swallowed, got: %v", err)
	}
}

// A nil store is the "persistence not wired" deployment (no main pool in this
// worker): the handler must still emit Prometheus and must not panic.
func TestRAGASSampleHandler_NilStore_NoPanic(t *testing.T) {
	handler := NewRAGASSampleHandler(happyCompleter(), nil)
	if err := handler(context.Background(), samplePayload(t)); err != nil {
		t.Errorf("nil store: got %v, want nil", err)
	}
}

// A completer that cannot name its model (the test factory's plain fake, or a
// resolver failure in production) writes an empty judge_model rather than
// blocking the row.
func TestRAGASSampleHandler_UnnamedCompleter_EmptyJudgeModel(t *testing.T) {
	store := &fakeSampleStore{}
	handler := NewRAGASSampleHandler(happyCompleter(), store)

	if err := handler(context.Background(), samplePayload(t)); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	rows := store.snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows written = %d, want 1", len(rows))
	}
	if rows[0].JudgeModel != "" {
		t.Errorf("judge_model = %q, want empty", rows[0].JudgeModel)
	}
}
