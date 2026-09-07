//go:build integration

// Verifies the Store.List server-side sort contract (W7-R4) against a live
// main Postgres: ORDER BY on the report's aggregate mean_recall/mrr, both
// directions, NULLS LAST for runs with no report. Skipped when DB_* env is
// unset; note the repo .env sets DB_HOST=db, so run with DB_HOST=localhost
// or these tests do not run at all.

package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

// TestStore_List_SortByRecallAndMRR inserts three runs against one KB — two
// completed with distinct mean_recall/mrr in their report, one left without
// a report (queued) — and asserts List orders them correctly for
// Sort="recall"/"mrr" in both directions, with the report-less run always
// last (NULLS LAST), and falls back to created_at DESC when Sort is unset.
func TestStore_List_SortByRecallAndMRR(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	s := NewStore(pool)
	kbID := insertKB(t, pool)

	insertRun := func(recall, mrr float64, withReport bool) uuid.UUID {
		id, err := s.Insert(ctx, Run{
			KBID:           kbID,
			FixtureHash:    "h-" + uuid.NewString(),
			ConfigSnapshot: json.RawMessage(`{}`),
			JudgeEnabled:   false,
			TopK:           10,
		})
		if err != nil {
			t.Fatalf("insert run: %v", err)
		}
		t.Cleanup(func() { _, _, _ = s.Delete(ctx, id) })
		if withReport {
			report := json.RawMessage(fmt.Sprintf(`{"aggregate":{"mean_recall":%f,"mrr":%f}}`, recall, mrr))
			if err := s.MarkCompleted(ctx, id, report); err != nil {
				t.Fatalf("mark completed: %v", err)
			}
		}
		return id
	}

	// Inserted in this order so a plain created_at DESC sort (the default)
	// returns them in reverse insertion order: idNull, idHigh, idLow.
	idLow := insertRun(0.2, 0.9, true)  // low recall, high mrr
	idHigh := insertRun(0.8, 0.1, true) // high recall, low mrr
	idNull := insertRun(0, 0, false)    // no report at all

	order := func(t *testing.T, opts ListOpts) []uuid.UUID {
		t.Helper()
		opts.KBID = &kbID
		opts.Limit = 10
		runs, total, err := s.List(ctx, opts)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if total != 3 {
			t.Fatalf("total = %d, want 3", total)
		}
		ids := make([]uuid.UUID, len(runs))
		for i, r := range runs {
			ids[i] = r.ID
		}
		return ids
	}

	assertOrder := func(t *testing.T, got, want []uuid.UUID) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("order = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("order = %v, want %v", got, want)
			}
		}
	}

	t.Run("recall desc: high before low, report-less last", func(t *testing.T) {
		got := order(t, ListOpts{Sort: "recall", Order: "desc"})
		assertOrder(t, got, []uuid.UUID{idHigh, idLow, idNull})
	})

	t.Run("recall asc: low before high, report-less still last", func(t *testing.T) {
		got := order(t, ListOpts{Sort: "recall", Order: "asc"})
		assertOrder(t, got, []uuid.UUID{idLow, idHigh, idNull})
	})

	t.Run("mrr desc: idLow (mrr 0.9) before idHigh (mrr 0.1), report-less last", func(t *testing.T) {
		got := order(t, ListOpts{Sort: "mrr", Order: "desc"})
		assertOrder(t, got, []uuid.UUID{idLow, idHigh, idNull})
	})

	t.Run("mrr asc: idHigh before idLow, report-less still last", func(t *testing.T) {
		got := order(t, ListOpts{Sort: "mrr", Order: "asc"})
		assertOrder(t, got, []uuid.UUID{idHigh, idLow, idNull})
	})

	t.Run("default (no Sort) falls back to created_at DESC", func(t *testing.T) {
		got := order(t, ListOpts{})
		assertOrder(t, got, []uuid.UUID{idNull, idHigh, idLow})
	})

	t.Run("unrecognised Sort/Order values fall back to created_at DESC rather than erroring", func(t *testing.T) {
		got := order(t, ListOpts{Sort: "bogus", Order: "bogus"})
		assertOrder(t, got, []uuid.UUID{idNull, idHigh, idLow})
	})
}
