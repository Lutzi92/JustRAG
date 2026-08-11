//go:build integration

package kg

import (
	"context"
	"sort"
	"testing"
)

func TestMergeEntities_RepointsDropsSelfLoopsUnionsAliasesIdempotent(t *testing.T) {
	pool := openKGTestPool(t)
	ctx := context.Background()
	store := NewPgStore(pool)

	var kb string
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('merge-test', NULL, 't') RETURNING id::text`).Scan(&kb); err != nil {
		t.Fatalf("kb: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id=$1::uuid`, kb) })

	mk := func(name string, aliases []string) int64 {
		var id int64
		// COALESCE mirrors the production writer (processor/kg_store.go): a nil
		// slice encodes as NULL, and kg_entities.aliases is NOT NULL DEFAULT
		// '{}'. Without this, mk(name, nil) — "entity with no aliases" —
		// violates the constraint instead of exercising the empty-alias case.
		if err := pool.QueryRow(ctx, `INSERT INTO kg_entities (kb_id, canonical_name, type, aliases)
			VALUES ($1::uuid,$2,'org',COALESCE($3::text[],'{}')) RETURNING id`, kb, name, aliases).Scan(&id); err != nil {
			t.Fatalf("entity %s: %v", name, err)
		}
		return id
	}
	surv := mk("PPM-Team", []string{"team"})
	dup := mk("PPM", []string{"ppm"})
	other := mk("Other", nil)

	if _, err := pool.Exec(ctx, `INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel) VALUES ($1::uuid,$2,$3,'r1')`, kb, dup, other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel) VALUES ($1::uuid,$2,$3,'r2')`, kb, surv, dup); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel) VALUES ($1::uuid,$2,$2,'selfloop')`, kb, other); err != nil {
		t.Fatal(err)
	}
	var fileF string
	_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&fileF)
	if _, err := pool.Exec(ctx, `INSERT INTO kg_entity_files (entity_id, kb_id, file_id) VALUES ($1,$2::uuid,$3::uuid)`, dup, kb, fileF); err != nil {
		t.Fatal(err)
	}

	if err := store.MergeEntities(ctx, kb, surv, []int64{dup}, []string{"PPM-Team", "team", "PPM", "ppm"}); err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}

	var dupCount int
	pool.QueryRow(ctx, `SELECT count(*) FROM kg_entities WHERE id=$1`, dup).Scan(&dupCount)
	if dupCount != 0 {
		t.Errorf("merged entity should be deleted")
	}
	var r1src int64
	pool.QueryRow(ctx, `SELECT src_entity_id FROM kg_edges WHERE kb_id=$1::uuid AND rel='r1'`, kb).Scan(&r1src)
	if r1src != surv {
		t.Errorf("r1 src should be re-pointed to survivor %d, got %d", surv, r1src)
	}
	var r2Count int
	pool.QueryRow(ctx, `SELECT count(*) FROM kg_edges WHERE kb_id=$1::uuid AND rel='r2'`, kb).Scan(&r2Count)
	if r2Count != 0 {
		t.Errorf("self-loop r2 should be dropped, found %d", r2Count)
	}
	var selfLoop int
	pool.QueryRow(ctx, `SELECT count(*) FROM kg_edges WHERE kb_id=$1::uuid AND rel='selfloop'`, kb).Scan(&selfLoop)
	if selfLoop != 1 {
		t.Errorf("pre-existing self-loop on unrelated entity must survive, found %d", selfLoop)
	}
	var efSurv int
	pool.QueryRow(ctx, `SELECT count(*) FROM kg_entity_files WHERE entity_id=$1 AND file_id=$2::uuid`, surv, fileF).Scan(&efSurv)
	if efSurv != 1 {
		t.Errorf("entity_files should re-point to survivor")
	}
	var aliases []string
	pool.QueryRow(ctx, `SELECT aliases FROM kg_entities WHERE id=$1`, surv).Scan(&aliases)
	sort.Strings(aliases)
	if len(aliases) != 4 {
		t.Errorf("survivor aliases should union to 4, got %v", aliases)
	}
	if err := store.MergeEntities(ctx, kb, surv, []int64{dup}, aliases); err != nil {
		t.Errorf("idempotent re-run errored: %v", err)
	}
	_ = other
}
