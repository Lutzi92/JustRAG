// Package pgxutil provides shared pgx helper functions used across
// domain-specific store implementations.
//
// When to use which helper:
//
//   - Use QueryOne[T] for multi-column row reads that map to a struct.
//     It collapses the boilerplate of acquiring rows, scanning, closing,
//     and translating pgx.ErrNoRows into a nil pointer.
//   - Use QueryRows[T] for multi-row struct reads. Returns an empty
//     (non-nil) slice when there are no matches.
//   - Use pool.QueryRow(ctx, sql, args...).Scan(&v) for single-scalar
//     reads (COUNT(*), a single column SELECT, EXISTS checks). The
//     struct-scanning machinery behind QueryOne is unnecessary overhead
//     for one primitive value, and the call site is shorter and clearer
//     read as a direct Scan.
//
// Nil-row convention for the helpers in this package: QueryOne returns
// (nil, nil) when no rows match (NOT pgx.ErrNoRows). QueryRows returns
// an empty (non-nil) slice. Callers must check for nil pointer / empty
// slice; pgx.ErrNoRows is never propagated from these helpers.
//
// Single-scalar callers using pool.QueryRow().Scan() opt into pgx's
// native ErrNoRows surface — that's expected and supported (the helpers
// are not a blanket replacement for direct Scan).
package pgxutil

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the read+write interface satisfied by both *pgxpool.Pool and
// pgx.Tx (and any pgx.Conn). Accepting this interface lets helpers be used
// from either the pool directly or from inside a transaction without the
// caller falling back to raw tx.Query + pgx.CollectRows boilerplate.
//
// Methods mirror the sqlc-generated DBTX shape so that store helpers which
// do both reads and writes inside a transaction can accept Querier rather
// than the concrete pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Compile-time assertions that both pgxpool.Pool and pgx.Tx satisfy Querier.
// These catch breaking changes in the pgx API at build time rather than at
// the first store-test run.
var (
	_ Querier = (*pgxpool.Pool)(nil)
	_ Querier = (pgx.Tx)(nil)
)

// QueryRows runs sql with args and collects results into a slice of T using
// pgx.RowToStructByName. Returns an empty (non-nil) slice when there are no
// rows; returns nil with the error when the query fails (Go-stdlib convention).
//
// Row lifecycle: pgx.CollectRows drains and closes the underlying pgx.Rows
// before returning, so callers do NOT need (and must not issue) a
// `defer rows.Close()` here. The helper never returns an open iterator.
func QueryRows[T any](ctx context.Context, q Querier, sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	if err != nil {
		return nil, err
	}
	if result == nil {
		return []T{}, nil
	}
	return result, nil
}

// QueryOne runs sql with args and returns a pointer to a single T, or nil when
// no rows match.
//
// QueryOne automatically appends " LIMIT 1" to plain SELECT queries so the
// database short-circuits after the first match instead of streaming the
// entire result set into the client. Statements that already contain a LIMIT
// clause and statements with a RETURNING clause (INSERT/UPDATE/DELETE
// RETURNING — which PostgreSQL does not allow LIMIT on) are passed through
// unchanged.
//
// Row lifecycle: pgx.CollectOneRow closes rows internally; callers must not.
func QueryOne[T any](ctx context.Context, q Querier, sql string, args ...any) (*T, error) {
	rows, err := q.Query(ctx, withLimitOne(sql), args...)
	if err != nil {
		return nil, err
	}
	result, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[T])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// WithTx runs fn inside a pgx transaction with default isolation, committing
// on success and rolling back on error. The deferred Rollback is a no-op once
// Commit has succeeded (pgx tracks the transaction state), so the standard Go
// idiom of `defer Rollback` after a `Begin` is safe and concise.
//
// For non-default isolation levels (e.g. Serializable for read-check-write
// cycles), use WithTxOptions.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	return WithTxOptions(ctx, pool, pgx.TxOptions{}, fn)
}

