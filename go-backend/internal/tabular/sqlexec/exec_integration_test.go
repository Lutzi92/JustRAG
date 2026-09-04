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

// TestExecuteNormalisesIntervalUUID is the I2 fix: pgtype.Interval and a
// UUID's [16]byte have no String()/fmt.Stringer, so without dedicated
// cases in normalise() they would reach the JSON row as an opaque struct
// (Interval) or an array of 16 small integers (the UUID bytes) instead of
// a usable value. (Numeric is covered separately by
// TestExecuteNormalisesNumericAsCanonicalString — R47 changed its shape.)
func TestExecuteNormalisesIntervalUUID(t *testing.T) {
	pool := openMainPool(t)
	exec := NewReadOnly(pool)
	res, err := exec.Execute(context.Background(),
		"SELECT INTERVAL '1 day' AS i, gen_random_uuid() AS u", Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.RowCount != 1 {
		t.Fatalf("expected 1 row, got %d", res.RowCount)
	}
	row := res.Rows[0]

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

// TestExecuteNormalisesNumericAsCanonicalString is the R47 fix: NUMERIC
// always renders as its canonical decimal string, never a float64 — a
// single, predictable shape regardless of which specific decimal value
// happens to be exactly representable in IEEE-754 double precision.
// Integer columns (int2/int4/int8) are unaffected: pgx decodes those to a
// native Go int type, never pgtype.Numeric, so "7::int" comes back as a Go
// int (pgx v5.10 decodes int4 to int32 specifically), not a string.
func TestExecuteNormalisesNumericAsCanonicalString(t *testing.T) {
	pool := openMainPool(t)
	exec := NewReadOnly(pool)
	res, err := exec.Execute(context.Background(),
		"SELECT 1.5::numeric AS a, 19.99::numeric AS b, 3::numeric AS c, 7::int AS d", Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.RowCount != 1 {
		t.Fatalf("expected 1 row, got %d", res.RowCount)
	}
	row := res.Rows[0]

	wantStrings := map[string]string{"a": "1.5", "b": "19.99", "c": "3"}
	for col, want := range wantStrings {
		got, ok := row[col].(string)
		if !ok {
			t.Errorf("%s: expected string, got %T (%v)", col, row[col], row[col])
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", col, got, want)
		}
	}

	switch d := row["d"].(type) {
	case int32:
		if d != 7 {
			t.Errorf("d = %v, want 7", d)
		}
	case int64:
		if d != 7 {
			t.Errorf("d = %v, want 7", d)
		}
	default:
		t.Errorf("d: expected a Go int type (int32/int64), got %T (%v)", row["d"], row["d"])
	}
}
