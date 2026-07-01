//go:build integration

// DB-backed tests for PgStore.EntityDetail (Task 1, Phase 2 entity-detail endpoint).
// Uses the same pool bootstrap helper (openKGTestPool) as query_integration_test.go.

package kg

import (
	"context"
	"errors"
	"testing"
)

// seedGraphFixture inserts a KB, two kg_entities, one files row, and one
// kg_edges row linking them (with file_id set so Sources is populated).
// Returns the store, the kbID, and a cleanup func that removes the KB (and
// cascades to entities + edges).
//
// Entity names seeded:
//
//	"Alice, Inc." (organization) --[partners_with]--> "BobCo" (organization)
func seedGraphFixture(t *testing.T) (*PgStore, string, func()) {
	t.Helper()
	pool := openKGTestPool(t)
	ctx := context.Background()
	store := NewPgStore(pool)

	// Insert KB.
	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('entity-detail-fixture', NULL, 'integration test fixture')
		RETURNING id::text
	`).Scan(&kbID); err != nil {
		t.Fatalf("seedGraphFixture: insert KB: %v", err)
	}

	// Insert two entities.
	var alice, bob int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO kg_entities (kb_id, canonical_name, type, aliases)
		VALUES ($1::uuid, 'Alice, Inc.', 'organization', ARRAY['alice inc','alice'])
		RETURNING id
	`, kbID).Scan(&alice); err != nil {
		t.Fatalf("seedGraphFixture: insert Alice entity: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO kg_entities (kb_id, canonical_name, type, aliases)
		VALUES ($1::uuid, 'BobCo', 'organization', ARRAY['bobco'])
		RETURNING id
	`, kbID).Scan(&bob); err != nil {
		t.Fatalf("seedGraphFixture: insert BobCo entity: %v", err)
	}

	// Insert a file row so Sources is populated.
	var fileID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (kb_id, name, type) VALUES ($1::uuid, 'fixture.pdf', 'pdf')
		RETURNING id::text
	`, kbID).Scan(&fileID); err != nil {
		t.Fatalf("seedGraphFixture: insert file: %v", err)
	}

	// Generate a random chunk UUID without relying on any Go UUID library.
	var chunkID string
	if err := pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&chunkID); err != nil {
		t.Fatalf("seedGraphFixture: gen chunk uuid: %v", err)
	}

	// Insert an edge with file_id so the Sources query returns a row.
	if _, err := pool.Exec(ctx, `
		INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel, chunk_id, file_id)
		VALUES ($1::uuid, $2, $3, 'partners_with', $4::uuid, $5::uuid)
	`, kbID, alice, bob, chunkID, fileID); err != nil {
		t.Fatalf("seedGraphFixture: insert edge: %v", err)
	}

	cleanup := func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID)
	}
	return store, kbID, cleanup
}

// entityIDByName resolves a canonical entity name within the given KB.
// Uses PgStore.LookupEntityByName (case-insensitive); fatals if not found.
func entityIDByName(t *testing.T, store *PgStore, kbID, name string) int64 {
	t.Helper()
	entities, err := store.LookupEntityByName(context.Background(), kbID, name)
	if err != nil {
		t.Fatalf("entityIDByName(%q): %v", name, err)
	}
	if len(entities) == 0 {
		t.Fatalf("entityIDByName(%q): entity not found in KB %s", name, kbID)
	}
	return entities[0].ID
}

func TestEntityDetail_ReturnsScopedDetail(t *testing.T) {
	ctx := context.Background()
	store, kbID, cleanup := seedGraphFixture(t)
	defer cleanup()

	id := entityIDByName(t, store, kbID, "Alice, Inc.")

	d, err := store.EntityDetail(ctx, kbID, id)
	if err != nil {
		t.Fatalf("EntityDetail: %v", err)
	}
	if d.ID != id || d.Name != "Alice, Inc." {
		t.Fatalf("wrong entity: %+v", d)
	}
	if d.Degree < 1 {
		t.Errorf("expected degree >= 1, got %d", d.Degree)
	}
	if len(d.Neighbors) == 0 {
		t.Errorf("expected at least one neighbor")
	}
}

func TestEntityDetail_WrongKB_NotFound(t *testing.T) {
	ctx := context.Background()
	store, kbID, cleanup := seedGraphFixture(t)
	defer cleanup()
	id := entityIDByName(t, store, kbID, "Alice, Inc.")

	_, err := store.EntityDetail(ctx, "00000000-0000-0000-0000-000000000000", id)
	if !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("expected ErrEntityNotFound for cross-KB id, got %v", err)
	}
}
