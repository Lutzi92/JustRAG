// Package sqlexec runs validated router SQL in a time-boxed, read-only
// transaction, with row and byte caps on the returned result. In production
// the pool it wraps is the dedicated SELECT-only pool (JUSTRAG_DB_URL_READONLY),
// the same boundary the sql_query/table_query MCP tools use — never the
// main read/write pool; cmd/eval is the sole exception, wrapping the main
// pool because it has no read-only DSN available (R30). It trusts the
// caller (sqlcheck.Validate) to have already checked the statement shape;
// this package's own defence-in-depth is the transaction's access mode and
// statement_timeout.
package sqlexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Result is the outcome of a single Execute call.
type Result struct {
	Columns   []string
	Rows      []map[string]any
	RowCount  int
	Truncated bool
	Duration  time.Duration
}

// Options bounds a single Execute call. Zero values fall back to the
// package defaults (5s / 200 rows / 64 KiB).
type Options struct {
	Timeout time.Duration // statement_timeout; 0 = 5s
	RowCap  int           // 0 = 200
	ByteCap int           // 0 = 64 KiB (serialised cell text)
}

// Executor runs a single validated SQL statement and returns its result.
type Executor interface {
	Execute(ctx context.Context, sql string, opts Options) (*Result, error)
}

type readOnly struct{ pool *pgxpool.Pool }

// NewReadOnly wraps a pool. Every Execute runs in BEGIN READ ONLY with
// SET LOCAL statement_timeout and ROLLBACKs at the end (a rollback is
// correct even for a successful SELECT: nothing was written, and it avoids
// holding a transaction open past the call).
func NewReadOnly(pool *pgxpool.Pool) Executor { return &readOnly{pool: pool} }

func (e *readOnly) Execute(ctx context.Context, sql string, opts Options) (*Result, error) {
	opts = resolveOptions(opts)
	start := time.Now()
	tx, err := e.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("sqlexec: begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // best-effort; the tx is read-only and the conn will be reset regardless

	// statement_timeout cannot be a bound parameter (Postgres rejects SET
	// with $1); the value is an int we computed, never user text.
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", opts.Timeout.Milliseconds())); err != nil {
		return nil, fmt.Errorf("sqlexec: statement_timeout: %w", err)
	}

	rs, err := tx.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rs.Close()

	res := &Result{}
	for _, fd := range rs.FieldDescriptions() {
		res.Columns = append(res.Columns, string(fd.Name))
	}

	bytes := 0
	for rs.Next() {
		if len(res.Rows) >= opts.RowCap {
			res.Truncated = true
			break
		}
		vals, err := rs.Values()
		if err != nil {
			return nil, err
		}
		row := make(map[string]any, len(vals))
		for i, v := range vals {
			row[res.Columns[i]] = normalise(v)
		}
		b, err := json.Marshal(row)
		if err != nil {
			return nil, fmt.Errorf("sqlexec: marshal row: %w", err)
		}
		bytes += len(b)
		if bytes > opts.ByteCap {
			res.Truncated = true
			break
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}

	res.RowCount = len(res.Rows)
	res.Duration = time.Since(start)
	return res, nil
}

// resolveOptions applies the package defaults (R39: the timeout is also
// clamped to a minimum of 1ms — Postgres treats statement_timeout = 0 as
// "disabled", so a caller-supplied sub-millisecond duration would silently
// turn INTO no timeout at all rather than an aggressively short one).
func resolveOptions(opts Options) Options {
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	} else if opts.Timeout < time.Millisecond {
		opts.Timeout = time.Millisecond
	}
	if opts.RowCap <= 0 {
		opts.RowCap = 200
	}
	if opts.ByteCap <= 0 {
		opts.ByteCap = 64 << 10
	}
	return opts
}

// normalise renders pgx/pgtype values as JSON-safe strings/numbers so rows
// marshal without pgx-specific types (I2). pgtype.Numeric, pgtype.Interval
// and a UUID's [16]byte have no String()/fmt.Stringer, so — unlike every
// other pgtype the default type map decodes to (time.Time, []byte, or a
// type with a String() method) — they would otherwise reach json.Marshal
// as opaque structs (e.g. {"Int":..,"Exp":..,...}) instead of a usable
// value.
func normalise(v any) any {
	switch x := v.(type) {
	case time.Time:
		return x.Format(time.RFC3339)
	case []byte:
		return string(x)
	case [16]byte:
		return uuidString(x)
	case pgtype.Numeric:
		return numericValue(x)
	case pgtype.Interval:
		return intervalValue(x)
	case fmt.Stringer:
		return x.String()
	}
	return v
}

// uuidString renders a UUID's raw bytes in canonical 8-4-4-4-12 hex form.
func uuidString(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// numericValue renders a Postgres NUMERIC as its canonical decimal string
// (e.g. "19.99", "1.5", "3", "NaN") — fix round 2, R47. Round 1 returned a
// float64 for values it judged "exactly representable", but that judgment
// itself was type-dependent noise a caller can't predict from the query
// alone: the same column value ("3") could come back as int-shaped float64
// 3 for one row and a string for another depending on the specific decimal
// stored, and "exactly representable in float64" is not the same property
// as "the value the operator/LLM expects to see" for currency-shaped data.
// A single, predictable string representation is simpler for callers to
// consume (parse once, decide precision themselves) and can never lose or
// misrepresent precision. Integer columns (int2/int4/int8) are unaffected:
// pgx decodes those to native Go int16/int32/int64, which already fall
// through to the default `return v` case below untouched.
func numericValue(n pgtype.Numeric) any {
	if !n.Valid {
		return nil
	}
	if n.NaN {
		return "NaN"
	}
	switch n.InfinityModifier {
	case pgtype.Infinity:
		return "Infinity"
	case pgtype.NegativeInfinity:
		return "-Infinity"
	}
	if canonical, err := n.Value(); err == nil {
		if s, ok := canonical.(string); ok {
			return s
		}
	}
	return fmt.Sprintf("%v", n)
}

// intervalValue renders a Postgres INTERVAL in its canonical text form
// (e.g. "1 day", "3 mons 2 days 00:00:01").
func intervalValue(i pgtype.Interval) any {
	if !i.Valid {
		return nil
	}
	if v, err := i.Value(); err == nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return fmt.Sprintf("%v", i)
}
