//go:build integration

// FileExcerpts against a live vector Postgres: the row choice lives in SQL
// (DISTINCT ON + ORDER BY over node_kind, tree_level and the metadata
// chunkIndex), so only a real database proves it. Skipped when VECTOR_DB_*
// env is unset.

package vector

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestFileExcerpts_ChoosesRootSummaryThenDocumentOrder seeds files whose
// insertion (created_at) order deliberately contradicts the intended pick, so
// an ORDER BY created_at — the previous implementation — fails every case.
//
// Oracle: the hand-seeded fixture. Each file's expected excerpt is fixed by
// how it was seeded (which row is the RAPTOR root, which leaf has chunkIndex
// 0, which table holds which copy), not computed by the code under test.
func TestFileExcerpts_ChoosesRootSummaryThenDocumentOrder(t *testing.T) {
	pool := openBM25TestPool(t, "VECTOR_DB_HOST", "VECTOR_DB_PORT", "VECTOR_DB_USER", "VECTOR_DB_PASSWORD", "VECTOR_DB_NAME")
	if pool == nil {
		t.Skip("file excerpt tests require VECTOR_DB_* env (vector Postgres)")
	}
	svc := NewChunkService(pool)
	ctx := context.Background()

	const dimA, dimB = 8, 16
	kbID, otherKB := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		for _, d := range []int{dimA, dimB} {
			for _, kb := range []string{kbID, otherKB} {
				if err := svc.DeleteChunksByKbID(ctx, kb, d); err != nil {
					t.Logf("cleanup %s/%d: %v", kb, d, err)
				}
			}
		}
	})

	emb := func(dim int) []float64 {
		v := make([]float64, dim)
		for i := range v {
			v[i] = 1 / float64(i+1)
		}
		return v
	}
	// insert writes one row per call, i.e. one transaction per row, so
	// created_at strictly follows call order.
	insert := func(kb, fileID string, dim int, content, kind string, level int, chunkIndex *int) {
		t.Helper()
		meta := map[string]any{}
		if chunkIndex != nil {
			meta["chunkIndex"] = *chunkIndex
		}
		in := ChunkInput{Content: content, Embedding: emb(dim), KbID: kb, FileID: fileID, NodeKind: kind, TreeLevel: level, Metadata: meta}
		if err := svc.AddDocumentChunks(ctx, fileID, []ChunkInput{in}, dim, "simple"); err != nil {
			t.Fatalf("insert %q: %v", content, err)
		}
	}
	idx := func(i int) *int { return &i }

	// A: leaves only, written last-chunk-first.
	fileA := uuid.NewString()
	insert(kbID, fileA, dimA, "dritter Abschnitt", "leaf", 0, idx(2))
	insert(kbID, fileA, dimA, "zweiter Abschnitt", "leaf", 0, idx(1))
	insert(kbID, fileA, dimA, "Ära eins", "leaf", 0, idx(0))

	// B: a two-level RAPTOR tree; the level-1 summary is older than the root.
	fileB := uuid.NewString()
	insert(kbID, fileB, dimA, "b leaf", "leaf", 0, idx(0))
	insert(kbID, fileB, dimA, "b level one", "summary", 1, nil)
	insert(kbID, fileB, dimA, "b root", "summary", 2, nil)

	// C: an older community_summary row sharing the file id must be ignored.
	fileC := uuid.NewString()
	insert(kbID, fileC, dimA, "community text", "community_summary", 0, nil)
	insert(kbID, fileC, dimA, "c leaf", "leaf", 0, idx(0))

	// D: one copy per table (a KB re-embedded under a new dimension).
	fileD := uuid.NewString()
	insert(kbID, fileD, dimA, "d in 8", "leaf", 0, idx(0))
	insert(kbID, fileD, dimB, "d in 16", "leaf", 0, idx(0))

	// E: chunks under another KB only.
	fileE := uuid.NewString()
	insert(otherKB, fileE, dimA, "other kb", "leaf", 0, idx(0))

	ids := []string{fileA, fileB, fileC, fileD, fileE}

	got, err := svc.FileExcerpts(ctx, kbID, ids, 600, dimB)
	if err != nil {
		t.Fatalf("FileExcerpts: %v", err)
	}
	for id, want := range map[string]string{fileA: "Ära eins", fileB: "b root", fileC: "c leaf", fileD: "d in 16"} {
		if got[id] != want {
			t.Errorf("file %s: excerpt %q, want %q", id, got[id], want)
		}
	}
	if v, ok := got[fileE]; ok {
		t.Errorf("file of another KB leaked: %q", v)
	}

	// The preferred table decides between the two copies of D.
	got8, err := svc.FileExcerpts(ctx, kbID, []string{fileD}, 600, dimA)
	if err != nil {
		t.Fatalf("FileExcerpts (prefer %d): %v", dimA, err)
	}
	if got8[fileD] != "d in 8" {
		t.Errorf("prefer %d: excerpt %q, want %q", dimA, got8[fileD], "d in 8")
	}

	// maxLen counts characters, not bytes ("Ä" is two bytes).
	short, err := svc.FileExcerpts(ctx, kbID, []string{fileA}, 3, dimB)
	if err != nil {
		t.Fatalf("FileExcerpts (maxLen 3): %v", err)
	}
	if short[fileA] != "Ära" {
		t.Errorf("maxLen 3: excerpt %q, want %q", short[fileA], "Ära")
	}
}
