//go:build integration

// Verifies the last_success_at write on git_repo_sources (migration 0071):
// a success stamps it, a failure state (which SetGitRepoSyncState also
// writes, with LastSuccessAt nil) must leave it alone. Requires a live main
// Postgres; skipped when DB_* env is unset.

package gitrepo_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/gitrepo"
)

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("gitrepo last_success_at tests require DB_* env (main Postgres)")
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

// Mutation: drop the COALESCE around $10 in SetGitRepoSyncState → the
// failure write below NULLs the previously stamped success and this fails.
func TestSetGitRepoSyncState_LastSuccessOnlyOnSuccess(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	store := gitrepo.NewStore(pool)

	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('gitrepo-last-success', 'public')
		RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})

	var srcID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO git_repo_sources (kb_id, repo_url) VALUES ($1::uuid, 'https://example.invalid/r.git')
		RETURNING id::text`, kbID).Scan(&srcID); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	read := func() *time.Time {
		t.Helper()
		var ts *time.Time
		if err := pool.QueryRow(ctx,
			`SELECT last_success_at FROM git_repo_sources WHERE id = $1::uuid`, srcID).Scan(&ts); err != nil {
			t.Fatalf("read source: %v", err)
		}
		return ts
	}

	if ts := read(); ts != nil {
		t.Fatalf("a fresh source must start with a NULL last_success_at, got %v", ts)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.SetGitRepoSyncState(ctx, srcID, gitrepo.SyncState{
		Status: "active", LastSyncedAt: &now, LastSuccessAt: &now,
	}); err != nil {
		t.Fatalf("SetGitRepoSyncState (success): %v", err)
	}
	stamped := read()
	if stamped == nil {
		t.Fatal("a successful sync must stamp last_success_at")
	}

	later := now.Add(time.Minute)
	msg := "clone failed"
	if err := store.SetGitRepoSyncState(ctx, srcID, gitrepo.SyncState{
		Status: "error", ErrorMessage: &msg, LastSyncedAt: &later,
	}); err != nil {
		t.Fatalf("SetGitRepoSyncState (failure): %v", err)
	}
	if after := read(); after == nil || !after.Equal(*stamped) {
		t.Errorf("a failed sync must leave last_success_at untouched: %v -> %v", stamped, after)
	}
}
