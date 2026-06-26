package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/community"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kg"
)

type fakeCommStore struct {
	edges    []kg.CommunityEdge
	ents     []kg.CommunityEntity
	internal []kg.InternalEdgeRow
	cleared  int
	stored   int
}

func (f *fakeCommStore) EdgesForCommunities(context.Context, string) ([]kg.CommunityEdge, error) {
	return f.edges, nil
}
func (f *fakeCommStore) EntitiesForCommunities(context.Context, string, []int64) ([]kg.CommunityEntity, error) {
	return f.ents, nil
}
func (f *fakeCommStore) InternalEdgesForCommunities(context.Context, string) ([]kg.InternalEdgeRow, error) {
	return f.internal, nil
}
func (f *fakeCommStore) ClearCommunities(context.Context, string) error { f.cleared++; return nil }
func (f *fakeCommStore) StoreCommunity(context.Context, string, int, []int64, string, int) error {
	f.stored++
	return nil
}

type commMapReader map[string]string

func (m commMapReader) GetSiteConfigValue(_ context.Context, k string) (*string, error) {
	if v, ok := m[k]; ok {
		return &v, nil
	}
	return nil, nil
}

func twoTriangleStore() *fakeCommStore {
	s := &fakeCommStore{
		edges: []kg.CommunityEdge{
			{Src: 1, Dst: 2, Weight: 1}, {Src: 2, Dst: 3, Weight: 1}, {Src: 1, Dst: 3, Weight: 1},
			{Src: 4, Dst: 5, Weight: 1}, {Src: 5, Dst: 6, Weight: 1}, {Src: 4, Dst: 6, Weight: 1},
			{Src: 3, Dst: 4, Weight: 0.1},
		},
	}
	for i := int64(1); i <= 6; i++ {
		s.ents = append(s.ents, kg.CommunityEntity{ID: i, Name: "E", Type: "concept"})
	}
	return s
}

func commDeps(store *fakeCommStore, reader commMapReader, published *bool) CommunitiesDeps {
	return CommunitiesDeps{
		Store:           store,
		Reader:          reader,
		DeleteSummaries: func(context.Context, string) error { return nil },
		SummariseFor:    func(string) community.SummariseFunc { return func(context.Context, string) (string, error) { return "summary", nil } },
		EmbedFor:        func(string) community.EmbedFunc { return func(context.Context, string) ([]float64, error) { return []float64{1, 0}, nil } },
		SinkFor: func(string) community.Sink {
			return fakeSink{store}
		},
		Publish: func(context.Context, string) {
			if published != nil {
				*published = true
			}
		},
	}
}

type fakeSink struct{ s *fakeCommStore }

func (k fakeSink) InsertSummaryChunk(context.Context, string, []float64) (string, error) {
	return "chunk", nil
}
func (k fakeSink) StoreCommunity(_ context.Context, _ int, _ []int64, _ string, _ int) error {
	k.s.stored++
	return nil
}

func runCommHandler(t *testing.T, deps CommunitiesDeps) {
	t.Helper()
	h := NewKGCommunitiesBuildHandler(deps)
	payload, _ := json.Marshal(jobs.KGCommunitiesBuildPayload{KbID: "kb-1"})
	if err := h(context.Background(), asynq.NewTask(jobs.TypeKGCommunitiesBuild, payload)); err != nil {
		t.Fatalf("handler: %v", err)
	}
}

func TestKGCommunitiesHandler_GateOffNoOp(t *testing.T) {
	store := twoTriangleStore()
	runCommHandler(t, commDeps(store, commMapReader{}, nil))
	if store.cleared != 0 || store.stored != 0 {
		t.Errorf("gate off must not clear/store, got cleared=%d stored=%d", store.cleared, store.stored)
	}
}

func TestKGCommunitiesHandler_GateOnBuilds(t *testing.T) {
	store := twoTriangleStore()
	published := false
	runCommHandler(t, commDeps(store, commMapReader{"kg_communities_enabled": "true"}, &published))
	if store.cleared != 1 {
		t.Errorf("gate on must clear once, got %d", store.cleared)
	}
	if store.stored != 2 {
		t.Errorf("two-triangle graph should store 2 communities, got %d", store.stored)
	}
	if !published {
		t.Errorf("should publish on success")
	}
}
