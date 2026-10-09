//go:build integration

// GetFileLeafTextAllDims reads leaf chunk text back from the live pgvector
// store via TEST_VECTOR_DSN (publish-time screening has no parsed text).

package vector

import (
	"context"
	"testing"
)

func TestGetFileLeafTextAllDims(t *testing.T) {
	pool := openTestVectorPool(t)
	svc := NewChunkService(pool)
	ctx := context.Background()

	kbID := "55555555-5555-5555-5555-555555555555"
	fileID := "66666666-6666-6666-6666-666666666666"
	t.Cleanup(func() {
		if err := svc.DeleteChunksByFileIDAllDims(ctx, fileID); err != nil {
			t.Logf("cleanup DeleteChunksByFileIDAllDims: %v", err)
		}
	})

	emb := make([]float64, 8)
	for i := range emb {
		emb[i] = 1.0 / float64(i+1)
	}
	mk := func(content string, idx int, kind string) ChunkInput {
		return ChunkInput{
			Content: content, Embedding: emb, KbID: kbID, FileID: fileID,
			Metadata: map[string]any{"chunkIndex": idx}, NodeKind: kind,
		}
	}
	// Inserted out of order, plus one RAPTOR summary that must not appear.
	chunks := []ChunkInput{
		mk("third", 2, "leaf"),
		mk("first", 0, "leaf"),
		mk("model-written summary", 0, "summary"),
		mk("second", 1, "leaf"),
	}
	if err := svc.AddDocumentChunks(ctx, fileID, chunks, 8, "simple"); err != nil {
		t.Fatalf("AddDocumentChunks: %v", err)
	}

	got, err := svc.GetFileLeafTextAllDims(ctx, kbID, fileID)
	if err != nil {
		t.Fatalf("GetFileLeafTextAllDims: %v", err)
	}
	if want := "first\n\nsecond\n\nthird"; got != want {
		t.Fatalf("leaf text = %q, want %q", got, want)
	}

	none, err := svc.GetFileLeafTextAllDims(ctx, kbID, "77777777-7777-7777-7777-777777777777")
	if err != nil {
		t.Fatalf("unknown file: %v", err)
	}
	if none != "" {
		t.Fatalf("unknown file: got %q, want empty", none)
	}
}
