//go:build integration

// Store-level test for the per-KB injection_flagged aggregate (Wave-5 Task
// 6). Requires a live main Postgres; skipped when DB_* env is unset.
// openMainPool is shared with freshness_integration_test.go (same package).

package adminkboverview_test

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/adminkboverview"
)

// TestFileStatsByKB_InjectionFlagged seeds a KB with two flagged and two
// clean files and asserts the count is the flagged ones only.
//
// Mutation: drop the FILTER clause (COUNT(*) instead of COUNT(*) FILTER
// (WHERE injection_flag)) → the count becomes 4 and this fails. Counting a
// NULL detail instead of the flag would also fail: one flagged row here
// deliberately carries no detail, standing in for a screening pass whose
// detail write was lost.
func TestFileStatsByKB_InjectionFlagged(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()

	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('kboverview-injection', 'public')
		RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})

	seed := func(name string, flagged bool, detail *string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO files (kb_id, name, type, status, storage_path, origin, injection_flag, injection_detail)
			VALUES ($1::uuid, $2, 'text/markdown', 'completed', $3, 'rss', $4, $5::jsonb)`,
			kbID, name, "p/"+name, flagged, detail); err != nil {
			t.Fatalf("seed file %s: %v", name, err)
		}
	}
	payload := `{"rule":"ignore_previous","position":1,"snippet":"x","screened_at":"2026-09-06T00:00:00Z"}`
	seed("flagged-with-detail.md", true, &payload)
	seed("flagged-without-detail.md", true, nil)
	seed("screened-clean.md", false, nil)
	seed("never-screened.md", false, nil)

	stats, err := adminkboverview.NewStore(pool).FileStatsByKB(ctx, 180)
	if err != nil {
		t.Fatalf("FileStatsByKB: %v", err)
	}
	fs, ok := stats[kbID]
	if !ok {
		t.Fatal("no stats row for the seeded KB")
	}
	if fs.FileCount != 4 {
		t.Errorf("FileCount = %d, want 4", fs.FileCount)
	}
	if fs.InjectionFlagged != 2 {
		t.Errorf("InjectionFlagged = %d, want 2", fs.InjectionFlagged)
	}
}
