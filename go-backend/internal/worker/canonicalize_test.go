package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/canonicalize"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kg"
)

type fakeCanonStore struct {
	ents   []kg.CanonEntityRow
	merges int
}

func (f *fakeCanonStore) EntitiesForCanonicalization(context.Context, string) ([]kg.CanonEntityRow, error) {
	return f.ents, nil
}
func (f *fakeCanonStore) MergeEntities(context.Context, string, int64, []int64, []string) error {
	f.merges++
	return nil
}

type mapReader map[string]string

func (m mapReader) GetSiteConfigValue(_ context.Context, k string) (*string, error) {
	if v, ok := m[k]; ok {
		return &v, nil
	}
	return nil, nil
}

func okEmbed(embs [][]float64) func(string) canonicalize.EmbedFunc {
	return func(string) canonicalize.EmbedFunc {
		return func(context.Context, []string) ([][]float64, error) { return embs, nil }
	}
}
func confirmAll(string) canonicalize.ConfirmFunc {
	return func(_ context.Context, _ []canonicalize.CanonEntity, p []canonicalize.Pair) ([]canonicalize.Pair, error) {
		return p, nil
	}
}

func TestCanonicalizeHandler_GateOffNoOp(t *testing.T) {
	store := &fakeCanonStore{ents: []kg.CanonEntityRow{{ID: 1, Name: "A", Type: "org"}, {ID: 2, Name: "A", Type: "org"}}}
	deps := CanonicalizeDeps{
		Store:      store,
		Reader:     mapReader{},
		EmbedFor:   okEmbed([][]float64{{1, 0}, {1, 0}}),
		ConfirmFor: confirmAll,
		Publish:    func(context.Context, string) {},
	}
	h := NewEntityCanonicalizeHandler(deps)
	payload, _ := json.Marshal(jobs.EntityCanonicalizePayload{KbID: "kb-1"})
	if err := h(context.Background(), asynq.NewTask(jobs.TypeEntityCanonicalizeKB, payload)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if store.merges != 0 {
		t.Errorf("gate off must not merge, got %d", store.merges)
	}
}

func TestCanonicalizeHandler_GateOnRunsAndPublishes(t *testing.T) {
	store := &fakeCanonStore{ents: []kg.CanonEntityRow{
		{ID: 1, Name: "PPM", Type: "org", Degree: 1},
		{ID: 2, Name: "PPM-Team", Type: "org", Degree: 5},
	}}
	published := false
	deps := CanonicalizeDeps{
		Store:      store,
		Reader:     mapReader{"kg_canonicalization_enabled": "true", "kg_canonicalization_threshold": "0.5"},
		EmbedFor:   okEmbed([][]float64{{1, 0}, {0.99, 0.01}}),
		ConfirmFor: confirmAll,
		Publish:    func(context.Context, string) { published = true },
	}
	h := NewEntityCanonicalizeHandler(deps)
	payload, _ := json.Marshal(jobs.EntityCanonicalizePayload{KbID: "kb-1"})
	if err := h(context.Background(), asynq.NewTask(jobs.TypeEntityCanonicalizeKB, payload)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if store.merges != 1 {
		t.Errorf("gate on should merge the confirmed pair, got %d", store.merges)
	}
	if !published {
		t.Errorf("should publish graph_changed on success")
	}
}
