//go:build integration

// The published_at write in CreateConfluenceFile (W4-R10): a Confluence page
// file carries the page's own version date, an attachment carries none.
// Requires a live main Postgres; skipped when DB_* env is unset. Note the repo
// .env sets DB_HOST=db, so run with DB_HOST=localhost or these skip silently.

package confluence_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/confluence"
)

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("confluence published_at tests require DB_* env (main Postgres)")
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

// seedSource creates the users → confluence_connections → confluence_sources
// chain the files FK needs, and registers cleanup. Deleting the KB and the
// user cascades everything created below them.
func seedSource(t *testing.T, pool *pgxpool.Pool) (kbID, sourceID string) {
	t.Helper()
	ctx := context.Background()

	var userID string
	username := fmt.Sprintf("confluence-published-at-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash) VALUES ($1, 'x')
		RETURNING id::text`, username).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID) //nolint:errcheck
	})

	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('confluence-published-at', 'public')
		RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})

	var connID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO confluence_connections (user_id, token) VALUES ($1::uuid, 'enc')
		RETURNING id::text`, userID).Scan(&connID); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO confluence_sources (kb_id, connection_id, space_key) VALUES ($1::uuid, $2::uuid, 'SPC')
		RETURNING id::text`, kbID, connID).Scan(&sourceID); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	return kbID, sourceID
}

// Mutation: drop published_at from the CreateConfluenceFile INSERT column
// list (and its $9 placeholder) → the page row comes back NULL and this fails.
func TestCreateConfluenceFile_WritesPublishedAt(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	store := confluence.NewStore(pool)
	kbID, sourceID := seedSource(t, pool)

	published := time.Date(2025, 12, 24, 9, 30, 0, 0, time.UTC)
	page, err := store.CreateConfluenceFile(ctx, confluence.CreateConfluenceFileData{
		KbID: kbID, Name: "handbuch.md", Type: "text/markdown", Size: 12,
		Origin: "confluence", StoragePath: "confluence/p1.md",
		ConfluenceSourceID: sourceID, ConfluencePageID: "p1",
		PublishedAt: &published,
	})
	if err != nil {
		t.Fatalf("CreateConfluenceFile (page): %v", err)
	}

	// An attachment is created through the same store call with no date —
	// its effective date must stay COALESCE(NULL, created_at).
	att, err := store.CreateConfluenceFile(ctx, confluence.CreateConfluenceFileData{
		KbID: kbID, Name: "anhang.pdf", Type: "application/pdf", Size: 8,
		Origin: "confluence", StoragePath: "confluence/attachments/p1-anhang.pdf",
		ConfluenceSourceID: sourceID, ConfluencePageID: "p1",
	})
	if err != nil {
		t.Fatalf("CreateConfluenceFile (attachment): %v", err)
	}

	var gotPage *time.Time
	if err := pool.QueryRow(ctx, `SELECT published_at FROM files WHERE id = $1::uuid`, page.ID).Scan(&gotPage); err != nil {
		t.Fatalf("read back page: %v", err)
	}
	if gotPage == nil || !gotPage.UTC().Equal(published) {
		t.Fatalf("page published_at = %v, want %v", gotPage, published)
	}

	var gotAtt *time.Time
	if err := pool.QueryRow(ctx, `SELECT published_at FROM files WHERE id = $1::uuid`, att.ID).Scan(&gotAtt); err != nil {
		t.Fatalf("read back attachment: %v", err)
	}
	if gotAtt != nil {
		t.Fatalf("attachment published_at = %v, want NULL", gotAtt)
	}
}
