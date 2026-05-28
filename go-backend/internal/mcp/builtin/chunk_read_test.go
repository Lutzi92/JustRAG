package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

type fakeChunkReader struct {
	rows         []vector.ChunkRow
	gotKbID      string
	gotChunkID   string
	gotDirection string
	gotDepth     int
}

func (f *fakeChunkReader) LookupChunkNeighbors(_ context.Context, kbID, chunkID, direction string, depth int) ([]vector.ChunkRow, error) {
	f.gotKbID = kbID
	f.gotChunkID = chunkID
	f.gotDirection = direction
	f.gotDepth = depth
	return f.rows, nil
}

func TestChunkRead_RequiresChunkID(t *testing.T) {
	t.Parallel()
	tool := NewChunkRead(&fakeChunkReader{})
	_, err := tool.Handler.Invoke(context.Background(), json.RawMessage(`{"kb_id":"kb-1"}`))
	if err == nil {
		t.Error("expected error on missing chunk_id")
	}
}

func TestChunkRead_RequiresKbID(t *testing.T) {
	t.Parallel()
	tool := NewChunkRead(&fakeChunkReader{})
	_, err := tool.Handler.Invoke(context.Background(), json.RawMessage(`{"chunk_id":"c1"}`))
	if err == nil {
		t.Error("expected error on missing kb_id")
	}
}

// TestChunkRead_DefaultDirection: when direction is omitted, the
// underlying SearchService method receives empty string and applies
// its own "both" default. The tool layer must NOT silently rewrite
// the direction — that would mask wiring errors.
func TestChunkRead_DefaultDirection(t *testing.T) {
	t.Parallel()
	fake := &fakeChunkReader{
		rows: []vector.ChunkRow{
			{ID: "c1", FileID: "f1", Content: "self"},
		},
	}
	tool := NewChunkRead(fake)
	got, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"chunk_id":"c1","kb_id":"kb-1","depth":3}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if fake.gotDepth != 3 {
		t.Errorf("depth not propagated: %d", fake.gotDepth)
	}
	if fake.gotDirection != "" {
		t.Errorf("direction must be passed-through-empty (let SearchService default), got %q", fake.gotDirection)
	}
	if len(got.Chunks) != 1 || got.Chunks[0].ID != "c1" {
		t.Errorf("chunks not bubbled: %+v", got.Chunks)
	}
}
