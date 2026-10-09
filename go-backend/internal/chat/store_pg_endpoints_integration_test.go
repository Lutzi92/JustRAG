//go:build integration

// Integration tests for the PGStore methods behind PATCH /api/chats/{id} and
// GET /api/kb/{id}/starter-questions: ownership and updated_at are enforced
// in SQL, and the starter documents are filtered in SQL, so only a live
// Postgres proves them. Skipped when DB_* env is unset (pool helper from
// store_pg_conflicts_integration_test.go).

package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/store"
)

// seedEndpointUser inserts a throwaway user and removes it at cleanup.
func seedEndpointUser(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x-not-a-real-hash', 'user') RETURNING id::text`,
		name+"-"+uuid.NewString()[:8]).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id) }) //nolint:errcheck
	return id
}

// seedEndpointKB inserts a throwaway KB owned by userID; its chats and files
// cascade with it at cleanup.
func seedEndpointKB(t *testing.T, pool *pgxpool.Pool, userID, name string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id) VALUES ($1, $2::uuid) RETURNING id::text`,
		name, userID).Scan(&id); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM chats WHERE kb_id = $1::uuid`, id)        //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM files WHERE kb_id = $1::uuid`, id)        //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	return id
}

// TestPGStore_UpdateChatTitle covers owner-only renames, the untouched
// updated_at and the not-found contract.
//
// Oracle: the row as re-read from the table after each call, against values
// the test itself wrote (the original title, a fixed updated_at a year in the
// past). A rename that bumps updated_at (the previous `updated_at = now()`)
// or that ignores the owner fails it.
func TestPGStore_UpdateChatTitle(t *testing.T) {
	pool := conflictTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	owner := seedEndpointUser(t, pool, "rename-owner")
	other := seedEndpointUser(t, pool, "rename-other")
	kbID := seedEndpointKB(t, pool, owner, "rename-kb")

	stamp := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	var chatID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO chats (kb_id, user_id, title, updated_at)
		VALUES ($1::uuid, $2::uuid, 'Original', $3) RETURNING id::text`,
		kbID, owner, stamp).Scan(&chatID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	read := func() (string, time.Time) {
		t.Helper()
		var title string
		var updated time.Time
		if err := pool.QueryRow(ctx, `SELECT title, updated_at FROM chats WHERE id = $1::uuid`, chatID).Scan(&title, &updated); err != nil {
			t.Fatalf("read chat: %v", err)
		}
		return title, updated
	}

	if err := s.UpdateChatTitle(ctx, chatID, other, "Hijacked"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other user's rename: got %v, want store.ErrNotFound", err)
	}
	if title, _ := read(); title != "Original" {
		t.Fatalf("other user's rename changed the title to %q", title)
	}

	if err := s.UpdateChatTitle(ctx, chatID, owner, "Budget 2027"); err != nil {
		t.Fatalf("owner rename: %v", err)
	}
	title, updated := read()
	if title != "Budget 2027" {
		t.Fatalf("title = %q, want %q", title, "Budget 2027")
	}
	if !updated.Equal(stamp) {
		t.Fatalf("updated_at moved from %v to %v — a rename must not reorder the history", stamp, updated)
	}

	if err := s.UpdateChatTitle(ctx, uuid.NewString(), owner, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing chat: got %v, want store.ErrNotFound", err)
	}
}

// TestPGStore_StarterContext checks which documents the starter questions are
// generated from.
//
// Oracle: the seeded fixture — each file's status, injection_flag, KB and
// created_at are chosen by the test, so the expected list (eligible files of
// this KB, newest first, capped by limit) follows from the seed alone.
func TestPGStore_StarterContext(t *testing.T) {
	pool := conflictTestPool(t)
	s := NewStore(pool)
	ctx := context.Background()

	owner := seedEndpointUser(t, pool, "starter-owner")
	kbID := seedEndpointKB(t, pool, owner, "Starter KB")
	otherKB := seedEndpointKB(t, pool, owner, "Other KB")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seed := func(kb, name, status string, flagged bool, age int) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO files (kb_id, name, type, status, storage_path, created_at, injection_flag)
			VALUES ($1::uuid, $2, 'text/markdown', $3, $4, $5, $6)
			RETURNING id::text`,
			kb, name, status, "p/"+uuid.NewString(), base.Add(-time.Duration(age)*time.Hour), flagged).Scan(&id); err != nil {
			t.Fatalf("seed file %s: %v", name, err)
		}
		return id
	}
	newest := seed(kbID, "newest.md", "completed", false, 1)
	partial := seed(kbID, "partial.md", "partial", false, 2)
	seed(kbID, "flagged.md", "completed", true, 0) // newest of all, but flagged
	seed(kbID, "processing.md", "processing", false, 0)
	seed(kbID, "error.md", "error", false, 0)
	oldest := seed(kbID, "oldest.md", "completed", false, 3)
	seed(otherKB, "elsewhere.md", "completed", false, 0)

	src, err := s.StarterContext(ctx, kbID, 10)
	if err != nil {
		t.Fatalf("StarterContext: %v", err)
	}
	if src.KBName != "Starter KB" {
		t.Errorf("KBName = %q, want %q", src.KBName, "Starter KB")
	}
	want := []string{newest, partial, oldest}
	if len(src.Files) != len(want) {
		t.Fatalf("got %d files %+v, want %d (eligible files of this KB only)", len(src.Files), src.Files, len(want))
	}
	for i, f := range src.Files {
		if f.ID != want[i] {
			t.Errorf("file %d = %s (%s), want %s — newest eligible first", i, f.ID, f.Name, want[i])
		}
	}

	capped, err := s.StarterContext(ctx, kbID, 2)
	if err != nil {
		t.Fatalf("StarterContext (limit 2): %v", err)
	}
	if len(capped.Files) != 2 || capped.Files[1].ID != partial {
		t.Errorf("limit 2: got %+v, want the two newest eligible files", capped.Files)
	}

	if _, err := s.StarterContext(ctx, uuid.NewString(), 10); err == nil {
		t.Error("missing KB: want an error")
	}
}
