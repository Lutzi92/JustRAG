package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/longmem"
)

type fakeMemStore struct {
	rows    []longmem.Memory
	updated map[int64][]float64
	failID  int64
}

func (f *fakeMemStore) ListByUser(_ context.Context, _ string) ([]longmem.Memory, error) {
	return f.rows, nil
}
func (f *fakeMemStore) UpdateEmbedding(_ context.Context, _ string, id int64, emb []float64) error {
	if id == f.failID {
		return errors.New("boom")
	}
	if f.updated == nil {
		f.updated = map[int64][]float64{}
	}
	f.updated[id] = emb
	return nil
}

func fakeEmbedder(_ context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i := range texts {
		out[i] = []float64{0.1, 0.2, 0.3}
	}
	return out, nil
}

func TestReEmbedUserMemory_UpdatesRows(t *testing.T) {
	store := &fakeMemStore{rows: []longmem.Memory{{ID: 1, Content: "a"}, {ID: 2, Content: "b"}}}
	h := NewReEmbedUserMemoryHandler(store, fakeEmbedder)
	payload, _ := json.Marshal(jobs.ReEmbedUserMemoryPayload{UserID: "u1"})
	if err := h(context.Background(), asynq.NewTask(jobs.TypeReEmbedUserMemory, payload)); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if len(store.updated) != 2 {
		t.Errorf("want 2 rows updated, got %d", len(store.updated))
	}
}

func TestReEmbedUserMemory_PerRowFailureDoesNotAbort(t *testing.T) {
	store := &fakeMemStore{rows: []longmem.Memory{{ID: 1, Content: "a"}, {ID: 2, Content: "b"}, {ID: 3, Content: "c"}}, failID: 2}
	h := NewReEmbedUserMemoryHandler(store, fakeEmbedder)
	payload, _ := json.Marshal(jobs.ReEmbedUserMemoryPayload{UserID: "u1"})
	if err := h(context.Background(), asynq.NewTask(jobs.TypeReEmbedUserMemory, payload)); err != nil {
		t.Fatalf("handler should not fail on per-row error: %v", err)
	}
	if len(store.updated) != 2 {
		t.Errorf("want 2 successful updates, got %d", len(store.updated))
	}
}
