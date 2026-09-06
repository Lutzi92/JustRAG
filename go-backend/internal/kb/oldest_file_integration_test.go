//go:build integration

// Integration test for the oldest_file_at card aggregate (migration 0071).
// Requires a live main Postgres; skipped when DB_* env is unset.

package kb_test

import (
	"context"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/kb"
)

// TestKBCards_OldestFileAt pins that the card's corpus-age aggregate keys on
// the EFFECTIVE date. The KB below holds one file ingested five years ago
// whose publication date is last week, and one ingested last month with no
// publication date — so the oldest effective date is last month, not the
// five-year-old ingest timestamp.
//
// Mutation: swap MIN(COALESCE(published_at, created_at)) for MIN(created_at)
// in kbStatsJoins → the card reports the five-year-old date and this fails.
func TestKBCards_OldestFileAt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := insertUser(t, pool, "kbcards-oldest")

	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id) VALUES ('cards-oldest', $1::uuid)
		RETURNING id::text`, userID).Scan(&kbID); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})
	// ListKnowledgeBases selects on kb_members, which a raw INSERT does not
	// create — see TestListKnowledgeBases_TurnStatsFromUsageLedger.
	if _, err := pool.Exec(ctx,
		`INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'owner')`,
		kbID, userID); err != nil {
		t.Fatalf("insert kb_members owner row: %v", err)
	}

	now := time.Now().UTC()
	ancientIngest := now.AddDate(-5, 0, 0)
	recentPublication := now.AddDate(0, 0, -7)
	monthOld := now.AddDate(0, -1, 0)

	seed := func(name string, created time.Time, published *time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO files (kb_id, name, type, status, storage_path, created_at, published_at)
			VALUES ($1::uuid, $2, 'text/markdown', 'completed', $3, $4, $5)`,
			kbID, name, "p/"+name, created, published); err != nil {
			t.Fatalf("seed file %s: %v", name, err)
		}
	}
	seed("republished.md", ancientIngest, &recentPublication)
	seed("plain.md", monthOld, nil)

	rows, err := kb.NewStore(pool).ListKnowledgeBases(ctx, userID, 50, 0)
	if err != nil {
		t.Fatalf("ListKnowledgeBases: %v", err)
	}
	var found *kb.KBRow
	for i := range rows {
		if rows[i].ID == kbID {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatal("KB not returned")
	}
	if found.OldestFileAt == nil {
		t.Fatal("oldestFileAt must be set once the KB has files")
	}
	// Tolerance covers the timestamptz round trip; the assertion that matters
	// is "a month ago, not five years ago".
	if diff := found.OldestFileAt.UTC().Sub(monthOld); diff > time.Minute || diff < -time.Minute {
		t.Errorf("oldestFileAt = %v, want the month-old effective date %v", found.OldestFileAt.UTC(), monthOld)
	}
}
