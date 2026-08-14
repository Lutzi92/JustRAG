package vector

import (
	"context"
	"encoding/json"

	"testing"
)

// stubNeighborFetcher returns pre-configured rows keyed by fileID + chunkIndex.
type stubNeighborFetcher struct {
	chunks map[string]map[int]rawRow // fileID -> chunkIndex -> rawRow
}

func (s *stubNeighborFetcher) FetchChunksByIndices(
	ctx context.Context, tableName, kbID, fileID string, indices []int,
) ([]rawRow, error) {
	fileMap, ok := s.chunks[fileID]
	if !ok {
		return nil, nil
	}
	var rows []rawRow
	for _, idx := range indices {
		if r, ok := fileMap[idx]; ok {
			rows = append(rows, r)
		}
	}
	return rows, nil
}

func makeMetadata(chunkIndex, totalChunks int, pages []int) json.RawMessage {
	m := map[string]any{
		"chunkIndex":  float64(chunkIndex),
		"totalChunks": float64(totalChunks),
	}
	if len(pages) == 1 {
		m["page"] = float64(pages[0])
	} else if len(pages) > 1 {
		ps := make([]any, len(pages))
		for i, p := range pages {
			ps[i] = float64(p)
		}
		m["pages"] = ps
	}
	b, _ := json.Marshal(m)
	return b
}

func parseMetaMap(s json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(s, &m)
	return m
}

func TestExpandNeighbors_NoMetadata(t *testing.T) {
	t.Parallel()
	chunks := []SearchChunk{
		{ID: "1", Content: "hello", FileID: "f1", Metadata: map[string]any{}},
		{ID: "2", Content: "world", FileID: "f2", Metadata: map[string]any{"foo": "bar"}},
	}
	fetcher := &stubNeighborFetcher{}

	result := ExpandNeighbors(context.Background(), chunks, 1, fetcher, "test_table", "kb1")

	if len(result) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(result))
	}
	if result[0].Content != "hello" {
		t.Errorf("expected unchanged content 'hello', got %q", result[0].Content)
	}
	if result[1].Content != "world" {
		t.Errorf("expected unchanged content 'world', got %q", result[1].Content)
	}
}

func TestExpandNeighbors_WithNeighbors(t *testing.T) {
	// Chunk at index 1 with windowSize=1 should fetch neighbors at 0 and 2.
	t.Parallel()

	chunks := []SearchChunk{
		{
			ID:       "mid",
			Content:  "middle",
			FileID:   "f1",
			Metadata: parseMetaMap(makeMetadata(1, 5, []int{2})),
		},
	}

	fetcher := &stubNeighborFetcher{
		chunks: map[string]map[int]rawRow{
			"f1": {
				0: {ID: "n0", Content: "before", Metadata: makeMetadata(0, 5, []int{1}), FileID: "f1"},
				2: {ID: "n2", Content: "after", Metadata: makeMetadata(2, 5, []int{3}), FileID: "f1"},
			},
		},
	}

	result := ExpandNeighbors(context.Background(), chunks, 1, fetcher, "test_table", "kb1")

	if len(result) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(result))
	}

	expected := "before\n\nmiddle\n\nafter"
	if result[0].Content != expected {
		t.Errorf("content mismatch:\nwant: %q\ngot:  %q", expected, result[0].Content)
	}

	// The citable pages stay those of the matched chunk: the neighbours are
	// reading context, not the passage that answered the query.
	pages := extractPages(result[0].Metadata)
	if len(pages) != 1 || pages[0] != 2 {
		t.Fatalf("citable pages: want [2] (the matched chunk's own page), got %v", pages)
	}
}

