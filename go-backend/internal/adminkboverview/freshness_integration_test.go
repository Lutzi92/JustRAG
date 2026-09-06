//go:build integration

// Store-level tests for the freshness aggregates (migration 0071). Require a
// live main Postgres; skipped when DB_* env is unset.

package adminkboverview_test

import (
	"context"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/adminkboverview"
)

// TestFileStatsByKB_StalenessAndOldestFile seeds three files with known
// effective dates and asserts the two new aggregates. The middle file carries
// a published_at NEWER than its created_at, which is the whole point of the
// COALESCE: it must count as fresh even though it was ingested long ago.
//
// Mutation: replace COALESCE(published_at, created_at) with created_at in
// either aggregate → the fresh-by-publication file counts as stale and this
// fails.
func TestFileStatsByKB_StalenessAndOldestFile(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()

	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('kboverview-freshness', 'public')
		RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})

	seed := func(name string, created time.Time, published *time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO files (kb_id, name, type, status, storage_path, created_at, published_at)
			VALUES ($1::uuid, $2, 'text/markdown', 'completed', $3, $4, $5)`,
			kbID, name, "p/"+name, created, published); err != nil {
			t.Fatalf("seed file %s: %v", name, err)
		}
	}
	now := time.Now().UTC()
	ancient := now.AddDate(-5, 0, 0)
	recent := now.AddDate(0, 0, -3)
	seed("ancient.md", ancient, nil)                   // stale
	seed("republished.md", ancient, &recent)           // ingested long ago, PUBLISHED recently -> fresh
	seed("fresh.md", now.AddDate(0, 0, -1), nil)       // fresh
	seed("borderline.md", now.AddDate(0, 0, -60), nil) // stale at a 30-day threshold

	stats, err := adminkboverview.NewStore(pool).FileStatsByKB(ctx, 30)
	if err != nil {
		t.Fatalf("FileStatsByKB: %v", err)
	}
	fs, ok := stats[kbID]
	if !ok {
		t.Fatalf("no stats row for the seeded KB")
	}
	if fs.FileCount != 4 {
		t.Fatalf("fileCount = %d, want 4", fs.FileCount)
	}
	if fs.StaleFileCount != 2 {
		t.Errorf("staleFileCount = %d, want 2 (ancient + borderline; republished is fresh by publication date)", fs.StaleFileCount)
	}
	if fs.OldestFileAt == nil {
		t.Fatal("oldestFileAt must be populated")
	}
	if got := *fs.OldestFileAt; got[:4] != ancient.Format("2006") {
		t.Errorf("oldestFileAt = %q, want the ancient file's year %s", got, ancient.Format("2006"))
	}
}

// TestSyncStatsByKB_UnionsThreeSourceTables seeds one RSS feed (succeeded, no
// failures) and one git repository (never succeeded, failing) and asserts the
// merge: the success timestamp wins, the attempt timestamp is carried
// alongside, failing is OR-ed, kinds are collected — plus the W4-R9 per-kind
// breakdown: two ByKind entries, rss succeeded, git never succeeded (which is
// exactly the case allSyncKindsSucceeded uses to flip the aggregate
// SyncSucceeded to false, per the service-level test of the same scenario in
// freshness_test.go).
//
// Mutation: change BOOL_OR to BOOL_AND → failing reads false here and the
// admin panel would show a broken source as healthy.
func TestSyncStatsByKB_UnionsThreeSourceTables(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()

	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('kboverview-sync', 'public')
		RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})

	success := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	attempt := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	if _, err := pool.Exec(ctx, `
		INSERT INTO rss_feeds (kb_id, url, status, last_polled_at, last_success_at, consecutive_failures)
		VALUES ($1::uuid, 'https://example.invalid/f.xml', 'active', $2, $3, 0)`,
		kbID, attempt, success); err != nil {
		t.Fatalf("seed rss feed: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO git_repo_sources (kb_id, repo_url, last_synced_at, consecutive_failures)
		VALUES ($1::uuid, 'https://example.invalid/r.git', $2, 3)`,
		kbID, attempt); err != nil {
		t.Fatalf("seed git repo source: %v", err)
	}

	stats, err := adminkboverview.NewStore(pool).SyncStatsByKB(ctx)
	if err != nil {
		t.Fatalf("SyncStatsByKB: %v", err)
	}
	ss, ok := stats[kbID]
	if !ok {
		t.Fatalf("no sync stats row for the seeded KB")
	}
	if ss.LastSuccessAt == nil {
		t.Error("lastSuccessAt must come from the RSS feed's last_success_at")
	}
	if ss.LastAttemptAt == nil {
		t.Error("lastAttemptAt must be carried as the fallback")
	}
	if !ss.Failing {
		t.Error("failing must be true — the git source has consecutive_failures > 0")
	}
	if len(ss.Kinds) != 2 {
		t.Errorf("kinds = %v, want rss + git", ss.Kinds)
	}
	if len(ss.ByKind) != 2 {
		t.Fatalf("byKind = %+v, want two entries (rss + git)", ss.ByKind)
	}
	var rssStatus, gitStatus *adminkboverview.SyncKindStatus
	for i := range ss.ByKind {
		switch ss.ByKind[i].Kind {
		case "rss":
			rssStatus = &ss.ByKind[i]
		case "git":
			gitStatus = &ss.ByKind[i]
		}
	}
	if rssStatus == nil || !rssStatus.SyncSucceeded {
		t.Errorf("rss byKind entry must report syncSucceeded=true, got %+v", rssStatus)
	}
	if gitStatus == nil {
		t.Fatal("no git entry in byKind")
	}
	if gitStatus.SyncSucceeded {
		t.Error("git byKind entry must report syncSucceeded=false — it has never succeeded")
	}
	if !gitStatus.SyncFailing {
		t.Error("git byKind entry must report syncFailing=true")
	}
}
