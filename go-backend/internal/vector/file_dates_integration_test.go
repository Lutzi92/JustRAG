//go:build integration

// fileCreatedTimes reads the main DB with a uuid[] parameter, so only a live
// Postgres can prove the pgx encoding and the effective-date COALESCE work.
// Skipped when DB_* env is unset.

package vector

import (
	"context"
	"testing"
	"time"
)

// TestFileCreatedTimes_UsesEffectiveDateAndUUIDArray seeds two files — one
// whose publication date is newer than its ingest timestamp, one with no
// publication date — and asserts the recency boost sees the effective date
// for both.
//
// It also pins the parameter shape: the query passes []string against
// `id = ANY($1::uuid[])`. `id::text = ANY($1)` would return the same rows but
// as a Seq Scan over files, on a query that runs on every chat turn with a
// recency boost enabled (EXPLAIN on dev: "Seq Scan on files" vs
// "Index Scan using files_pkey"). A malformed id must therefore surface as an
// error rather than silently matching nothing — asserted below.
func TestFileCreatedTimes_UsesEffectiveDateAndUUIDArray(t *testing.T) {
	mainPool := openBM25TestPool(t, "DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME")
	if mainPool == nil {
		t.Skip("file date tests require DB_* env (main Postgres)")
	}
	ctx := context.Background()

	var kbID string
	if err := mainPool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('vector-file-dates', 'public')
		RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		mainPool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})

	now := time.Now().UTC()
	ingested := now.AddDate(-2, 0, 0)
	publishedAt := now.AddDate(0, 0, -5)

	seed := func(name string, created time.Time, published *time.Time) string {
		t.Helper()
		var id string
		if err := mainPool.QueryRow(ctx, `
			INSERT INTO files (kb_id, name, type, status, storage_path, created_at, published_at)
			VALUES ($1::uuid, $2, 'text/markdown', 'completed', $3, $4, $5)
			RETURNING id::text`, kbID, name, "p/"+name, created, published).Scan(&id); err != nil {
			t.Fatalf("seed file %s: %v", name, err)
		}
		return id
	}
	republished := seed("republished.md", ingested, &publishedAt)
	plain := seed("plain.md", ingested, nil)

	svc := &SearchService{mainDB: mainPool}
	times, err := svc.fileCreatedTimes(ctx, []string{republished, plain})
	if err != nil {
		t.Fatalf("fileCreatedTimes: %v", err)
	}
	if len(times) != 2 {
		t.Fatalf("got %d rows, want 2", len(times))
	}
	if got := times[republished]; got.Sub(publishedAt).Abs() > time.Minute {
		t.Errorf("republished file: effective date = %v, want the publication date %v", got, publishedAt)
	}
	if got := times[plain]; got.Sub(ingested).Abs() > time.Minute {
		t.Errorf("plain file: effective date = %v, want the ingest date %v", got, ingested)
	}

	// A non-uuid id must error rather than be silently ignored — the old
	// `id::text = ANY($1)` shape accepted anything.
	if _, err := svc.fileCreatedTimes(ctx, []string{"not-a-uuid"}); err == nil {
		t.Error("expected an error for a malformed file id")
	}
}
