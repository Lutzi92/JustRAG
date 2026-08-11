//go:build integration

package kg

import (
	"context"
	"testing"
)

func TestScopedGraphForMessage_UnionAndSources(t *testing.T) {
	pool := openKGTestPool(t)
	ctx := context.Background()
	store := NewPgStore(pool)

	var kb string
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('scoped-mm-test', NULL, 't') RETURNING id::text`).Scan(&kb); err != nil {
		t.Fatalf("kb: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id=$1::uuid`, kb) })

	mkEnt := func(name string, aliases []string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO kg_entities (kb_id, canonical_name, type, aliases)
			VALUES ($1::uuid,$2,'concept',$3) RETURNING id`, kb, name, aliases).Scan(&id); err != nil {
			t.Fatalf("entity %s: %v", name, err)
		}
		return id
	}
	x := mkEnt("X", []string{"X"})
	y := mkEnt("Y", []string{"Y"})
	q := mkEnt("Quux", []string{"Quux"})

	// chats.user_id is NOT NULL *and* carries a FK to users
	// (chats_user_id_users_id_fk, since migration 0000), so a bare
	// gen_random_uuid() fails the constraint — the row needs a real user.
	// Username is randomised so repeated runs can't collide on users_username_unique.
	var owner string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash)
		VALUES ('kg-scoped-test-' || gen_random_uuid()::text, 'x') RETURNING id::text`).Scan(&owner); err != nil {
		t.Fatalf("user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, owner) })

	var chat, userMsg, aiMsg string
	if err := pool.QueryRow(ctx, `INSERT INTO chats (kb_id, user_id, title) VALUES ($1::uuid, $2::uuid, 't') RETURNING id::text`, kb, owner).Scan(&chat); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO messages (chat_id, role, content) VALUES ($1::uuid,'user','tell me about Quux') RETURNING id::text`, chat).Scan(&userMsg); err != nil {
		t.Fatalf("user msg: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO messages (chat_id, role, content, parent_message_id) VALUES ($1::uuid,'ai','answer',$2::uuid) RETURNING id::text`, chat, userMsg).Scan(&aiMsg); err != nil {
		t.Fatalf("ai msg: %v", err)
	}

	var chunkID, fileID string
	_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&chunkID)
	// files has no user_id column; type is NOT NULL with no default.
	if err := pool.QueryRow(ctx, `INSERT INTO files (kb_id, name, type) VALUES ($1::uuid,'report.pdf','pdf') RETURNING id::text`, kb).Scan(&fileID); err != nil {
		t.Fatalf("file: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel, chunk_id, file_id)
		VALUES ($1::uuid,$2,$3,'rel_xy',$4::uuid,$5::uuid)`, kb, x, y, chunkID, fileID); err != nil {
		t.Fatalf("edge xy: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO message_chunks (message_id, chunk_id, kb_id) VALUES ($1::uuid,$2::uuid,$3::uuid)`, aiMsg, chunkID, kb); err != nil {
		t.Fatalf("message_chunks: %v", err)
	}

	g, err := store.ScopedGraphForMessage(ctx, kb, aiMsg)
	if err != nil {
		t.Fatalf("ScopedGraphForMessage: %v", err)
	}
	have := map[int64]ScopedGraphNode{}
	for _, n := range g.Nodes {
		have[n.ID] = n
	}
	if _, ok := have[x]; !ok {
		t.Errorf("expected chunk-derived entity X in scope")
	}
	if _, ok := have[y]; !ok {
		t.Errorf("expected chunk-derived entity Y in scope")
	}
	if _, ok := have[q]; !ok {
		t.Errorf("expected query-matched entity Quux in scope (union)")
	}
	if len(g.Edges) != 1 || g.Edges[0].Rel != "rel_xy" {
		t.Errorf("expected one edge rel_xy, got %+v", g.Edges)
	}
	xn := have[x]
	if len(xn.Sources) != 1 || xn.Sources[0].FileName != "report.pdf" || xn.Sources[0].ChunkID != chunkID {
		t.Errorf("expected X source report.pdf/%s, got %+v", chunkID, xn.Sources)
	}
}

func TestScopedGraphForMessage_CrossKBReturnsSentinel(t *testing.T) {
	pool := openKGTestPool(t)
	ctx := context.Background()
	store := NewPgStore(pool)
	var kbA string
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id, description) VALUES ('kb-a', NULL, 't') RETURNING id::text`).Scan(&kbA); err != nil {
		t.Fatalf("kbA: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id=$1::uuid`, kbA) })
	var bogus string
	_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&bogus)
	if _, err := store.ScopedGraphForMessage(ctx, kbA, bogus); err == nil {
		t.Fatalf("expected ErrMessageNotInKB for a foreign/absent message")
	}
}
