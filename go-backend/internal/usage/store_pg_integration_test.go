//go:build integration

// Integration tests for PGRecorder against a live main Postgres. Skipped when
// DB_* env is unset. Pool/skip pattern follows
// internal/kb/store_pg_integration_test.go.

package usage_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/usage"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("usage integration tests require DB_* env (main Postgres)")
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// insertKB creates a throwaway user + KB and returns both ids.
func insertKB(t *testing.T, pool *pgxpool.Pool, suffix string) (userID, kbID string) {
	t.Helper()
	ctx := context.Background()
	err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x-not-a-real-hash', 'user') RETURNING id::text`,
		"usage-test-"+suffix).Scan(&userID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id) VALUES ($1, $2::uuid) RETURNING id::text`,
		"usage-kb-"+suffix, userID).Scan(&kbID)
	if err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM usage_events WHERE user_id = $1::uuid OR kb_id = $2::uuid`, userID, kbID) //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID)                               //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID)                                       //nolint:errcheck
	})
	return userID, kbID
}

// waitForCount polls until the row count for kbID reaches want, or fails.
// PGRecorder.Record is fire-and-forget, so the write lands on another
// goroutine; polling is what makes the assertion deterministic without
// exposing the goroutine to callers.
func waitForCount(t *testing.T, pool *pgxpool.Pool, kbID string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got int
	for time.Now().Before(deadline) {
		err := pool.QueryRow(context.Background(),
			`SELECT COUNT(*)::int FROM usage_events WHERE kb_id = $1::uuid`, kbID).Scan(&got)
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if got == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("usage_events count for kb: got %d, want %d", got, want)
}

func TestRecord_WritesOneRowWithAllDimensions(t *testing.T) {
	pool := testPool(t)
	userID, kbID := insertKB(t, pool, "dims")
	rec := usage.NewRecorder(pool)

	// api_key_id carries an FK, so the test needs a real api_keys row.
	var keyID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO api_keys (user_id, name, key_hash, key_prefix)
		VALUES ($1::uuid, 'usage-test', 'x', 'jrag_testpre')
		RETURNING id::text`, userID).Scan(&keyID)
	if err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1::uuid`, keyID) //nolint:errcheck
	})

	rec.Record(context.Background(), usage.Event{
		KbID: kbID, UserID: userID, APIKeyID: &keyID, Surface: usage.SurfaceOpenAICompat,
	})
	waitForCount(t, pool, kbID, 1)

	var surface string
	var gotUser, gotKey *string
	err = pool.QueryRow(context.Background(), `
		SELECT surface, user_id::text, api_key_id::text
		FROM usage_events WHERE kb_id = $1::uuid`, kbID).Scan(&surface, &gotUser, &gotKey)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if surface != "openai_compat" {
		t.Errorf("surface: got %q, want openai_compat", surface)
	}
	if gotUser == nil || *gotUser != userID {
		t.Errorf("user_id: got %v, want %s", gotUser, userID)
	}
	if gotKey == nil || *gotKey != keyID {
		t.Errorf("api_key_id: got %v, want %s", gotKey, keyID)
	}
}

func TestRecord_NilAPIKeyStoredAsNull(t *testing.T) {
	pool := testPool(t)
	userID, kbID := insertKB(t, pool, "nokey")
	usage.NewRecorder(pool).Record(context.Background(), usage.Event{
		KbID: kbID, UserID: userID, Surface: usage.SurfaceWeb,
	})
	waitForCount(t, pool, kbID, 1)

	var key *string
	if err := pool.QueryRow(context.Background(),
		`SELECT api_key_id::text FROM usage_events WHERE kb_id = $1::uuid`, kbID).Scan(&key); err != nil {
		t.Fatalf("select: %v", err)
	}
	if key != nil {
		t.Errorf("api_key_id: got %q, want NULL", *key)
	}
}

// TestRecord_EmptyDimensionsStoredAsNull pins the fix for the mcpserver
// nil-claims defensive branch (unreachable in practice behind
// apiKeyAuth.Authenticate, but the insert must degrade gracefully if it
// ever fires): an empty KbID/UserID string must land as SQL NULL in the
// nullable columns, not fail the $1::uuid cast and get the whole row
// swallowed at WARN.
func TestRecord_EmptyDimensionsStoredAsNull(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	usage.NewRecorder(pool).Record(ctx, usage.Event{
		KbID: "", UserID: "", Surface: usage.SurfaceMCP,
	})

	deadline := time.Now().Add(5 * time.Second)
	var count int
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*)::int FROM usage_events
			WHERE kb_id IS NULL AND user_id IS NULL AND surface = 'mcp'
			  AND created_at > now() - interval '1 minute'`).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count >= 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if count < 1 {
		t.Fatalf("row with empty kb_id/user_id: got %d matching NULL/NULL rows, want >= 1", count)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM usage_events WHERE kb_id IS NULL AND user_id IS NULL AND surface = 'mcp' AND created_at > now() - interval '1 minute'`) //nolint:errcheck
	})
}

// TestRecord_SurvivesKBDeletion pins the ledger decision: the FK is
// ON DELETE SET NULL, not CASCADE, so deleting a KB must not shrink the
// all-time total. A future "cleanup" to CASCADE turns this red.
func TestRecord_SurvivesKBDeletion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID, kbID := insertKB(t, pool, "cascade")
	usage.NewRecorder(pool).Record(ctx, usage.Event{
		KbID: kbID, UserID: userID, Surface: usage.SurfaceMCP,
	})
	waitForCount(t, pool, kbID, 1)

	if _, err := pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID); err != nil {
		t.Fatalf("delete kb: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM usage_events
		WHERE user_id = $1::uuid AND kb_id IS NULL`, userID).Scan(&count); err != nil {
		t.Fatalf("count orphans: %v", err)
	}
	if count != 1 {
		t.Errorf("rows surviving KB deletion with kb_id NULL: got %d, want 1", count)
	}
}
