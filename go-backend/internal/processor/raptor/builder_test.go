package raptor

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

// fakeStore is an in-memory Store for unit testing the builder
// without spinning up Postgres.
type fakeStore struct {
	leaves      []LeafRow
	listErr     error
	insertErr   error
	summaries   int32 // atomic; total number of summary rows inserted
	nextID      int32 // atomic counter for fake summary ids
	insertCalls int32 // atomic; counts calls regardless of insertErr
}

func (f *fakeStore) ListLeaves(_ context.Context, _, _ string, _ int) ([]LeafRow, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.leaves, nil
}

func (f *fakeStore) InsertSummary(_ context.Context, _, _, _ string, _ int, _ SummaryRow) (string, error) {
	atomic.AddInt32(&f.insertCalls, 1)
	if f.insertErr != nil {
		return "", f.insertErr
	}
	atomic.AddInt32(&f.summaries, 1)
	id := atomic.AddInt32(&f.nextID, 1)
	return fmt.Sprintf("sum-%d", id), nil
}

type fakeEmbedder struct {
	calls int32
}

func (e *fakeEmbedder) EmbedOne(_ context.Context, _, _ string) ([]float64, error) {
	atomic.AddInt32(&e.calls, 1)
	return []float64{0, 0}, nil
}

type fakeSummariser struct {
	calls int32
	out   string
	err   error
}

func (s *fakeSummariser) Summarise(_ context.Context, _, _, _ string, _ int, _ string) (string, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.err != nil {
		return "", s.err
	}
	if s.out == "" {
		return "summary text", nil
	}
	return s.out, nil
}

func TestBuild_SkipsBelowMinChunks(t *testing.T) {
	store := &fakeStore{leaves: []LeafRow{
		{ID: "a", Content: "x", Embedding: []float64{1, 0}},
		{ID: "b", Content: "y", Embedding: []float64{0, 1}},
	}}
	b := NewBuilder(store, &fakeEmbedder{}, &fakeSummariser{},
		Config{MinChunks: 25, MaxLevels: 4, BranchingFactor: 5, Concurrency: 1})

	stats, err := b.Build(context.Background(), BuildParams{
		KbID: "kb", FileID: "file", FileName: "n.txt", Dimensions: 2,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if stats.LevelsBuilt != 0 || stats.SummariesAdded != 0 {
		t.Errorf("expected skip stats, got %+v", stats)
	}
	if atomic.LoadInt32(&store.summaries) != 0 {
		t.Errorf("store received %d summaries on a skipped build", store.summaries)
	}
}

func TestBuild_HappyPath_AtLeastOneLevel(t *testing.T) {
	// 30 leaves split into two well-separated clouds — k-means
	// converges and the builder produces at least one level.
	leaves := make([]LeafRow, 30)
	for i := range leaves {
		offset := 0.0
		if i >= 15 {
			offset = 10.0
		}
		leaves[i] = LeafRow{
			ID:        fmt.Sprintf("leaf-%d", i),
			Content:   fmt.Sprintf("passage %d", i),
			Embedding: []float64{offset, offset},
		}
	}
	store := &fakeStore{leaves: leaves}
	summer := &fakeSummariser{}
	embed := &fakeEmbedder{}
	b := NewBuilder(store, embed, summer,
		Config{MinChunks: 25, MaxLevels: 4, BranchingFactor: 5, Concurrency: 4})

	stats, err := b.Build(context.Background(), BuildParams{
		KbID: "kb", FileID: "file", FileName: "n.txt", Dimensions: 2,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if stats.LevelsBuilt < 1 {
		t.Fatalf("want >= 1 levels built, got %d", stats.LevelsBuilt)
	}
	if stats.SummariesAdded == 0 {
		t.Error("expected at least one summary inserted")
	}
	if atomic.LoadInt32(&summer.calls) == 0 {
		t.Error("summariser never called")
	}
	if atomic.LoadInt32(&embed.calls) == 0 {
		t.Error("embedder never called")
	}
}

func TestBuild_PerClusterFailureDoesNotFailWholeBuild(t *testing.T) {
	leaves := make([]LeafRow, 30)
	for i := range leaves {
		offset := 0.0
		if i >= 15 {
			offset = 10.0
		}
		leaves[i] = LeafRow{
			ID:        fmt.Sprintf("leaf-%d", i),
			Content:   "passage",
			Embedding: []float64{offset, offset},
		}
	}
	store := &fakeStore{leaves: leaves, insertErr: errors.New("simulated insert failure")}
	summer := &fakeSummariser{}
	b := NewBuilder(store, &fakeEmbedder{}, summer,
		Config{MinChunks: 25, MaxLevels: 4, BranchingFactor: 5, Concurrency: 1})

	stats, err := b.Build(context.Background(), BuildParams{
		KbID: "kb", FileID: "file", FileName: "n.txt", Dimensions: 2,
	})
	if err != nil {
		t.Fatalf("build should not error when every cluster fails: %v", err)
	}
	if stats.SummariesAdded != 0 {
		t.Errorf("expected zero summaries on all-fail, got %d", stats.SummariesAdded)
	}
	// summariser still gets called even though insert fails.
	if atomic.LoadInt32(&summer.calls) == 0 {
		t.Error("summariser was not called despite clusters being available")
	}
}

func TestBuild_LeafListErrorBubbles(t *testing.T) {
	store := &fakeStore{listErr: errors.New("db gone")}
	b := NewBuilder(store, &fakeEmbedder{}, &fakeSummariser{},
		Config{MinChunks: 25, MaxLevels: 4, BranchingFactor: 5})
	_, err := b.Build(context.Background(), BuildParams{
		KbID: "kb", FileID: "file", Dimensions: 2,
	})
	if err == nil {
		t.Fatal("expected error from list-leaves failure")
	}
}

func TestJoinAndMaybeTruncate_ShortPassesThrough(t *testing.T) {
	members := []ClusterInput{{ID: "a"}, {ID: "b"}}
	content := map[string]string{"a": "alpha", "b": "beta"}
	got := joinAndMaybeTruncate(members, content, 12_000)
	if got != "alpha\n\n---\n\nbeta" {
		t.Errorf("unexpected join: %q", got)
	}
}

func TestJoinAndMaybeTruncate_LongTruncatesMiddle(t *testing.T) {
	// 100 members of 200 bytes each = 20 000 bytes; cap = 100 tokens
	// = 400 bytes → middle should drop.
	members := make([]ClusterInput, 100)
	content := map[string]string{}
	for i := range members {
		id := fmt.Sprintf("m%d", i)
		members[i] = ClusterInput{ID: id}
		content[id] = string(make([]byte, 200))
	}
	out := joinAndMaybeTruncate(members, content, 100)
	if len(out) > 600 {
		t.Errorf("truncated output too long: %d bytes", len(out))
	}
	if !contains(out, "middle truncated") {
		t.Errorf("missing truncation marker: %q", out)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
