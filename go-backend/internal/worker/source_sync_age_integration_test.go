//go:build integration

// Executes the rag_source_sync_age_seconds query against a live main
// Postgres. Skipped when DB_* env is unset.
//
// A unit test cannot cover this: the query is a three-way UNION over tables
// whose column names differ per source kind, so the only thing that can
// verify it is Postgres. Mutation: rename any of the three last-attempt
// columns in the SQL → this fails with a 42703.

package worker

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/justrag/go-backend/internal/observability"
)

func syncAgeTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("source sync age tests require DB_* env (main Postgres)")
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestSourceSyncAgeSQL_RunsAgainstPostgres(t *testing.T) {
	pool := syncAgeTestPool(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, sourceSyncAgeSQL)
	if err != nil {
		t.Fatalf("sourceSyncAgeSQL: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kbID, kind string
		var age float64
		if err := rows.Scan(&kbID, &kind, &age); err != nil {
			t.Fatalf("scan: %v (the three result columns must stay text/text/float8)", err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	// refreshSourceSyncAge itself must be safe on a nil pool — the worker
	// wires it from the maintenance loop, which may run without a main DB.
	refreshSourceSyncAge(ctx, nil)
}

// TestRefreshSourceSyncAge_DropsSeriesOfDeletedSource is the end-to-end form
// of the snapshot rule: a real source produces a series, and once the source
// is gone the next refresh must not leave that series behind. A Set-only
// refresher freezes it at its last age forever, so an alert raised by a feed
// that has since been deleted could never resolve.
//
// Mutation: remove the observability.ResetSourceSyncAge() call from
// refreshSourceSyncAge → this fails.
func TestRefreshSourceSyncAge_DropsSeriesOfDeletedSource(t *testing.T) {
	pool := syncAgeTestPool(t)
	ctx := context.Background()

	var kbID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, visibility) VALUES ('sync-age-refresh', 'public')
		RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) //nolint:errcheck
	})

	var feedID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO rss_feeds (kb_id, url, status, last_polled_at, last_success_at)
		VALUES ($1::uuid, 'https://example.invalid/age.xml', 'active', NOW() - INTERVAL '1 hour', NOW() - INTERVAL '1 hour')
		RETURNING id::text`, kbID).Scan(&feedID); err != nil {
		t.Fatalf("seed feed: %v", err)
	}

	refreshSourceSyncAge(ctx, pool)
	if got := testutil.ToFloat64(observability.SourceSyncAgeForTest().WithLabelValues("rss", kbID)); got < 3000 {
		t.Fatalf("expected a ~1h age series for the seeded feed, got %v", got)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM rss_feeds WHERE id = $1::uuid`, feedID); err != nil {
		t.Fatalf("delete feed: %v", err)
	}

	refreshSourceSyncAge(ctx, pool)
	for _, m := range collectSyncAgeSeries(t) {
		if m == kbID {
			t.Error("the deleted feed's series survived the next refresh")
		}
	}
}

// collectSyncAgeSeries returns the kb label value of every currently exported
// rag_source_sync_age_seconds series.
func collectSyncAgeSeries(t *testing.T) []string {
	t.Helper()
	ch := make(chan prometheus.Metric, 4096)
	observability.SourceSyncAgeForTest().Collect(ch)
	close(ch)
	var out []string
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		for _, l := range pb.GetLabel() {
			if l.GetName() == "kb" {
				out = append(out, l.GetValue())
			}
		}
	}
	return out
}
