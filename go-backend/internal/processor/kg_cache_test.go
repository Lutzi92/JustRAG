package processor

import (
	"context"
	"sync"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

type fakeKGCache struct {
	mu      sync.Mutex
	entries map[string]ai.KGExtraction
	gets    int
	puts    []string
	models  []string
}

func (f *fakeKGCache) Get(_ context.Context, ufID, hash, model, lang string) (*ai.KGExtraction, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	f.models = append(f.models, model)
	e, ok := f.entries[ufID+"|"+hash+"|"+model+"|"+lang]
	if !ok {
		return nil, false, nil
	}
	return &e, true, nil
}

func (f *fakeKGCache) Put(_ context.Context, ufID, hash, model, lang string, ext ai.KGExtraction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, hash)
	if f.entries == nil {
		f.entries = map[string]ai.KGExtraction{}
	}
	f.entries[ufID+"|"+hash+"|"+model+"|"+lang] = ext
	return nil
}

type fakeKGPersister struct {
	mu        sync.Mutex
	persisted map[string]ai.KGExtraction // chunkID -> extraction
}

func (f *fakeKGPersister) persistKGExtraction(_ context.Context, _, _, chunkID string, ext ai.KGExtraction) (int, int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.persisted == nil {
		f.persisted = map[string]ai.KGExtraction{}
	}
	f.persisted[chunkID] = ext
	return len(ext.Entities), 0, 0, nil
}

func kgExt(name string) ai.KGExtraction {
	return ai.KGExtraction{Entities: []ai.KGEntity{{Name: name, Type: "concept"}}}
}

func TestExtractAndPersistKG_LibraryFileUsesCache(t *testing.T) {
	cache := &fakeKGCache{entries: map[string]ai.KGExtraction{
		"uf1|" + vector.HashContent("chunk A") + "|model-x|de": kgExt("cached-A"),
	}}
	var mu sync.Mutex
	var extracted []string
	p := &Processor{
		kgCache: cache, kgEffectiveModel: passModel,
		extractKG: func(_ context.Context, _, _, chunk, _, _, _ string) (ai.KGExtraction, error) {
			mu.Lock()
			defer mu.Unlock()
			extracted = append(extracted, chunk)
			return kgExt("llm-" + chunk), nil
		},
	}
	store := &fakeKGPersister{}
	chunks := []vector.FileChunkRow{{ID: "a", Content: "chunk A"}, {ID: "b", Content: "chunk B"}}

	p.extractAndPersistKG(context.Background(), store, chunks, "f1", "kb1", "doc", "body", "de", "model-x", "uf1")

	if len(extracted) != 1 || extracted[0] != "chunk B" {
		t.Fatalf("extractor must run only for the miss (chunk B), got %v", extracted)
	}
	if got := store.persisted["a"].Entities[0].Name; got != "cached-A" {
		t.Errorf("chunk A must persist the cached extraction, got %q", got)
	}
	if got := store.persisted["b"].Entities[0].Name; got != "llm-chunk B" {
		t.Errorf("chunk B must persist the fresh extraction, got %q", got)
	}
	if len(cache.puts) != 1 || cache.puts[0] != vector.HashContent("chunk B") {
		t.Errorf("only the miss is Put, got %v", cache.puts)
	}
}

func TestExtractAndPersistKG_NoUserFileNeverTouchesCache(t *testing.T) {
	cache := &fakeKGCache{}
	calls := 0
	var mu sync.Mutex
	p := &Processor{
		kgCache: cache, kgEffectiveModel: passModel,
		extractKG: func(_ context.Context, _, _, chunk, _, _, _ string) (ai.KGExtraction, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return kgExt(chunk), nil
		},
	}
	store := &fakeKGPersister{}
	chunks := []vector.FileChunkRow{{ID: "a", Content: "chunk A"}, {ID: "b", Content: "chunk B"}}

	p.extractAndPersistKG(context.Background(), store, chunks, "f1", "kb1", "doc", "body", "de", "m", "")

	if cache.gets != 0 || len(cache.puts) != 0 {
		t.Errorf("cache touched for a non-library file: gets=%d puts=%v", cache.gets, cache.puts)
	}
	if calls != 2 || len(store.persisted) != 2 {
		t.Errorf("both chunks must extract+persist, calls=%d persisted=%d", calls, len(store.persisted))
	}
}

func TestExtractAndPersistKG_DifferentModelMisses(t *testing.T) {
	cache := &fakeKGCache{entries: map[string]ai.KGExtraction{
		"uf1|" + vector.HashContent("chunk A") + "|other-model|de": kgExt("cached-A"),
	}}
	calls := 0
	p := &Processor{
		kgCache: cache, kgEffectiveModel: passModel,
		extractKG: func(_ context.Context, _, _, _, _, _, _ string) (ai.KGExtraction, error) {
			calls++
			return kgExt("fresh"), nil
		},
	}
	store := &fakeKGPersister{}
	p.extractAndPersistKG(context.Background(), store, []vector.FileChunkRow{{ID: "a", Content: "chunk A"}},
		"f1", "kb1", "doc", "body", "de", "model-x", "uf1")
	if calls != 1 || store.persisted["a"].Entities[0].Name != "fresh" {
		t.Errorf("an entry made under another model must not be served (calls=%d)", calls)
	}
}
