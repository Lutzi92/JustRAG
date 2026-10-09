//go:build integration

// Integration test for the last_ingested_at card aggregate — the KB card's
// „Aktualisiert" line, which means "when content was last ingested".
// Requires a live main Postgres; skipped when DB_* env is unset.

package kb_test

import (
	"context"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/kb"
)

// TestKBCards_LastIngestedAt pins what counts as "last ingested".
//
// ORACLE: the fixture's dates and statuses, with the expected answer worked
// out by hand from the product rule (ingest time of successfully ingested
// files; the worker's progress_updated_at, falling back to created_at) — not
// read back from the code under test.
//
//	KB "mixed":
//	  a.md  completed  created 2020-01-01  progress 2026-03-05  published 2026-09-01  → 2026-03-05 (MAX)
//	  b.md  partial    created 2026-02-01  progress —                                 → 2026-02-01
//	  c.md  error      created 2026-05-01  progress 2026-05-02                        → ignored
//	  d.md  pending    created 2026-06-01  progress —                                 → ignored
//	  e.md  processing created 2026-04-01  progress 2026-04-02                        → ignored
//	KB "partial-only": one partial file created 2026-02-01, never stamped → 2026-02-01
//	KB "never-ingested": one error and one pending file                   → nil
//
// Mutations, each caught here:
//   - MAX(created_at) (no progress_updated_at)       → mixed = 2026-02-01
//   - no status filter                               → mixed = 2026-06-01
//   - COALESCE(published_at, …) (document date)      → mixed = 2026-09-01
//   - MAX(progress_updated_at) (no created_at fallback) → partial-only = nil
//   - status filter without 'partial'                → partial-only = nil
//   - failed/pending counted                         → never-ingested ≠ nil
func TestKBCards_LastIngestedAt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := insertUser(t, pool, "kbcards-ingested")

	newKB := func(name string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO knowledge_bases (name, user_id) VALUES ($1, $2::uuid)
			RETURNING id::text`, name, userID).Scan(&id); err != nil {
			t.Fatalf("insert kb %s: %v", name, err)
		}
		t.Cleanup(func() {
			pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) //nolint:errcheck
		})
		// ListKnowledgeBases selects on kb_members, which a raw INSERT does
		// not create.
		if _, err := pool.Exec(ctx,
			`INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'owner')`,
			id, userID); err != nil {
			t.Fatalf("insert kb_members owner row: %v", err)
		}
		return id
	}
	day := func(s string) time.Time {
		t.Helper()
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		return d.UTC()
	}
	ptr := func(s string) *time.Time { d := day(s); return &d }

	seed := func(kbID, name, status string, created time.Time, progress, published *time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO files (kb_id, name, type, status, storage_path, created_at, progress_updated_at, published_at)
			VALUES ($1::uuid, $2, 'text/markdown', $3, $4, $5, $6, $7)`,
			kbID, name, status, "p/"+kbID+"/"+name, created, progress, published); err != nil {
			t.Fatalf("seed file %s: %v", name, err)
		}
	}

	mixed := newKB("cards-ingested-mixed")
	seed(mixed, "a.md", "completed", day("2020-01-01"), ptr("2026-03-05"), ptr("2026-09-01"))
	seed(mixed, "b.md", "partial", day("2026-02-01"), nil, nil)
	seed(mixed, "c.md", "error", day("2026-05-01"), ptr("2026-05-02"), nil)
	seed(mixed, "d.md", "pending", day("2026-06-01"), nil, nil)
	seed(mixed, "e.md", "processing", day("2026-04-01"), ptr("2026-04-02"), nil)

	partialOnly := newKB("cards-ingested-partial")
	seed(partialOnly, "p.md", "partial", day("2026-02-01"), nil, nil)

	never := newKB("cards-ingested-never")
	seed(never, "x.md", "error", day("2026-07-01"), ptr("2026-07-02"), nil)
	seed(never, "y.md", "pending", day("2026-08-01"), nil, nil)

	rows, err := kb.NewStore(pool).ListKnowledgeBases(ctx, userID, 50, 0)
	if err != nil {
		t.Fatalf("ListKnowledgeBases: %v", err)
	}
	byID := map[string]*kb.KBRow{}
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}

	for _, tc := range []struct {
		name string
		kbID string
		want *time.Time
	}{
		{"mixed", mixed, ptr("2026-03-05")},
		{"partial-only", partialOnly, ptr("2026-02-01")},
		{"never-ingested", never, nil},
	} {
		got := byID[tc.kbID]
		if got == nil {
			t.Fatalf("%s: KB not returned", tc.name)
		}
		switch {
		case tc.want == nil && got.LastIngestedAt != nil:
			t.Errorf("%s: lastIngestedAt = %v, want nil — no file was ingested successfully", tc.name, got.LastIngestedAt)
		case tc.want != nil && (got.LastIngestedAt == nil || !got.LastIngestedAt.UTC().Equal(*tc.want)):
			t.Errorf("%s: lastIngestedAt = %v, want %v", tc.name, got.LastIngestedAt, tc.want.Format("2006-01-02"))
		}
	}
}
