//go:build integration

package kg

import (
	"context"
	"testing"
)

func TestPPRPassages_RanksSharedChunkTop(t *testing.T) {
	pool := openKGTestPool(t)
	ctx := context.Background()
	store := NewPgStore(pool)

	var kb string
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('ppr-passages-test', NULL, 't') RETURNING id::text`).Scan(&kb); err != nil {
		t.Fatalf("kb: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id=$1::uuid`, kb) })

	mkEnt := func(name string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO kg_entities (kb_id, canonical_name, type, aliases)
			VALUES ($1::uuid,$2,'concept','{}') RETURNING id`, kb, name).Scan(&id); err != nil {
			t.Fatalf("entity %s: %v", name, err)
		}
		return id
	}
	a, b := mkEnt("A"), mkEnt("B")

	var hot, cold string
	_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&hot)
	_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&cold)
	if _, err := pool.Exec(ctx, `INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel, chunk_id, weight)
		VALUES ($1::uuid,$2,$3,'r1',$4::uuid,1.0)`, kb, a, b, hot); err != nil {
		t.Fatalf("hot edge: %v", err)
	}
	leaf := mkEnt("Leaf")
	if _, err := pool.Exec(ctx, `INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel, chunk_id, weight)
		VALUES ($1::uuid,$2,$3,'r2',$4::uuid,1.0)`, kb, b, leaf, cold); err != nil {
		t.Fatalf("cold edge: %v", err)
	}

	out, err := store.PPRPassages(ctx, kb, []int64{a}, 5, PPRConfig{})
	if err != nil {
		t.Fatalf("PPRPassages: %v", err)
	}
	if len(out) == 0 || out[0] != hot {
		t.Errorf("expected hot chunk %s ranked first, got %v", hot, out)
	}
}
