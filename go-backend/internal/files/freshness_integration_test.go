//go:build integration

// Migration 0071 (files.published_at + last_success_at on the three source
// tables) and the store code that reads and writes those columns. Require a
// live main Postgres; skipped when DB_* env is unset. Note the repo .env sets
// DB_HOST=db, so run with DB_HOST=localhost or these skip silently.

package files_test

import (
	"context"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/files"
)

// TestMigration0071_ColumnsExist is the plain existence check for the four
// columns the freshness surface added. Everything below reads or writes them,
// so this failing first localises "the migration did not run" away from "the
// query is wrong".
func TestMigration0071_ColumnsExist(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()

	cases := []struct{ table, column string }{
		{"files", "published_at"},
		{"rss_feeds", "last_success_at"},
		{"confluence_sources", "last_success_at"},
		{"git_repo_sources", "last_success_at"},
	}
	for _, c := range cases {
		var dataType string
		err := pool.QueryRow(ctx, `
			SELECT data_type FROM information_schema.columns
			 WHERE table_name = $1 AND column_name = $2`, c.table, c.column).Scan(&dataType)
		if err != nil {
			t.Errorf("%s.%s: not found (%v)", c.table, c.column, err)
			continue
		}
		if dataType != "timestamp with time zone" {
			t.Errorf("%s.%s: data_type = %q, want timestamptz", c.table, c.column, dataType)
		}
	}
}

// TestCreateFile_PersistsPublishedAt covers both arms of CreateFile's
// INSERT — with and without an RSS feed id — since they are separate SQL
// statements and only one of them is on the RSS path that actually fills the
// column today.
//
// Mutation: drop published_at from either INSERT → the read-back is NULL and
// this fails.
func TestCreateFile_PersistsPublishedAt(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	store := files.NewStore(pool)

	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('freshness-test', 'public')
		RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})

	// The RSS arm of the INSERT is the one that matters in production, so it
	// needs a real rss_feeds row to satisfy the FK.
	var feedID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO rss_feeds (kb_id, url, status) VALUES ($1::uuid, 'https://example.invalid/f.xml', 'active')
		RETURNING id::text`, kbID).Scan(&feedID); err != nil {
		t.Fatalf("seed rss feed: %v", err)
	}

	published := time.Date(2025, 12, 24, 9, 30, 0, 0, time.UTC)
	rssFile, err := store.CreateFile(ctx, files.CreateFileData{
		KbID: kbID, Name: "advisory.md", Type: "text/markdown", Size: 10,
		Origin: "rss", StoragePath: "rss/a.md", RSSFeedID: feedID, PublishedAt: &published,
	})
	if err != nil {
		t.Fatalf("CreateFile (rss arm) with published_at: %v", err)
	}
	plainFile, err := store.CreateFile(ctx, files.CreateFileData{
		KbID: kbID, Name: "manual.md", Type: "text/markdown", Size: 10,
		Origin: "upload", StoragePath: "u/m.md", PublishedAt: &published,
	})
	if err != nil {
		t.Fatalf("CreateFile (non-rss arm) with published_at: %v", err)
	}
	withoutDate, err := store.CreateFile(ctx, files.CreateFileData{
		KbID: kbID, Name: "upload.pdf", Type: "application/pdf", Size: 10,
		Origin: "upload", StoragePath: "u/a.pdf",
	})
	if err != nil {
		t.Fatalf("CreateFile without published_at: %v", err)
	}

	dates, err := store.FileDatesByIDs(ctx, []string{rssFile.ID, plainFile.ID, withoutDate.ID, "00000000-0000-0000-0000-000000000000"})
	if err != nil {
		t.Fatalf("FileDatesByIDs: %v", err)
	}
	if len(dates) != 3 {
		t.Fatalf("FileDatesByIDs returned %d rows, want 3 (the unknown id must simply be absent)", len(dates))
	}
	for label, id := range map[string]string{"rss arm": rssFile.ID, "non-rss arm": plainFile.ID} {
		got := dates[id]
		if got.PublishedAt == nil || !got.PublishedAt.UTC().Equal(published) {
			t.Errorf("%s: published_at round-trip got %v, want %v", label, got.PublishedAt, published)
		}
		if got.CreatedAt.IsZero() {
			t.Errorf("%s: created_at must always be populated", label)
		}
	}
	if p := dates[withoutDate.ID].PublishedAt; p != nil {
		t.Errorf("a file created without a publication date must keep published_at NULL, got %v", p)
	}
}

// TestFileDatesByIDs_EmptyInputSkipsTheQuery pins the guard: an empty id list
// must not reach Postgres (`= ANY('{}')` is legal but pointless, and the chat
// path calls this on every turn).
func TestFileDatesByIDs_EmptyInput(t *testing.T) {
	store := files.NewStore(openMainPool(t))
	out, err := store.FileDatesByIDs(context.Background(), nil)
	if err != nil {
		t.Fatalf("FileDatesByIDs(nil): %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want empty map, got %v", out)
	}
}
