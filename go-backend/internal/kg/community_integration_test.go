//go:build integration

package kg

import (
	"context"
	"testing"
)

func TestCommunities_StoreClearRoundTrip(t *testing.T) {
	pool := openKGTestPool(t)
	ctx := context.Background()
	store := NewPgStore(pool)

	var kb string
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('comm-test', NULL, 't') RETURNING id::text`).Scan(&kb); err != nil {
		t.Fatalf("kb: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id=$1::uuid`, kb) })

	mk := func(name string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO kg_entities (kb_id, canonical_name, type, aliases)
			VALUES ($1::uuid,$2,'concept','{}') RETURNING id`, kb, name).Scan(&id); err != nil {
			t.Fatalf("entity %s: %v", name, err)
		}
		return id
	}
	a, b := mk("A"), mk("B")
	if _, err := pool.Exec(ctx, `INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel, weight) VALUES ($1::uuid,$2,$3,'r',2.0)`, kb, a, b); err != nil {
		t.Fatal(err)
	}

	edges, err := store.EdgesForCommunities(ctx, kb)
	if err != nil || len(edges) != 1 || edges[0].Weight != 2.0 {
		t.Fatalf("EdgesForCommunities: %v %+v", err, edges)
	}
	ents, err := store.EntitiesForCommunities(ctx, kb, []int64{a, b})
	if err != nil || len(ents) != 2 {
		t.Fatalf("EntitiesForCommunities: %v %+v", err, ents)
	}
	ies, err := store.InternalEdgesForCommunities(ctx, kb)
	if err != nil || len(ies) != 1 || ies[0].Src != a || ies[0].Dst != b || ies[0].Rel != "r" {
		t.Fatalf("InternalEdgesForCommunities: %v %+v", err, ies)
	}

	var chunk string
	_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&chunk)
	if err := store.StoreCommunity(ctx, kb, 0, []int64{a, b}, chunk, 2); err != nil {
		t.Fatalf("StoreCommunity: %v", err)
	}
	var cnt int
	pool.QueryRow(ctx, `SELECT count(*) FROM kg_communities WHERE kb_id=$1::uuid`, kb).Scan(&cnt)
	if cnt != 1 {
		t.Errorf("expected 1 community, got %d", cnt)
	}
	if err := store.ClearCommunities(ctx, kb); err != nil {
		t.Fatalf("ClearCommunities: %v", err)
	}
	pool.QueryRow(ctx, `SELECT count(*) FROM kg_communities WHERE kb_id=$1::uuid`, kb).Scan(&cnt)
	if cnt != 0 {
		t.Errorf("expected 0 after clear, got %d", cnt)
	}
}
