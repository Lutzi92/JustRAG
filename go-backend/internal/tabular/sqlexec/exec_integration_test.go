//go:build integration

// Requires a live main Postgres (DB_* env); skipped otherwise (openMainPool
// below, copied from internal/tabular/materializer_integration_test.go so
// this leaf package stays dependency-free of internal/tabular).

package sqlexec

import (
	"context"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("sqlexec integration tests require DB_* env (main Postgres)")
	}
	dsn := "postgres://" + url.QueryEscape(os.Getenv("DB_USER")) + ":" + url.QueryEscape(os.Getenv("DB_PASSWORD")) +
		"@" + host + ":" + port + "/" + name
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestExecuteStatementTimeout(t *testing.T) {
	pool := openMainPool(t)
	exec := NewReadOnly(pool)
	_, err := exec.Execute(context.Background(), "SELECT pg_sleep(2)", Options{Timeout: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("expected a statement-timeout error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "statement timeout") {
		t.Fatalf("expected a statement timeout error, got: %v", err)
	}
}

func TestExecuteReadOnlyRejectsWrites(t *testing.T) {
	pool := openMainPool(t)
	exec := NewReadOnly(pool)
	_, err := exec.Execute(context.Background(), "CREATE TEMP TABLE x(a int)", Options{})
	if err == nil {
		t.Fatal("expected the read-only transaction to reject a CREATE, got nil")
	}
}

func TestExecuteRowCap(t *testing.T) {
	pool := openMainPool(t)
	exec := NewReadOnly(pool)
	res, err := exec.Execute(context.Background(), "SELECT * FROM generate_series(1,5) AS s", Options{RowCap: 2})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.RowCount != 2 || len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d (rows=%v)", res.RowCount, res.Rows)
	}
	if !res.Truncated {
		t.Fatal("expected Truncated=true")
	}
}

// TestExecuteNormalisesNumericIntervalUUID is the I2 fix: pgtype.Numeric,
// pgtype.Interval and a UUID's [16]byte have no String()/fmt.Stringer, so
// without dedicated cases in normalise() they would reach the JSON row as
// opaque structs instead of a usable value.
func TestExecuteNormalisesNumericIntervalUUID(t *testing.T) {
	pool := openMainPool(t)
	exec := NewReadOnly(pool)
	res, err := exec.Execute(context.Background(),
		"SELECT 1.5::numeric AS n, INTERVAL '1 day' AS i, gen_random_uuid() AS u", Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.RowCount != 1 {
		t.Fatalf("expected 1 row, got %d", res.RowCount)
	}
	row := res.Rows[0]

	n, ok := row["n"].(float64)
	if !ok {
		t.Fatalf("n: expected float64, got %T (%v)", row["n"], row["n"])
	}
	if n != 1.5 {
		t.Errorf("n = %v, want 1.5", n)
	}

	iv, ok := row["i"].(string)
	if !ok {
		t.Fatalf("i: expected string, got %T (%v)", row["i"], row["i"])
	}
	if iv == "" {
		t.Error("i: expected a non-empty interval text form")
	}

	u, ok := row["u"].(string)
	if !ok {
		t.Fatalf("u: expected string, got %T (%v)", row["u"], row["u"])
	}
	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	if !uuidRe.MatchString(u) {
		t.Errorf("u = %q, want a canonical UUID string", u)
	}
}
