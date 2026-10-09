//go:build integration

package adkbridge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var schemaSeq atomic.Int64

// isolatedPool returns a pool whose search_path is a fresh schema with the
// Up sections of the named migrations applied, dropped on cleanup.
func isolatedPool(t *testing.T, migrations ...string) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("adkbridge integration tests require DB_* env (main Postgres)")
	}
	base := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	schema := fmt.Sprintf("adk_test_%d_%d", time.Now().UnixNano(), schemaSeq.Add(1))
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	// public stays on the path so migrations can reference users/knowledge_bases.
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	// A schema-local knowledge_bases stub shadows public's (search_path), so
	// the migrations' kb_id FK targets it and seedKB never inserts into or
	// deletes from public.knowledge_bases, whose cascades lock tables other
	// test binaries are migrating concurrently (40P01).
	if _, err := pool.Exec(ctx, `CREATE TABLE knowledge_bases (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL)`); err != nil {
		t.Fatalf("create knowledge_bases stub: %v", err)
	}
	for _, m := range migrations {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "main", m))
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		up := strings.SplitN(string(raw), "-- +goose Down", 2)[0]
		// Test binaries of other packages add FKs to and delete from
		// public.users concurrently; a deadlock there is retried.
		for attempt := 1; ; attempt++ {
			_, err := pool.Exec(ctx, up)
			var pgErr *pgconn.PgError
			if err != nil && attempt < 5 && errors.As(err, &pgErr) && pgErr.Code == "40P01" {
				time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
				continue
			}
			if err != nil {
				t.Fatalf("apply %s: %v", m, err)
			}
			break
		}
	}
	return pool
}