// A wide neighbour window must not widen the citation. With windowSize=3 the
// expanded content spans seven chunks — and before this was fixed the merged
// page set turned a hit on page 8 into a "S. 5-11" citation, which is what
// users saw as a page range far too broad to be useful.
func TestExpandNeighbors_WideWindowKeepsSingleCitedPage(t *testing.T) {
	t.Parallel()

	chunks := []SearchChunk{{
		ID:       "hit",
		Content:  "matched",
		FileID:   "f1",
		Metadata: parseMetaMap(makeMetadata(7, 20, []int{8})),
	}}

	neighbors := map[int]rawRow{}
	for idx := 4; idx <= 10; idx++ {
		if idx == 7 {
			continue
		}
		neighbors[idx] = rawRow{
			ID:       "n",
			Content:  "ctx",
			FileID:   "f1",
			Metadata: makeMetadata(idx, 20, []int{idx + 1}),
		}
	}
	fetcher := &stubNeighborFetcher{chunks: map[string]map[int]rawRow{"f1": neighbors}}

	result := ExpandNeighbors(context.Background(), chunks, 3, fetcher, "test_table", "kb1")

	pages := extractPages(result[0].Metadata)
	if len(pages) != 1 || pages[0] != 8 {
		t.Fatalf("citable pages: want [8], got %v", pages)
	}
}

func TestExpandNeighbors_SkipsAlreadyMatchedIndices(t *testing.T) {
	// Two adjacent matched chunks from the same file.
	// With windowSize=1, expansion of chunk at index 1 should include chunk at
	// index 2 from matched set (not re-fetch), and expansion of chunk at index 2
	// should include chunk at index 1 from matched set.
	t.Parallel()

	chunks := []SearchChunk{
		{
			ID:       "c1",
			Content:  "first",
			FileID:   "f1",
			Metadata: parseMetaMap(makeMetadata(1, 5, []int{1})),
		},
		{
			ID:       "c2",
			Content:  "second",
			FileID:   "f1",
			Metadata: parseMetaMap(makeMetadata(2, 5, []int{2})),
		},
	}

	fetcher := &stubNeighborFetcher{
		chunks: map[string]map[int]rawRow{
			"f1": {
				0: {ID: "n0", Content: "zero", Metadata: makeMetadata(0, 5, []int{0}), FileID: "f1"},
				3: {ID: "n3", Content: "three", Metadata: makeMetadata(3, 5, []int{3}), FileID: "f1"},
			},
		},
	}

	result := ExpandNeighbors(context.Background(), chunks, 1, fetcher, "test_table", "kb1")

	if len(result) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(result))
	}

	// Chunk at index 1: should have [0, 1, 2] = "zero\n\nfirst\n\nsecond"
	want0 := "zero\n\nfirst\n\nsecond"
	if result[0].Content != want0 {
		t.Errorf("chunk 0 content:\nwant: %q\ngot:  %q", want0, result[0].Content)
	}

	// Chunk at index 2: should have [1, 2, 3] = "first\n\nsecond\n\nthree"
	want1 := "first\n\nsecond\n\nthree"
	if result[1].Content != want1 {
		t.Errorf("chunk 1 content:\nwant: %q\ngot:  %q", want1, result[1].Content)
	}
}

func TestExpandNeighbors_WindowSizeZero(t *testing.T) {
	t.Parallel()
	chunks := []SearchChunk{
		{
			ID:       "c1",
			Content:  "content",
			FileID:   "f1",
			Metadata: parseMetaMap(makeMetadata(5, 10, []int{5})),
		},
	}

	fetcher := &stubNeighborFetcher{
		chunks: map[string]map[int]rawRow{
			"f1": {
				4: {ID: "n4", Content: "prev", Metadata: makeMetadata(4, 10, []int{4}), FileID: "f1"},
			},
		},
	}

	result := ExpandNeighbors(context.Background(), chunks, 0, fetcher, "test_table", "kb1")

	if len(result) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(result))
	}
	if result[0].Content != "content" {
		t.Errorf("expected unchanged content, got %q", result[0].Content)
	}
}

func TestExpandNeighbors_NilFetcher(t *testing.T) {
	t.Parallel()
	chunks := []SearchChunk{
		{ID: "1", Content: "hello", FileID: "f1", Metadata: map[string]any{"chunkIndex": float64(0)}},
	}
	result := ExpandNeighbors(context.Background(), chunks, 1, nil, "t", "kb")
	if len(result) != 1 || result[0].Content != "hello" {
		t.Error("nil fetcher should return chunks unchanged")
	}
}

// Ensure the stub satisfies the interface.
var _ NeighborFetcher = (*stubNeighborFetcher)(nil)
