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
)

func TestSourceSyncAgeSQL_RunsAgainstPostgres(t *testing.T) {
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("source sync age query test requires DB_* env (main Postgres)")
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

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
