//go:build integration

// Integration test for the paging order of GET /api/kb
// (ListKnowledgeBases and ListKnowledgeBasesWithUserFilters).
// Requires a live main Postgres; skipped when DB_* env is unset.

package kb_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/kb"
)

// TestListKnowledgeBases_PagingIsStableOnCreatedAtTies pins the ORDER BY
// tiebreak. Every KB below has the same created_at, so created_at alone
// leaves their order undefined and LIMIT/OFFSET pages may skip or repeat
// rows. Paging through with a small limit must return each KB exactly once.
//
// Oracle: the set of KB ids this test inserted itself. The expectation is
// "each of those ids once, nothing else", independent of any order the
// query chooses.
func TestListKnowledgeBases_PagingIsStableOnCreatedAtTies(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := kb.NewStore(pool)
	userID := insertUser(t, pool, "kbpaging-ties")

	const n = 25
	sameInstant := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	want := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO knowledge_bases (name, user_id, created_at)
			VALUES ($1, $2::uuid, $3) RETURNING id::text`,
			fmt.Sprintf("kbpaging-%02d", i), userID, sameInstant).Scan(&id); err != nil {
			t.Fatalf("insert kb %d: %v", i, err)
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
		want[id] = true
	}

	for _, variant := range []struct {
		name string
		list func(limit, offset int) ([]kb.KBRow, error)
	}{
		{"ListKnowledgeBases", func(limit, offset int) ([]kb.KBRow, error) {
			return store.ListKnowledgeBases(ctx, userID, limit, offset)
		}},
		{"ListKnowledgeBasesWithUserFilters", func(limit, offset int) ([]kb.KBRow, error) {
			return store.ListKnowledgeBasesWithUserFilters(ctx, userID, limit, offset)
		}},
	} {
		for _, limit := range []int{2, 3, 7} {
			t.Run(fmt.Sprintf("%s/limit=%d", variant.name, limit), func(t *testing.T) {
				seen := make(map[string]int, n)
				for offset := 0; offset < n+limit; offset += limit {
					page, err := variant.list(limit, offset)
					if err != nil {
						t.Fatalf("page at offset %d: %v", offset, err)
					}
					for _, row := range page {
						seen[row.ID]++
					}
					if len(page) < limit {
						break
					}
				}
				for id, count := range seen {
					if !want[id] {
						t.Errorf("unexpected KB %s in the pages", id)
					} else if count != 1 {
						t.Errorf("KB %s appeared %d times across pages, want exactly 1", id, count)
					}
				}
				for id := range want {
					if seen[id] == 0 {
						t.Errorf("KB %s never appeared in any page", id)
					}
				}
			})
		}
	}
}
