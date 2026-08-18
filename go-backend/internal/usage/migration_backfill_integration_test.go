//go:build integration

// Tests migration 0066's backfill statement — extracted from the embedded
// migration file itself, not hand-copied, so the test cannot drift away from
// the SQL that actually ships.
//
// The statement runs against a TEMP TABLE that shadows public.usage_events for
// this session (pg_temp precedes public in the search_path). Two reasons: the
// shared CI database is never mutated, and the statement's
// `NOT EXISTS (SELECT 1 FROM usage_events)` idempotency guard sees an empty
// table, so the INSERT is genuinely exercised instead of short-circuiting
// against rows the CI migration already backfilled.

package usage_test

import (
	"context"
	"strings"
	"testing"

	mainmigrations "github.com/justrag/go-backend/migrations/main"
)

// backfillStatement returns the INSERT ... SELECT from 0066, i.e. everything
// between the first `INSERT INTO usage_events` and the `-- +goose Down` marker.
func backfillStatement(t *testing.T) string {
	t.Helper()
	raw, err := mainmigrations.FS.ReadFile("0066_usage_events.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	start := strings.Index(sql, "INSERT INTO usage_events")
	if start < 0 {
		t.Fatal("0066 no longer contains an INSERT INTO usage_events — did the backfill move?")
	}
	end := strings.Index(sql, "-- +goose Down")
	if end < 0 || end < start {
		t.Fatal("0066 has no -- +goose Down marker after the backfill")
	}
	stmt := strings.TrimSpace(sql[start:end])
	if !strings.Contains(stmt, "c.type = 'chat'") {
		t.Fatal("backfill lost its c.type = 'chat' filter — research sessions would be counted")
	}
	return stmt
}

func TestBackfill_CountsUserTurnsOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID, kbID := insertKB(t, pool, "backfill")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Shadow the real table for this session.
	if _, err := tx.Exec(ctx,
		`CREATE TEMP TABLE usage_events (LIKE public.usage_events INCLUDING ALL) ON COMMIT DROP`); err != nil {
		t.Fatalf("create temp shadow: %v", err)
	}

	// Fixture: a normal chat with 2 user + 2 ai messages, and a research
	// session with 1 user message that must NOT be counted.
	seed := func(chatType string, roles ...string) {
		var chatID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO chats (kb_id, user_id, title, type)
			VALUES ($1::uuid, $2::uuid, 'fixture', $3) RETURNING id::text`,
			kbID, userID, chatType).Scan(&chatID); err != nil {
			t.Fatalf("insert chat: %v", err)
		}
		for _, role := range roles {
			if _, err := tx.Exec(ctx,
				`INSERT INTO messages (chat_id, role, content) VALUES ($1::uuid, $2, 'x')`,
				chatID, role); err != nil {
				t.Fatalf("insert message: %v", err)
			}
		}
	}
	seed("chat", "user", "ai", "user", "ai")
	seed("research", "user", "ai")

	if _, err := tx.Exec(ctx, backfillStatement(t)); err != nil {
		t.Fatalf("run backfill: %v", err)
	}

	var count int
	var distinctSurfaces int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)::int, COUNT(DISTINCT surface)::int
		FROM usage_events WHERE kb_id = $1::uuid`, kbID).Scan(&count, &distinctSurfaces); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("backfilled rows: got %d, want 2 (two user turns; ai rows and the research session excluded)", count)
	}

	var surface string
	if err := tx.QueryRow(ctx,
		`SELECT DISTINCT surface FROM usage_events WHERE kb_id = $1::uuid`, kbID).Scan(&surface); err != nil {
		t.Fatalf("select surface: %v", err)
	}
	if surface != "web" {
		t.Errorf("backfill surface: got %q, want web", surface)
	}
}

// TestBackfill_IsIdempotent runs the statement twice; the NOT EXISTS guard
// must make the second run a no-op.
func TestBackfill_IsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID, kbID := insertKB(t, pool, "idem")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`CREATE TEMP TABLE usage_events (LIKE public.usage_events INCLUDING ALL) ON COMMIT DROP`); err != nil {
		t.Fatalf("create temp shadow: %v", err)
	}
	var chatID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO chats (kb_id, user_id, title, type)
		VALUES ($1::uuid, $2::uuid, 'fixture', 'chat') RETURNING id::text`,
		kbID, userID).Scan(&chatID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO messages (chat_id, role, content) VALUES ($1::uuid, 'user', 'x')`, chatID); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	stmt := backfillStatement(t)
	for i := 0; i < 2; i++ {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}

	var count int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM usage_events WHERE kb_id = $1::uuid`, kbID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("fixture rows after two runs: got %d, want 1 (the NOT EXISTS guard must make run 2 a no-op)", count)
	}
}