// WithTxOptions runs fn inside a pgx transaction started with the given
// TxOptions, committing on success and rolling back on error. Use this when
// the call site needs a specific isolation level or access mode (typically
// Serializable for read-check-write cycles that would otherwise be racy
// under READ COMMITTED).
//
// Note: Serializable transactions can fail at commit with PostgreSQL error
// 40001 ("could not serialize access") when a concurrent transaction
// touched overlapping rows. WithTxOptions surfaces that error verbatim;
// callers that need automatic retry should wrap with WithSerializableRetry.
func WithTxOptions(ctx context.Context, pool *pgxpool.Pool, opts pgx.TxOptions, fn func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	// Wrap the commit error so a failed Commit (e.g. a network drop after
	// fn succeeded) is distinguishable in logs from an error returned by fn
	// itself — both would otherwise surface as a bare pgx error at the call
	// site with no indication of which phase failed.
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// maxSerializableRetries is the attempt budget for WithSerializableRetry.
// PostgreSQL serialization failures (40001) and deadlocks (40P01) are
// transient by definition; retrying a small bounded number of times with
// jittered backoff resolves the vast majority of contention in practice
// without masking a real bug. Beyond ~5 attempts the workload is fighting
// itself and a louder failure is more useful than another retry.
const maxSerializableRetries = 5

// WithSerializableRetry runs fn inside a Serializable transaction and
// retries on PostgreSQL serialization failure (SQLSTATE 40001) or deadlock
// (SQLSTATE 40P01) with exponential backoff capped at maxSerializableRetries
// attempts. Non-retryable errors propagate immediately. The context is
// honoured between attempts: a cancelled or deadline-expired context aborts
// the retry loop with ctx.Err().
//
// Use this for short read-check-write cycles where a serialization failure
// indicates contention rather than a logic bug. Long-running fn bodies (or
// fn bodies that perform side effects outside the transaction) are a poor
// fit because retries re-execute the entire body.
func WithSerializableRetry(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	opts := pgx.TxOptions{IsoLevel: pgx.Serializable}
	var lastErr error
	for attempt := 0; attempt < maxSerializableRetries; attempt++ {
		err := WithTxOptions(ctx, pool, opts, fn)
		if err == nil {
			return nil
		}
		if !IsSerializationFailure(err) {
			return err
		}
		lastErr = err
		if attempt == maxSerializableRetries-1 {
			break
		}
		// Backoff with jitter: a deterministic base of 5ms, 10ms, 20ms,
		// 40ms plus a random 0..base/2 offset. The base is short because
		// serialization failures unwind already-completed work; the longer
		// we wait, the more we burn the caller's deadline on something cheap
		// to retry. The jitter desynchronizes goroutines that entered the
		// retry loop together so they don't wake in lock-step and collide on
		// the same rows again — the classic synchronized-retry pitfall.
		base := time.Duration(5*(1<<attempt)) * time.Millisecond
		wait := base + time.Duration(rand.Int64N(int64(base/2)+1))
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
	return lastErr
}

// IsUniqueViolation reports whether err is a PostgreSQL unique-violation
// (SQLSTATE 23505). Use this at store boundaries to translate a driver
// error into store.ErrConflict without sprinkling pgconn.PgError matching
// throughout the codebase.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// IsSerializationFailure reports whether err is a PostgreSQL serialization
// failure (SQLSTATE 40001) or deadlock (SQLSTATE 40P01). Both are transient
// by definition and safe to retry once the conflicting transaction has
// resolved. Used internally by WithSerializableRetry; exported so callers
// that need a bespoke retry policy can compose their own loop.
func IsSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}

// IsUndefinedTable reports whether err is a PostgreSQL undefined-table error
// (SQLSTATE 42P01). Use this instead of strings.Contains(err.Error(), "does
// not exist") when a query targets a dim-keyed / optional table that may not
// have been created yet (HyPE, RAPTOR, etc.) so the caller can fail open
// without coupling to the driver's English error text.
func IsUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

// likePatternEscaper escapes the three LIKE wildcards `%`, `_`, and the
// escape character `\` itself so user-supplied input is treated as a literal
// substring. Defined as a package-level Replacer so the (small) construction
// cost is paid once at init rather than on every call.
var likePatternEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike escapes LIKE/ILIKE metacharacters in s so the result is safe
// to interpolate into a `LIKE $1 ESCAPE '\'` pattern (or wrapped with
// '%' on either side for substring search). Without escaping, a search for
// "100%" would also match "1000", "10000", etc.
//
// SQL side must include `ESCAPE '\'` because the standard does not define
// a default escape character. PostgreSQL accepts the backslash convention
// without it, but being explicit is portable and self-documenting.
func EscapeLike(s string) string {
	return likePatternEscaper.Replace(s)
}

// ClauseBuilder accumulates parameterized SQL fragments (WHERE conditions or
// SET assignments) together with their bind arguments. Each fragment's $N
// placeholder is derived from the running argument count, so the placeholder
// index can never drift out of sync with the args slice — the failure mode of
// hand-threaded `param` counters that increment separately from the args
// append.
//
// base is the number of bind arguments that precede this builder's own args in
// the final query. Pass 0 for a self-contained WHERE filter (first fragment
// binds $1); pass 1 when the caller prepends an id as $1 ahead of a SET list
// (first fragment binds $2).
type ClauseBuilder struct {
	base    int
	clauses []string
	args    []any
}

// NewClauseBuilder returns a ClauseBuilder whose first bound argument is
// $(base+1). See ClauseBuilder for the meaning of base.
func NewClauseBuilder(base int) *ClauseBuilder {
	return &ClauseBuilder{base: base}
}

// Add binds val and appends a fragment. format must contain exactly one %d
// verb, which receives val's placeholder index (e.g. "operator_id = $%d" or
// "name = $%d").
func (b *ClauseBuilder) Add(format string, val any) {
	b.args = append(b.args, val)
	b.clauses = append(b.clauses, fmt.Sprintf(format, b.base+len(b.args)))
}

// AddRaw appends a literal fragment that binds no argument, such as
// "col = NULL" or "col IS NOT NULL". It must contain no $N placeholder.
func (b *ClauseBuilder) AddRaw(clause string) {
	b.clauses = append(b.clauses, clause)
}

// Bind binds val without emitting a clause and returns its placeholder index.
// Use for placeholders that are not part of the joined clause list, such as a
// trailing LIMIT/OFFSET appended to the query by hand.
func (b *ClauseBuilder) Bind(val any) int {
	b.args = append(b.args, val)
	return b.base + len(b.args)
}

// Clauses returns the accumulated fragments in the order they were added.
func (b *ClauseBuilder) Clauses() []string { return b.clauses }

// Args returns the accumulated bind arguments in placeholder order. Prepend
// any base args (e.g. a leading id) before passing to the query.
func (b *ClauseBuilder) Args() []any { return b.args }

// Len reports how many clauses have been added (excludes Bind-only args).
func (b *ClauseBuilder) Len() int { return len(b.clauses) }

// limitOrReturningRE matches a LIMIT or RETURNING keyword as a standalone
// SQL token (whitespace/punctuation/string boundaries on both sides) so
// occurrences inside identifiers, column aliases, or string literals do
// NOT suppress the appended LIMIT 1. (?i) makes the match case-insensitive.
var limitOrReturningRE = regexp.MustCompile(`(?i)(^|[\s,)(;'"` + "`" + `])(LIMIT|RETURNING)([\s,)(;'"` + "`" + `]|$)`)

// withLimitOne appends " LIMIT 1" to a query unless it already contains a
// LIMIT or RETURNING clause as a standalone token. Identifier-like usages
// (e.g. `SELECT … AS limit_value` or string literals containing "LIMIT")
// are NOT treated as SQL keywords here.
//
// Known edge case: a subquery LIMIT (e.g. `WHERE id IN (SELECT id FROM bar
// LIMIT 5)`) suppresses the outer LIMIT 1. This is not a correctness bug —
// pgx.CollectOneRow stops reading after the first row and closes the
// iterator, so postgres halts streaming — but it does forfeit the explicit
// "LIMIT 1" hint that would let the planner short-circuit the outer scan.
// QueryOne callers that need the optimization on such queries should write
// the outer LIMIT 1 themselves.
func withLimitOne(sql string) string {
	if limitOrReturningRE.MatchString(sql) {
		return sql
	}
	return sql + " LIMIT 1"
}
