//go:build integration

// Materializer tests require a live main Postgres (knowledge_bases, files,
// the tabular schema from migration 0048). Gated by the `integration` build
// tag; skipped when DB_* env is unset.

package tabular

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("tabular integration tests require DB_* env (main Postgres)")
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

func TestMaterializeAggregations(t *testing.T) {
	ctx := context.Background()
	pool := openMainPool(t)

	// Seed a user + KB + file so the catalog FKs resolve.
	var userID, kbID, fileID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1,'x') RETURNING id::text`,
		fmt.Sprintf("tab-test-%d", os.Getpid())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) })
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id) VALUES ('tab', $1) RETURNING id::text`, userID).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO files (kb_id, file_name, status) VALUES ($1,'n.csv','completed') RETURNING id::text`, kbID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "n.csv")
	os.WriteFile(p, []byte("Name,Revenue\nAnn,100\nBob,250\n"), 0o600)

	m := NewMaterializer(pool)
	res, err := m.Materialize(ctx, p, "n.csv", fileID, kbID, SemanticOptions{})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(res.Sheets) != 1 || res.Sheets[0].RowCount != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	t.Cleanup(func() { m.DropTablesForFile(ctx, fileID) })

	var sum float64
	q := fmt.Sprintf(`SELECT SUM(revenue) FROM %s.%q`, TabularSchema, res.Sheets[0].TableName)
	if err := pool.QueryRow(ctx, q).Scan(&sum); err != nil {
		t.Fatalf("aggregate query: %v", err)
	}
	if sum != 350 {
		t.Fatalf("SUM(revenue) = %v, want 350", sum)
	}
}

func TestMaterializeSemanticRowChunks(t *testing.T) {
	ctx := context.Background()
	pool := openMainPool(t)
	// (reuse the same user/KB/file seeding pattern as TestMaterializeAggregations)
	var userID, kbID, fileID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1,'x') RETURNING id::text`,
		fmt.Sprintf("tab-sem-%d", os.Getpid())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) })
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id) VALUES ('tab',$1) RETURNING id::text`, userID).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO files (kb_id, file_name, status) VALUES ($1,'n.csv','completed') RETURNING id::text`, kbID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "n.csv")
	os.WriteFile(p, []byte("id,notes\n1,customer reported intermittent latency during peak load\n2,billing discrepancy on the march invoice escalated to finance\n"), 0o600)

	m := NewMaterializer(pool)
	res, err := m.Materialize(ctx, p, "n.csv", fileID, kbID, SemanticOptions{Enabled: true, MinAvgLen: 16, MinDistinctRatio: 0.6})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	t.Cleanup(func() { m.DropTablesForFile(ctx, fileID) })
	s := res.Sheets[0]
	if len(s.RowChunks) != 2 {
		t.Fatalf("want 2 row-chunks, got %d", len(s.RowChunks))
	}
	// _rowid populated 1..N and queryable.
	var maxID int64
	if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT MAX(_rowid) FROM %s.%q`, TabularSchema, s.TableName)).Scan(&maxID); err != nil {
		t.Fatalf("_rowid query: %v", err)
	}
	if maxID != 2 {
		t.Fatalf("MAX(_rowid) = %d, want 2", maxID)
	}
}
