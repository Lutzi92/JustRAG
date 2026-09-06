//go:build integration

// The published_at write in CreateGitRepoFile (W4-R11): every file of a sync
// carries the HEAD commit's committer time. Requires a live main Postgres;
// skipped when DB_* env is unset. Note the repo .env sets DB_HOST=db, so run
// with DB_HOST=localhost or these skip silently.

package gitrepo_test

import (
	"context"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/gitrepo"
)

// Mutation: drop published_at from the CreateGitRepoFile INSERT column list
// (and its $9 placeholder) → the row comes back NULL and this fails.
func TestCreateGitRepoFile_WritesPublishedAt(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	store := gitrepo.NewStore(pool)

	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('gitrepo-published-at', 'public')
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

	commitAt := time.Date(2026, 3, 17, 11, 5, 0, 0, time.UTC)
	withDate, err := store.CreateGitRepoFile(ctx, gitrepo.CreateGitRepoFileInput{
		KbID: kbID, Name: "README.md", Type: "text/markdown", Size: 9,
		StoragePath: "gitrepo/x/a", GitRepoSourceID: srcID,
		GitFilePath: "README.md", GitBlobSHA: "deadbeef",
		PublishedAt: &commitAt,
	})
	if err != nil {
		t.Fatalf("CreateGitRepoFile with published_at: %v", err)
	}
	// A repository whose HEAD carried no usable commit time must still
	// ingest, with published_at NULL rather than a guessed date.
	withoutDate, err := store.CreateGitRepoFile(ctx, gitrepo.CreateGitRepoFileInput{
		KbID: kbID, Name: "main.go", Type: "text/plain", Size: 12,
		StoragePath: "gitrepo/x/b", GitRepoSourceID: srcID,
		GitFilePath: "main.go", GitBlobSHA: "cafebabe",
	})
	if err != nil {
		t.Fatalf("CreateGitRepoFile without published_at: %v", err)
	}

	var got *time.Time
	if err := pool.QueryRow(ctx, `SELECT published_at FROM files WHERE id = $1::uuid`, withDate).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got == nil || !got.UTC().Equal(commitAt) {
		t.Fatalf("published_at = %v, want %v", got, commitAt)
	}

	var none *time.Time
	if err := pool.QueryRow(ctx, `SELECT published_at FROM files WHERE id = $1::uuid`, withoutDate).Scan(&none); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if none != nil {
		t.Fatalf("published_at = %v, want NULL", none)
	}
}
