package processor

import (
	"context"
	"sync"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

func passModel(_ context.Context, _, model string) (string, bool) { return model, true }

// fakeKGChunks models a chunk table holding leaf and summary rows; like the
// real reader, GetLeafChunksByFileID returns the leaves only.
type fakeKGChunks struct {
	leaves, summaries []vector.FileChunkRow
}

func (f *fakeKGChunks) ListChunkTableDimensions(context.Context) ([]int, error) {
	return []int{8}, nil
}

func (f *fakeKGChunks) GetLeafChunksByFileID(context.Context, string, string, int) ([]vector.FileChunkRow, error) {
	return f.leaves, nil
}

type spyKGPub struct {
	statuses []bool
	changed  int
}

func (s *spyKGPub) PublishStatus(_ context.Context, _ string, processing bool) {
	s.statuses = append(s.statuses, processing)
}
func (s *spyKGPub) PublishGraphChanged(context.Context, string) { s.changed++ }

func newRebuildProcessor(enabled bool, cache kgCache, extract func(string) (ai.KGExtraction, error)) (*Processor, *spyKGDeleter, *spyKGPub, *fakeKGPersister) {
	vals := map[string]*string{}
	if enabled {
		vals["kg_extraction_enabled"] = strPtr("true")
	}
	del, pub, store := &spyKGDeleter{}, &spyKGPub{}, &fakeKGPersister{}
	p := &Processor{
		siteConfigReader: &fakeSiteConfigReader{values: vals},
		kgCleaner:        del,
		kgPub:            pub,
		kgCache:          cache,
		kgEffectiveModel: passModel,
		kgPersist:        store,
		kgChunks: &fakeKGChunks{
			leaves:    []vector.FileChunkRow{{ID: "a", Content: "leaf A"}, {ID: "b", Content: "leaf B"}},
			summaries: []vector.FileChunkRow{{ID: "s", Content: "model-written summary"}},
		},
		extractKG: func(_ context.Context, _, _, chunk, _, _, _ string) (ai.KGExtraction, error) {
			return extract(chunk)
		},
	}
	return p, del, pub, store
}

func TestRebuildKGForFile_GateOffIsNoOp(t *testing.T) {
	calls := 0
	p, del, pub, store := newRebuildProcessor(false, &fakeKGCache{}, func(string) (ai.KGExtraction, error) {
		calls++
		return ai.KGExtraction{}, nil
	})
	if err := p.RebuildKGForFile(context.Background(), "kb1", "f1", "doc", "uf1", "body"); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(del.calls) != 0 || pub.changed != 0 || len(pub.statuses) != 0 || len(store.persisted) != 0 {
		t.Errorf("gate off must do nothing: calls=%d clears=%v pub=%+v persisted=%d", calls, del.calls, pub, len(store.persisted))
	}
}

func TestRebuildKGForFile_ExtractsLeavesClearsAndPublishes(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	p, del, pub, store := newRebuildProcessor(true, &fakeKGCache{}, func(chunk string) (ai.KGExtraction, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, chunk)
		return kgExt("llm-" + chunk), nil
	})
	if err := p.RebuildKGForFile(context.Background(), "kb1", "f1", "doc", "uf1", "body"); err != nil {
		t.Fatal(err)
	}
	if len(del.calls) != 1 || del.calls[0] != [2]string{"kb1", "f1"} {
		t.Errorf("clearStaleKG must run once for {kb1,f1}, got %v", del.calls)
	}
	if len(seen) != 2 {
		t.Errorf("only the 2 leaves are extracted (summary skipped), got %v", seen)
	}
	if _, ok := store.persisted["s"]; ok {
		t.Error("summary chunk must not be persisted")
	}
	if len(store.persisted) != 2 {
		t.Errorf("both leaves persist, got %d", len(store.persisted))
	}
	if pub.changed != 1 || len(pub.statuses) != 1 {
		t.Errorf("graph-changed + status must publish once each, got %+v", pub)
	}
}

func TestRebuildKGForFile_CacheHitsMakeNoExtractorCalls(t *testing.T) {
	// No mainDB: resolveKBLanguages falls back to "en", and the resolved KG
	// model is "" (no tier configured), so those are the key parts to seed.
	cache := &fakeKGCache{entries: map[string]ai.KGExtraction{
		"uf1|" + vector.HashContent("leaf A") + "||en": kgExt("cached-A"),
		"uf1|" + vector.HashContent("leaf B") + "||en": kgExt("cached-B"),
	}}
	calls := 0
	p, _, _, store := newRebuildProcessor(true, cache, func(string) (ai.KGExtraction, error) {
		calls++
		return ai.KGExtraction{}, nil
	})
	if err := p.RebuildKGForFile(context.Background(), "kb1", "f1", "doc", "uf1", "body"); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("full cache hit must make no extractor calls, got %d", calls)
	}
	if store.persisted["a"].Entities[0].Name != "cached-A" || store.persisted["b"].Entities[0].Name != "cached-B" {
		t.Errorf("cached extractions must be persisted: %+v", store.persisted)
	}
}
