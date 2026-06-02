package database

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/justrag/go-backend/internal/logctx"
)

// slowQueryTracer logs any pgx Query/Exec/Batch/CopyFrom call that takes
// longer than threshold. It implements the pgx tracer interfaces selectively:
// only the start/end pairs we care about — Query (for SELECT), Batch (for
// pipelined work), and CopyFrom (for bulk loads). Connect/Prepare are not
// instrumented because they're noise for this purpose.
//
// Output goes through logctx so the request_id / trace_id from the active
// context attaches automatically — that's what makes a slow-query line
// joinable to the rest of the request's log timeline.
//
// Data-governance note: the logged "sql" / "sample_sql" field is pgx's
// parameterized statement template ($1, $2, …), NOT the resolved query.
// pgx uses the extended protocol and carries bound argument values out of
// band (data.SQL never has them interpolated), so no parameter values —
// emails, search terms, PII — are ever written to the slow-query log.
type slowQueryTracer struct {
	threshold time.Duration
}

type ctxKey int

const (
	queryStartKey ctxKey = iota
	batchStartKey
	copyFromStartKey
)

type queryStart struct {
	at  time.Time
	sql string
}

type batchStart struct {
	at      time.Time
	queries int
	// sample is the first queued statement, captured so a slow-batch warning
	// names at least one of the queries that composed it. TraceBatchQuery
	// can't populate this (it returns no context), so we read it from the
	// batch up front in TraceBatchStart.
	sample string
}

type copyFromStart struct {
	at    time.Time
	table string
}

func (t *slowQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryStartKey, queryStart{at: time.Now(), sql: data.SQL})
}

func (t *slowQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	v, ok := ctx.Value(queryStartKey).(queryStart)
	if !ok {
		return
	}
	dur := time.Since(v.at)
	if dur < t.threshold {
		return
	}
	logctx.From(ctx).Warn("db.slow_query",
		"sql", v.sql,
		"duration_ms", dur.Milliseconds(),
		"err", errString(data.Err),
	)
}

func (t *slowQueryTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchStartData) context.Context {
	var sample string
	if qq := data.Batch.QueuedQueries; len(qq) > 0 {
		sample = qq[0].SQL
	}
	return context.WithValue(ctx, batchStartKey, batchStart{at: time.Now(), queries: data.Batch.Len(), sample: sample})
}

// TraceBatchQuery fires once per query inside a batch. We only care about
// the overall batch wallclock, so this is a no-op — TraceBatchEnd carries
// the timing and TraceBatchStart already captured a sample statement.
func (t *slowQueryTracer) TraceBatchQuery(_ context.Context, _ *pgx.Conn, _ pgx.TraceBatchQueryData) {
}

func (t *slowQueryTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	v, ok := ctx.Value(batchStartKey).(batchStart)
	if !ok {
		return
	}
	dur := time.Since(v.at)
	if dur < t.threshold {
		return
	}
	logctx.From(ctx).Warn("db.slow_batch",
		"queries", v.queries,
		"sample_sql", v.sample,
		"duration_ms", dur.Milliseconds(),
		"err", errString(data.Err),
	)
}

func (t *slowQueryTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	return context.WithValue(ctx, copyFromStartKey, copyFromStart{
		at:    time.Now(),
		table: data.TableName.Sanitize(),
	})
}

func (t *slowQueryTracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	v, ok := ctx.Value(copyFromStartKey).(copyFromStart)
	if !ok {
		return
	}
	dur := time.Since(v.at)
	if dur < t.threshold {
		return
	}
	logctx.From(ctx).Warn("db.slow_copy_from",
		"table", v.table,
		"rows", data.CommandTag.RowsAffected(),
		"duration_ms", dur.Milliseconds(),
		"err", errString(data.Err),
	)
}

// Compile-time assertions that we satisfy the tracer interfaces we claim.
var (
	_ pgx.QueryTracer    = (*slowQueryTracer)(nil)
	_ pgx.BatchTracer    = (*slowQueryTracer)(nil)
	_ pgx.CopyFromTracer = (*slowQueryTracer)(nil)
)

// errString returns "" for nil so the slog field stays empty/clean in the
// success case rather than rendering "<nil>".
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
