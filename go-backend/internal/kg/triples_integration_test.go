//go:build integration

package kg

import (
	"context"
	"testing"
)

func TestIncidentTriples_CapAndDedup(t *testing.T) {
	pool := openKGTestPool(t)
	ctx := context.Background()
	store := NewPgStore(pool)

	var kb string
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('triples-test', NULL, 't') RETURNING id::text`).Scan(&kb); err != nil {
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
	a, b := mkEnt("Alpha"), mkEnt("Beta")
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, `INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel, weight)
			VALUES ($1::uuid,$2,$3,'knows',1.0)`, kb, a, b); err != nil {
			t.Fatalf("edge: %v", err)
		}
	}
	got, err := store.IncidentTriples(ctx, kb, []int64{a}, 40)
	if err != nil {
		t.Fatalf("IncidentTriples: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 deduped triple, got %d: %+v", len(got), got)
	}
	if got[0].SrcName != "Alpha" || got[0].Rel != "knows" || got[0].DstName != "Beta" {
		t.Errorf("unexpected triple: %+v", got[0])
	}

	capped, err := store.IncidentTriples(ctx, kb, []int64{a}, 0)
	if err != nil || capped != nil {
		t.Errorf("cap<1 must return (nil,nil), got %v / %v", capped, err)
	}
}
