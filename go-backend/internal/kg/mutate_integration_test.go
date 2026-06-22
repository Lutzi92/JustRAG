//go:build integration

package kg

import (
	"context"
	"testing"
)

// TestDeleteKGForFile_PreciseRemoval builds a 2-file graph in a fresh KB:
//
//	file F1: entities A, B  + edge A->B
//	file F2: entities B, C  + edge B->C   (B shared by both files)
//
// then deletes F1 and asserts A is GC'd, B survives, C/F2 untouched.
func TestDeleteKGForFile_PreciseRemoval(t *testing.T) {
	pool := openKGTestPool(t)
	ctx := context.Background()
	store := NewPgStore(pool)

	var kb string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('kg-del-test', NULL, 'delete test') RETURNING id::text`).Scan(&kb); err != nil {
		t.Fatalf("insert KB: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id=$1::uuid`, kb) })

	var f1, f2 string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&f1); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&f2); err != nil {
		t.Fatal(err)
	}

	ent := func(name string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO kg_entities (kb_id, canonical_name, type, aliases)
			VALUES ($1::uuid, $2, 'thing', '{}') RETURNING id`, kb, name).Scan(&id); err != nil {
			t.Fatalf("insert entity %s: %v", name, err)
		}
		return id
	}
	a, b, c := ent("A"), ent("B"), ent("C")

	link := func(entID int64, file string) {
		if _, err := pool.Exec(ctx, `INSERT INTO kg_entity_files (entity_id, kb_id, file_id) VALUES ($1,$2::uuid,$3::uuid)`, entID, kb, file); err != nil {
			t.Fatalf("link: %v", err)
		}
	}
	link(a, f1)
	link(b, f1)
	link(b, f2)
	link(c, f2)

	edge := func(src, dst int64, file string) {
		if _, err := pool.Exec(ctx, `INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel, file_id) VALUES ($1::uuid,$2,$3,'rel',$4::uuid)`, kb, src, dst, file); err != nil {
			t.Fatalf("edge: %v", err)
		}
	}
	edge(a, b, f1)
	edge(b, c, f2)

	if err := store.DeleteKGForFile(ctx, kb, f1); err != nil {
		t.Fatalf("DeleteKGForFile: %v", err)
	}

	count := func(sql string, args ...any) int {
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	if n := count(`SELECT count(*) FROM kg_entities WHERE kb_id=$1::uuid AND canonical_name='A'`, kb); n != 0 {
		t.Errorf("entity A should be GC'd, found %d", n)
	}
	if n := count(`SELECT count(*) FROM kg_entities WHERE kb_id=$1::uuid AND canonical_name='B'`, kb); n != 1 {
		t.Errorf("entity B should survive (shared with F2), found %d", n)
	}
	if n := count(`SELECT count(*) FROM kg_entities WHERE kb_id=$1::uuid AND canonical_name='C'`, kb); n != 1 {
		t.Errorf("entity C should be untouched, found %d", n)
	}
	if n := count(`SELECT count(*) FROM kg_edges WHERE kb_id=$1::uuid AND file_id=$2::uuid`, kb, f1); n != 0 {
		t.Errorf("F1 edges should be deleted, found %d", n)
	}
	if n := count(`SELECT count(*) FROM kg_edges WHERE kb_id=$1::uuid AND file_id=$2::uuid`, kb, f2); n != 1 {
		t.Errorf("F2 edge should survive, found %d", n)
	}
}

func TestDeleteKGForFile_LeavesLegacyAndUnrelatedEntities(t *testing.T) {
	pool := openKGTestPool(t)
	ctx := context.Background()
	store := NewPgStore(pool)

	var kb string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('kg-legacy-test', NULL, 'legacy test') RETURNING id::text`).Scan(&kb); err != nil {
		t.Fatalf("insert KB: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id=$1::uuid`, kb) })

	var f1, f2 string
	_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&f1)
	_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&f2)

	ent := func(name string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO kg_entities (kb_id, canonical_name, type, aliases)
			VALUES ($1::uuid, $2, 'thing', '{}') RETURNING id`, kb, name).Scan(&id); err != nil {
			t.Fatalf("insert entity %s: %v", name, err)
		}
		return id
	}
	legacy := ent("Legacy") // NO kg_entity_files row → simulates pre-0055 data
	owned := ent("Owned")   // contributed only by f1
	other := ent("Other")   // contributed only by f2
	_ = legacy

	if _, err := pool.Exec(ctx, `INSERT INTO kg_entity_files (entity_id, kb_id, file_id) VALUES ($1,$2::uuid,$3::uuid)`, owned, kb, f1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kg_entity_files (entity_id, kb_id, file_id) VALUES ($1,$2::uuid,$3::uuid)`, other, kb, f2); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteKGForFile(ctx, kb, f1); err != nil {
		t.Fatalf("DeleteKGForFile: %v", err)
	}

	count := func(name string) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM kg_entities WHERE kb_id=$1::uuid AND canonical_name=$2`, kb, name).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	if count("Legacy") != 1 {
		t.Error("legacy zero-link entity must survive an unrelated file delete (pre-0055 no-op)")
	}
	if count("Owned") != 0 {
		t.Error("entity contributed only by the deleted file must be GC'd")
	}
	if count("Other") != 1 {
		t.Error("entity contributed by a different file must survive")
	}
}

func TestKBHasActiveIngestion(t *testing.T) {
	pool := openKGTestPool(t)
	ctx := context.Background()
	store := NewPgStore(pool)

	var kb string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('kg-active-test', NULL, 'active test') RETURNING id::text`).Scan(&kb); err != nil {
		t.Fatalf("insert KB: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id=$1::uuid`, kb) })

	active, err := store.KBHasActiveIngestion(ctx, kb)
	if err != nil {
		t.Fatalf("KBHasActiveIngestion: %v", err)
	}
	if active {
		t.Fatal("expected no active ingestion for empty KB")
	}

	var fileID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (kb_id, name, type, status) VALUES ($1::uuid, 'f.pdf', 'pdf', 'processing') RETURNING id::text`, kb).Scan(&fileID); err != nil {
		t.Fatalf("insert file: %v", err)
	}
	active, err = store.KBHasActiveIngestion(ctx, kb)
	if err != nil {
		t.Fatalf("KBHasActiveIngestion: %v", err)
	}
	if !active {
		t.Fatal("expected active ingestion with a processing file")
	}

	// A file whose status is already 'completed' but is still in a post-status
	// stage (KG extraction) must count as active — this is the refresh bug.
	if _, err := pool.Exec(ctx, `UPDATE files SET status='completed', current_stage='kg' WHERE id=$1::uuid`, fileID); err != nil {
		t.Fatalf("update completed+kg: %v", err)
	}
	active, err = store.KBHasActiveIngestion(ctx, kb)
	if err != nil {
		t.Fatalf("KBHasActiveIngestion: %v", err)
	}
	if !active {
		t.Fatal("want active=true for status=completed with current_stage='kg'")
	}

	// Once the stage is cleared, the KB is idle.
	if _, err := pool.Exec(ctx, `UPDATE files SET current_stage=NULL WHERE id=$1::uuid`, fileID); err != nil {
		t.Fatalf("clear stage: %v", err)
	}
	active, err = store.KBHasActiveIngestion(ctx, kb)
	if err != nil {
		t.Fatalf("KBHasActiveIngestion: %v", err)
	}
	if active {
		t.Fatal("want active=false after current_stage cleared")
	}
}
