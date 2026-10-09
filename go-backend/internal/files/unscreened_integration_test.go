//go:build integration

// ListUnscreenedUserFiles: which files a KB publish must still screen.
// Pool/seed helpers are shared with store_pg_integration_test.go.

package files_test

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/files"
)

func TestListUnscreenedUserFiles(t *testing.T) {
	pool := openMainPool(t)
	store := files.NewStore(pool)
	ctx := context.Background()

	// seedErrorFile: origin upload (column default), detail NULL → listed.
	kbID, wantID := seedErrorFile(t, pool, "completed")

	insert := func(name, origin, status, detail string) {
		t.Helper()
		var id string
		err := pool.QueryRow(ctx, `
			INSERT INTO files (kb_id, name, type, status, storage_path, origin, injection_detail)
			VALUES ($1::uuid, $2, 'application/pdf', $3, 'u/k/x', $4, NULLIF($5, '')::jsonb)
			RETURNING id::text`, kbID, name, status, origin, detail).Scan(&id)
		if err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
		t.Cleanup(func() {
			pool.Exec(ctx, `DELETE FROM files WHERE id = $1::uuid`, id) //nolint:errcheck
		})
	}
	insert("text.pdf", "text", "completed", "")
	insert("url.pdf", "url", "completed", "")
	insert("research.pdf", "research", "completed", "")
	insert("rss.pdf", "rss", "completed", "")
	insert("screened.pdf", "upload", "completed", `{"screened_at":"2026-01-01T00:00:00Z"}`)
	insert("broken.pdf", "upload", "error", "")

	got, err := store.ListUnscreenedUserFiles(ctx, kbID, []string{"upload", "text", "url", "research"})
	if err != nil {
		t.Fatalf("ListUnscreenedUserFiles: %v", err)
	}
	names := map[string]string{}
	for _, f := range got {
		names[f.Name] = f.Origin
	}
	if len(got) != 4 || names["doc.pdf"] != "upload" || names["text.pdf"] != "text" ||
		names["url.pdf"] != "url" || names["research.pdf"] != "research" {
		t.Fatalf("got %+v, want exactly upload/text/url/research rows", got)
	}
	found := false
	for _, f := range got {
		found = found || f.ID == wantID
	}
	if !found {
		t.Errorf("seeded file %s missing from %+v", wantID, got)
	}
}
