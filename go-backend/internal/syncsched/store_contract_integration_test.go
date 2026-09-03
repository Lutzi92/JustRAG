//go:build integration

// Exercises the sweeper contract (ListDue / ListUnscheduled / MarkScheduled)
// against a live main Postgres for all three store kinds. The new SQL these
// three methods introduced (nine statements total: three per store) has no
// other coverage — store_pg_test.go in each package only exercises struct
// conversion, and migration_integration_test.go never calls a store. Skipped
// silently when DB_* env is unset; note the repo .env sets DB_HOST=db, so run
// with DB_HOST=localhost or these tests do not run at all.
//
// package syncsched_test (not syncsched) so it can import the rss,
// confluence and gitrepo store packages without an import cycle — syncsched
// itself must stay free of a dependency on any of the three source packages.

package syncsched_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/gitrepo"
	"github.com/justrag/go-backend/internal/rss"
	"github.com/justrag/go-backend/internal/syncsched"
	"github.com/justrag/go-backend/internal/syncwindow"
)

// findDue returns the DueSource with the given id, if present.
func findDue(list []syncwindow.DueSource, id string) (syncwindow.DueSource, bool) {
	for _, d := range list {
		if d.ID == id {
			return d, true
		}
	}
	return syncwindow.DueSource{}, false
}

// insertConfluenceUserAndConnection seeds a throwaway user + confluence
// connection and returns the connection id. Deleting the user (via
// t.Cleanup) cascades to the connection, which cascades to any
// confluence_sources rows created against it — the whole fixture tears down
// with one DELETE.
func insertConfluenceUserAndConnection(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	username := "syncsched-contract-" + testRunSuffix + "-" + t.Name()
	var userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash) VALUES ($1, 'x') RETURNING id::text`,
		username).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	var connID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO confluence_connections (user_id, token) VALUES ($1, 'x') RETURNING id::text`,
		userID).Scan(&connID); err != nil {
		t.Fatalf("seed confluence connection: %v", err)
	}
	return connID
}

// seedFunc inserts a row with the given sync_schedule/status/next_sync_at
// (next may be nil for NULL) and returns its id. label disambiguates rows
// seeded within the same test (e.g. rss_feeds has a UNIQUE(kb_id, url)).
type seedFunc func(t *testing.T, schedule, status string, next *time.Time, label string) string

func rssSeedFunc(pool *pgxpool.Pool, kbID string) seedFunc {
	return func(t *testing.T, schedule, status string, next *time.Time, label string) string {
		t.Helper()
		ctx := context.Background()
		url := fmt.Sprintf("https://example.invalid/%s-%s-%s.xml", label, testRunSuffix, t.Name())
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO rss_feeds (kb_id, url, sync_schedule, status, next_sync_at)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text`, kbID, url, schedule, status, next).Scan(&id); err != nil {
			t.Fatalf("seed rss_feeds row (%s): %v", label, err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM rss_feeds WHERE id = $1`, id)
		})
		return id
	}
}

func confluenceSeedFunc(pool *pgxpool.Pool, kbID, connID string) seedFunc {
	return func(t *testing.T, schedule, status string, next *time.Time, label string) string {
		t.Helper()
		ctx := context.Background()
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO confluence_sources (kb_id, connection_id, space_key, sync_schedule, status, next_sync_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id::text`, kbID, connID, "SPACE-"+label, schedule, status, next).Scan(&id); err != nil {
			t.Fatalf("seed confluence_sources row (%s): %v", label, err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM confluence_sources WHERE id = $1`, id)
		})
		return id
	}
}

func gitRepoSeedFunc(pool *pgxpool.Pool, kbID string) seedFunc {
	return func(t *testing.T, schedule, status string, next *time.Time, label string) string {
		t.Helper()
		ctx := context.Background()
		repoURL := fmt.Sprintf("https://example.invalid/%s-%s-%s.git", label, testRunSuffix, t.Name())
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO git_repo_sources (kb_id, repo_url, sync_schedule, status, next_sync_at)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text`, kbID, repoURL, schedule, status, next).Scan(&id); err != nil {
			t.Fatalf("seed git_repo_sources row (%s): %v", label, err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM git_repo_sources WHERE id = $1`, id)
		})
		return id
	}
}

