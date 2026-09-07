//go:build integration

// Store-level test for RagasStatsByKB (Wave 5 / Task 2), pinning the wiring
// onto ragassamples' 24h aggregate (Task 1) rather than re-testing
// DailyStats' own arithmetic (already covered by
// internal/ragassamples/store_integration_test.go). Requires a live main
// Postgres; skipped when DB_* env is unset. openMainPool is shared with
// store_pg_transfer_integration_test.go in this same _test package.

package adminkboverview_test

import (
	"context"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/adminkboverview"
)

// TestRagasStatsByKB_WindowsToTheLast24Hours seeds two ragas_samples rows for
// a throwaway KB — one inside the 24h window, one older — and asserts n=1,
// counting only the in-window row. Rows are cleaned up BY ID (the KB
// cleanup catches the kb_id-scoped row; nothing here is deleted by score
// equality — see the Task 1 fix-round-1 leak this rule exists to prevent).
//
// Mutation: pass a since with no window (e.g. the zero time) instead of
// now-24h → n becomes 2 and this fails.
func TestRagasStatsByKB_WindowsToTheLast24Hours(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()

	var userID, kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ('kboverview-ragas-test', 'x-not-a-real-hash', 'user') RETURNING id::text`).
		Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id) VALUES ('kboverview-ragas-kb', $1::uuid) RETURNING id::text`,
		userID).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM ragas_samples WHERE kb_id = $1::uuid`, kbID) //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID)  //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID)          //nolint:errcheck
	})

	now := time.Now()
	insert := func(score float64, sampledAt time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO ragas_samples (kb_id, faithfulness, answer_relevance, context_precision, judge_model, sampled_at)
			VALUES ($1::uuid, $2, $2, $2, 'kboverview-ragas-test', $3)`,
			kbID, score, sampledAt); err != nil {
			t.Fatalf("seed ragas_samples row: %v", err)
		}
	}
	insert(0.8, now.Add(-1*time.Hour))  // inside the 24h window
	insert(0.2, now.Add(-48*time.Hour)) // outside — must not be counted

	stats, err := adminkboverview.NewStore(pool).RagasStatsByKB(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("RagasStatsByKB: %v", err)
	}
	got, ok := stats[kbID]
	if !ok {
		t.Fatalf("no ragas stats row for the seeded KB")
	}
	if got.N24h != 1 {
		t.Errorf("n24h = %d, want 1 (the 48h-old row must be excluded)", got.N24h)
	}
	// faithfulness is REAL (float4) in the table; AVG()::float8 widens it, so
	// the round-trip is not bit-exact — compare with a tolerance rather than
	// `!= 0.8` (the same REAL-vs-literal trap the Task 1 fix-round-1 note
	// documents for the cleanup predicate).
	if got.Faithfulness == nil || *got.Faithfulness < 0.799 || *got.Faithfulness > 0.801 {
		t.Errorf("faithfulness = %v, want ~0.8", got.Faithfulness)
	}
}
