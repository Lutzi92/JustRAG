//go:build integration

package vector

import (
	"context"
	"testing"
)

// GetLeafChunksByFileID must exclude RAPTOR summary rows (real SQL).
func TestGetLeafChunksByFileID_ExcludesSummaries(t *testing.T) {
	pool := openTestVectorPool(t)
	svc := NewChunkService(pool)
	ctx := context.Background()

	kbID := "88888888-8888-8888-8888-888888888881"
	fileID := "88888888-8888-8888-8888-888888888882"
	t.Cleanup(func() { _ = svc.DeleteChunksByFileIDAllDims(ctx, fileID) })

	emb := make([]float64, 8)
	for i := range emb {
		emb[i] = 1.0 / float64(i+1)
	}
	mk := func(content, kind string) ChunkInput {
		return ChunkInput{Content: content, Embedding: emb, KbID: kbID, FileID: fileID,
			Metadata: map[string]any{}, NodeKind: kind}
	}
	if err := svc.AddDocumentChunks(ctx, fileID, []ChunkInput{
		mk("leaf one", "leaf"), mk("model summary", "summary"), mk("leaf two", "leaf"),
	}, 8, "simple"); err != nil {
		t.Fatalf("AddDocumentChunks: %v", err)
	}

	leaves, err := svc.GetLeafChunksByFileID(ctx, kbID, fileID, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaves) != 2 {
		t.Fatalf("want 2 leaves, got %d: %+v", len(leaves), leaves)
	}
	for _, l := range leaves {
		if l.Content == "model summary" {
			t.Error("summary row leaked into the leaf reader")
		}
	}
	all, err := svc.GetChunksByFileID(ctx, kbID, fileID, 8)
	if err != nil || len(all) != 3 {
		t.Errorf("GetChunksByFileID must stay unfiltered (3 rows): n=%d err=%v", len(all), err)
	}
}
