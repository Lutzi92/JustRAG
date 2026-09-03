//go:build integration

// Verifies migration 0068's columns, CHECK constraints and backfill against a
// live main Postgres. Skipped when DB_* env is unset; note the repo .env sets
// DB_HOST=db, so run with DB_HOST=localhost or the test skips silently.

package syncsched_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var testRunSuffix = fmt.Sprintf("%d", time.Now().UnixNano())

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("syncsched migration tests require DB_* env (main Postgres)")
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

// insertKB creates a throwaway KB and returns its id.
func insertKB(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	name := "syncsched-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + testRunSuffix
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO knowledge_bases (name) VALUES ($1) RETURNING id::text`, name).Scan(&id)
	if err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1`, id)
	})
	return id
}

func TestMigration0068_DefaultsToManual(t *testing.T) {
	pool := testPool(t)
	kbID := insertKB(t, pool)
	ctx := context.Background()

	var schedule string
	var next *time.Time
	err := pool.QueryRow(ctx, `
		INSERT INTO git_repo_sources (kb_id, repo_url)
		VALUES ($1, 'https://example.invalid/repo.git')
		RETURNING sync_schedule, next_sync_at`, kbID).Scan(&schedule, &next)
	if err != nil {
		t.Fatalf("insert git repo source: %v", err)
	}
	if schedule != "manual" {
		t.Fatalf("expected manual, got %q", schedule)
	}
	if next != nil {
		t.Fatalf("expected NULL next_sync_at, got %v", next)
	}
}

func TestMigration0068_RejectsUnknownSchedule(t *testing.T) {
	pool := testPool(t)
	kbID := insertKB(t, pool)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO git_repo_sources (kb_id, repo_url, sync_schedule)
		VALUES ($1, 'https://example.invalid/repo2.git', 'hourly')`, kbID)
	if err == nil {
		t.Fatal("expected the CHECK constraint to reject 'hourly'")
	}
}

func TestMigration0068_BackfillMappedExistingRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Any row that predates the migration must not still be 'manual' unless
	// it is a git repo or an interval-less Confluence source.
	var stray int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM confluence_sources
		 WHERE sync_schedule = 'manual' AND sync_interval IS NOT NULL AND sync_interval > 0`).Scan(&stray)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if stray != 0 {
		t.Fatalf("%d Confluence sources with an interval were left on 'manual'", stray)
	}
}
