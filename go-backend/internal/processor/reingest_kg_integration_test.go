//go:build integration

package processor

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/kg"
)

// TestReingestKG_ClearThenRepersist_ReplacesEdges proves the re-ingest
// invariant: clearing a file's KG (via the same DeleteKGForFile clearStaleKG
// uses) before re-persisting leaves only the new edges, while an entity also
// contributed by another file survives the clear.
func TestReingestKG_ClearThenRepersist_ReplacesEdges(t *testing.T) {
	pool := openKGStoreTestPool(t)
	ctx := context.Background()

	var kb string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('kg-reingest-test', NULL, 'reingest test') RETURNING id::text`).Scan(&kb); err != nil {
		t.Fatalf("insert KB: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id=$1::uuid`, kb) })

	var fileF, fileG, chunkF, chunkG, chunkF2 string
	for _, p := range []*string{&fileF, &fileG, &chunkF, &chunkG, &chunkF2} {
		_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(p)
	}

	store := newKGStore(pool)
	deleter := kg.NewPgStore(pool)

	// Initial ingest of file F: X --rel1--> Y
	if _, _, _, err := store.persistKGExtraction(ctx, kb, fileF, chunkF, ai.KGExtraction{
		Entities:  []ai.KGEntity{{Name: "X", Type: "concept"}, {Name: "Y", Type: "concept"}},
		Relations: []ai.KGRelation{{Src: "X", Dst: "Y", Rel: "rel1", Evidence: "X rel1 Y"}},
	}); err != nil {
		t.Fatalf("persist F (initial): %v", err)
	}

	// File G also references X (shared entity): X --rel3--> W
	if _, _, _, err := store.persistKGExtraction(ctx, kb, fileG, chunkG, ai.KGExtraction{
		Entities:  []ai.KGEntity{{Name: "X", Type: "concept"}, {Name: "W", Type: "concept"}},
		Relations: []ai.KGRelation{{Src: "X", Dst: "W", Rel: "rel3", Evidence: "X rel3 W"}},
	}); err != nil {
		t.Fatalf("persist G: %v", err)
	}

	// Re-ingest of file F: clear F's KG, then re-persist new content X --rel2--> Z
	if err := deleter.DeleteKGForFile(ctx, kb, fileF); err != nil {
		t.Fatalf("DeleteKGForFile: %v", err)
	}
	if _, _, _, err := store.persistKGExtraction(ctx, kb, fileF, chunkF2, ai.KGExtraction{
		Entities:  []ai.KGEntity{{Name: "X", Type: "concept"}, {Name: "Z", Type: "concept"}},
		Relations: []ai.KGRelation{{Src: "X", Dst: "Z", Rel: "rel2", Evidence: "X rel2 Z"}},
	}); err != nil {
		t.Fatalf("persist F (re-ingest): %v", err)
	}

	// Old F edge rel1 must be gone.
	var rel1 int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kg_edges WHERE kb_id=$1::uuid AND rel='rel1'`, kb).Scan(&rel1); err != nil {
		t.Fatal(err)
	}
	if rel1 != 0 {
		t.Errorf("stale edge rel1 should be deleted on re-ingest, found %d", rel1)
	}

	// New F edge rel2 must be present.
	var rel2 int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kg_edges WHERE kb_id=$1::uuid AND rel='rel2'`, kb).Scan(&rel2); err != nil {
		t.Fatal(err)
	}
	if rel2 != 1 {
		t.Errorf("new edge rel2 should exist after re-ingest, found %d", rel2)
	}

	// G's edge rel3 must be untouched.
	var rel3 int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kg_edges WHERE kb_id=$1::uuid AND rel='rel3'`, kb).Scan(&rel3); err != nil {
		t.Fatal(err)
	}
	if rel3 != 1 {
		t.Errorf("file G edge rel3 should be untouched, found %d", rel3)
	}

	// Shared entity X must survive the clear (still linked to G).
	var xCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kg_entities WHERE kb_id=$1::uuid AND canonical_name='X'`, kb).Scan(&xCount); err != nil {
		t.Fatal(err)
	}
	if xCount != 1 {
		t.Errorf("shared entity X should survive (linked to file G), found %d", xCount)
	}

	// Orphan entity Y (only F, now gone) must be GC'd.
	var yCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kg_entities WHERE kb_id=$1::uuid AND canonical_name='Y'`, kb).Scan(&yCount); err != nil {
		t.Fatal(err)
	}
	if yCount != 0 {
		t.Errorf("orphan entity Y should be GC'd on re-ingest, found %d", yCount)
	}
}
