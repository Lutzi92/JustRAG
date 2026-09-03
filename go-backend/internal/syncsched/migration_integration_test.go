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
	"path/filepath"
	"regexp"
	"runtime"
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

// migrationSQL reads the on-disk text of migration 0068, resolved relative to
// this test file (not the process cwd) so `go test` works from any directory.
func migrationSQL(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot locate this test file")
	}
	// go-backend/internal/syncsched/migration_integration_test.go
	//   -> go-backend/migrations/main/0068_sync_schedule.sql
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations", "main", "0068_sync_schedule.sql")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration file %s: %v", path, err)
	}
	return string(data)
}

var (
	rssBackfillRe        = regexp.MustCompile(`(?s)UPDATE\s+rss_feeds\b.*?;`)
	confluenceBackfillRe = regexp.MustCompile(`(?s)UPDATE\s+confluence_sources\b.*?;`)
)

// extractBackfillStatements pulls the two backfill UPDATE statements out of
// migration 0068's "-- +goose Up" section, verbatim, so this test executes
// the real migration SQL rather than a pasted copy that could drift from it.
// A failure here is the drift alarm: the migration's shape changed and this
// test no longer knows how to find the statements it is supposed to cover.
func extractBackfillStatements(t *testing.T, sql string) (rssStmt, confluenceStmt string) {
	t.Helper()
	upIdx := strings.Index(sql, "-- +goose Up")
	downIdx := strings.Index(sql, "-- +goose Down")
	if upIdx == -1 || downIdx == -1 || downIdx <= upIdx {
		t.Fatalf("drift alarm: could not locate -- +goose Up / -- +goose Down markers in 0068_sync_schedule.sql")
	}
	upSection := sql[upIdx:downIdx]

	rssStmt = rssBackfillRe.FindString(upSection)
	if rssStmt == "" {
		t.Fatal("drift alarm: no `UPDATE rss_feeds ...;` statement found in migration 0068's Up section")
	}
	confluenceStmt = confluenceBackfillRe.FindString(upSection)
	if confluenceStmt == "" {
		t.Fatal("drift alarm: no `UPDATE confluence_sources ...;` statement found in migration 0068's Up section")
	}
	return rssStmt, confluenceStmt
}

// TestMigration0068_BackfillMappedExistingRows executes the real backfill
// UPDATE statements (extracted from the migration file itself, see
// extractBackfillStatements) against seeded fixture rows covering every arm:
// active/paused RSS feeds, and Confluence sources with a daily interval, a
// weekly interval, a NULL interval, and a zero interval. Runs inside a
// transaction that is rolled back at the end, so the dev DB is left
// untouched regardless of outcome.
func TestMigration0068_BackfillMappedExistingRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	rssStmt, confluenceStmt := extractBackfillStatements(t, migrationSQL(t))

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var kbID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO knowledge_bases (name) VALUES ($1) RETURNING id::text`,
		"syncsched-backfill-"+testRunSuffix).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}

	var userID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (username, password_hash) VALUES ($1, 'x') RETURNING id::text`,
		"syncsched-backfill-"+testRunSuffix).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var connID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO confluence_connections (user_id, token) VALUES ($1, 'x') RETURNING id::text`,
		userID).Scan(&connID); err != nil {
		t.Fatalf("seed confluence connection: %v", err)
	}

	// rss_feeds fixtures: one active, one explicitly paused. All start on
	// 'manual' so the UPDATE has something to do; only the active one should
	// move.
	var rssActiveID, rssPausedID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO rss_feeds (kb_id, url, status, sync_schedule)
		VALUES ($1, 'https://example.invalid/active.xml', 'active', 'manual')
		RETURNING id::text`, kbID).Scan(&rssActiveID); err != nil {
		t.Fatalf("seed active rss feed: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO rss_feeds (kb_id, url, status, sync_schedule)
		VALUES ($1, 'https://example.invalid/paused.xml', 'paused', 'manual')
		RETURNING id::text`, kbID).Scan(&rssPausedID); err != nil {
		t.Fatalf("seed paused rss feed: %v", err)
	}

	// confluence_sources fixtures: daily interval (1440), weekly interval
	// (10080, deliberately included per the spec), NULL interval, and a zero
	// interval (treated the same as "no interval" — must stay manual).
	seedConfluence := func(label string, interval *int) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO confluence_sources (kb_id, connection_id, space_key, sync_interval, sync_schedule)
			VALUES ($1, $2, $3, $4, 'manual')
			RETURNING id::text`, kbID, connID, "SPACE-"+label, interval).Scan(&id); err != nil {
			t.Fatalf("seed confluence source %s: %v", label, err)
		}
		return id
	}
	daily := 1440
	weekly := 10080
	zero := 0
	confDailyID := seedConfluence("daily", &daily)
	confWeeklyID := seedConfluence("weekly", &weekly)
	confNullID := seedConfluence("null", nil)
	confZeroID := seedConfluence("zero", &zero)

	// Execute the real migration statements, extracted verbatim above.
	if _, err := tx.Exec(ctx, rssStmt); err != nil {
		t.Fatalf("exec extracted rss_feeds backfill statement: %v\nstatement was:\n%s", err, rssStmt)
	}
	if _, err := tx.Exec(ctx, confluenceStmt); err != nil {
		t.Fatalf("exec extracted confluence_sources backfill statement: %v\nstatement was:\n%s", err, confluenceStmt)
	}

	schedule := func(table, id string) string {
		t.Helper()
		var s string
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT sync_schedule FROM %s WHERE id = $1`, table), id).Scan(&s); err != nil {
			t.Fatalf("read back sync_schedule from %s: %v", table, err)
		}
		return s
	}

	cases := []struct {
		label, table, id, want string
	}{
		{"active rss feed", "rss_feeds", rssActiveID, "daily"},
		{"paused rss feed", "rss_feeds", rssPausedID, "manual"},
		{"confluence daily interval (1440)", "confluence_sources", confDailyID, "daily"},
		{"confluence weekly interval (10080)", "confluence_sources", confWeeklyID, "daily"},
		{"confluence NULL interval", "confluence_sources", confNullID, "manual"},
		{"confluence zero interval", "confluence_sources", confZeroID, "manual"},
	}
	for _, c := range cases {
		if got := schedule(c.table, c.id); got != c.want {
			t.Errorf("%s: sync_schedule = %q, want %q", c.label, got, c.want)
		}
	}
}
