// Package sqlexec runs validated router SQL against the main Postgres pool
// in a time-boxed, read-only transaction, with row and byte caps on the
// returned result. It trusts the caller (sqlcheck.Validate) to have already
// checked the statement shape; this package's only defence-in-depth is the
// transaction's access mode and statement_timeout.
package sqlexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.RowCap <= 0 {
		opts.RowCap = 200
	}
	if opts.ByteCap <= 0 {
		opts.ByteCap = 64 << 10
	}
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

// normalise renders pgtype values (numeric, date, timestamp) as JSON-safe
// strings/numbers so rows marshal without pgx-specific types.
func normalise(v any) any {
	switch x := v.(type) {
	case time.Time:
		return x.Format(time.RFC3339)
	case []byte:
		return string(x)
	case fmt.Stringer:
		return x.String()
	}
	return v
}