// runSweeperContractRoundTrip is shared across all three store kinds: the
// three stores are deliberately parallel implementations of the same
// contract, and this asserts that contract holds against real Postgres for
// each of them.
func runSweeperContractRoundTrip(t *testing.T, store syncsched.SourceStore, seed seedFunc) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	t.Run("unscheduled_to_due_round_trip", func(t *testing.T) {
		id := seed(t, syncwindow.ScheduleDaily, "active", nil, "roundtrip")

		unscheduled, err := store.ListUnscheduled(ctx)
		if err != nil {
			t.Fatalf("ListUnscheduled: %v", err)
		}
		got, ok := findDue(unscheduled, id)
		if !ok {
			t.Fatalf("expected %s in ListUnscheduled before any stamp", id)
		}
		if got.Schedule != syncwindow.ScheduleDaily {
			t.Fatalf("expected schedule %q carried through ListUnscheduled, got %q", syncwindow.ScheduleDaily, got.Schedule)
		}

		due, err := store.ListDue(ctx, now)
		if err != nil {
			t.Fatalf("ListDue: %v", err)
		}
		if _, ok := findDue(due, id); ok {
			t.Fatal("must not appear in ListDue before being stamped (next_sync_at is NULL)")
		}

		// Stamp into the future: must disappear from ListUnscheduled but not
		// yet appear in ListDue.
		if err := store.MarkScheduled(ctx, id, future); err != nil {
			t.Fatalf("MarkScheduled(future): %v", err)
		}
		unscheduled2, err := store.ListUnscheduled(ctx)
		if err != nil {
			t.Fatalf("ListUnscheduled after stamp: %v", err)
		}
		if _, ok := findDue(unscheduled2, id); ok {
			t.Fatal("must disappear from ListUnscheduled once stamped")
		}
		due2, err := store.ListDue(ctx, now)
		if err != nil {
			t.Fatalf("ListDue after future stamp: %v", err)
		}
		if _, ok := findDue(due2, id); ok {
			t.Fatal("must NOT appear in ListDue while stamped in the future")
		}

		// Re-stamp into the past: must now appear in ListDue.
		if err := store.MarkScheduled(ctx, id, past); err != nil {
			t.Fatalf("MarkScheduled(past): %v", err)
		}
		due3, err := store.ListDue(ctx, now)
		if err != nil {
			t.Fatalf("ListDue after past stamp: %v", err)
		}
		if _, ok := findDue(due3, id); !ok {
			t.Fatal("must appear in ListDue once stamped in the past")
		}
	})

	t.Run("manual_schedule_excluded_from_both_lists", func(t *testing.T) {
		idNoStamp := seed(t, "manual", "active", nil, "manual-nostamp")
		idPastStamp := seed(t, "manual", "active", &past, "manual-paststamp")

		unscheduled, err := store.ListUnscheduled(ctx)
		if err != nil {
			t.Fatalf("ListUnscheduled: %v", err)
		}
		if _, ok := findDue(unscheduled, idNoStamp); ok {
			t.Fatal("a manual-schedule row with NULL next_sync_at must never appear in ListUnscheduled")
		}

		due, err := store.ListDue(ctx, now)
		if err != nil {
			t.Fatalf("ListDue: %v", err)
		}
		if _, ok := findDue(due, idPastStamp); ok {
			t.Fatal("a manual-schedule row must never appear in ListDue, even with next_sync_at in the past")
		}
	})

	// F1: an errored source must stay on the schedule (status IN
	// ('active','error')); a paused one must stay off it.
	t.Run("F1_error_status_stays_scheduled_paused_does_not", func(t *testing.T) {
		errID := seed(t, syncwindow.ScheduleDaily, "error", &past, "error-status")
		pausedID := seed(t, syncwindow.ScheduleDaily, "paused", &past, "paused-status")

		due, err := store.ListDue(ctx, now)
		if err != nil {
			t.Fatalf("ListDue: %v", err)
		}
		if _, ok := findDue(due, errID); !ok {
			t.Fatal("F1: an errored source must still be swept — one transient sync failure must not permanently drop it from the schedule")
		}
		if _, ok := findDue(due, pausedID); ok {
			t.Fatal("a paused source must stay off the schedule (ListDue)")
		}
	})
}

func TestSweeperContract_RSS(t *testing.T) {
	pool := testPool(t)
	kbID := insertKB(t, pool)
	store := rss.NewStore(pool)
	runSweeperContractRoundTrip(t, store, rssSeedFunc(pool, kbID))
}

func TestSweeperContract_Confluence(t *testing.T) {
	pool := testPool(t)
	kbID := insertKB(t, pool)
	connID := insertConfluenceUserAndConnection(t, pool)
	store := confluence.NewStore(pool)
	runSweeperContractRoundTrip(t, store, confluenceSeedFunc(pool, kbID, connID))
}

func TestSweeperContract_GitRepo(t *testing.T) {
	pool := testPool(t)
	kbID := insertKB(t, pool)
	store := gitrepo.NewStore(pool)
	runSweeperContractRoundTrip(t, store, gitRepoSeedFunc(pool, kbID))
}
