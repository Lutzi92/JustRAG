package database

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/config"
)

type DB struct {
	Main   *pgxpool.Pool
	Vector *pgxpool.Pool
}

// BuildConnString constructs a PostgreSQL DSN with properly URL-escaped
// credentials. Passwords containing @, :, /, ?, # etc. are handled correctly.
func BuildConnString(cfg config.DBConfig) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Path:   cfg.Name,
		RawQuery: fmt.Sprintf("statement_timeout=%d&connect_timeout=%d",
			int(cfg.StatementTimeout.Milliseconds()),
			max(int(cfg.ConnectionTimeout.Seconds()), 1)),
	}
	return u.String()
}

func BuildPoolConfig(cfg config.DBConfig) (*pgxpool.Config, error) {
	connStr := BuildConnString(cfg)
	poolCfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("parsing pool config: %w", err)
	}

	poolCfg.MaxConns = int32(cfg.MaxConns)
	poolCfg.MinConns = int32(cfg.MinConns)
	poolCfg.MaxConnIdleTime = cfg.IdleTimeout
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	// Spread connection replacements over a 20% window of MaxConnLifetime
	// so a fleet that boots together (k8s rollout, fresh deploy) doesn't
	// recycle every connection in the same instant once the lifetime
	// elapses — a thundering-herd reconnect would briefly spike DB CPU
	// and exhaust the connection-tracking budget.
	poolCfg.MaxConnLifetimeJitter = cfg.MaxConnLifetime / 5
	// 30s default mirrors the previous hardcoded value; configurable via
	// DB_HEALTH_CHECK_PERIOD_MS for deployments behind a PgBouncer / load
	// balancer with a shorter idle timeout. Zero here means cfg.Load was
	// bypassed (test fixtures) — fall back to the same 30s default.
	if cfg.HealthCheckPeriod > 0 {
		poolCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	} else {
		poolCfg.HealthCheckPeriod = 30 * time.Second
	}

	// Apply connection timeout to the dialer so pool acquisition fails fast
	// on bad network/DNS/TCP conditions instead of hanging indefinitely.
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectionTimeout

	// Leave DefaultQueryExecMode at the pgx v5 default (CacheDescribe).
	// Switching to CacheStatement caused a cross-the-board recall regression
	// (~14pp on the golden set, 2026-05-19): pgvector `<=>` and BM25 ts_rank
	// queries depend on per-query custom plans that account for the actual
	// query vector / tsquery, and PostgreSQL's prepared-statement generic
	// plan tends to abandon the HNSW / GIN index path after the custom-plan
	// threshold (~5 executions). CacheDescribe re-plans each call without
	// the named-prepared-statement round-trip, which is the right trade-off
	// for a retrieval workload.

	// Slow-query tracer: any pgx Query/Exec/Batch that exceeds the
	// configured threshold is logged as warn so regressions surface
	// before they show up as latency in production metrics. Disabled
	// when SlowQueryThreshold is zero.
	if cfg.SlowQueryThreshold > 0 {
		poolCfg.ConnConfig.Tracer = &slowQueryTracer{threshold: cfg.SlowQueryThreshold}
	}

	// pgvector >= 0.8: enable iterative HNSW scan on every new connection so
	// filtered ANN queries (kb_id, node_kind, GraphChunkIDs, file_id) keep
	// expanding the graph until the WHERE clause is satisfied. Without it,
	// HNSW's k-NN candidate list is exhausted by the filter before reaching
	// the requested LIMIT, producing silent under-recall on the hot path.
	// The setting is session-scoped (not SET LOCAL), so the cost is one
	// SET per connection at pool warm-up, not per query. Tolerates older
	// pgvector versions and the main DB (which may not load the extension
	// at all) by swallowing the "unrecognized configuration parameter"
	// error — connections stay usable for non-ANN queries.
	poolCfg.AfterConnect = afterConnect
	return poolCfg, nil
}

// afterConnect runs the per-connection setup hooks at pool warm-up: the HNSW
// iterative-scan GUC and the pgvector binary-codec registration. Both are
// fail-open on databases without the pgvector extension (e.g. a split main
// pool), so the same hook is safe on every pool BuildPoolConfig backs.
func afterConnect(ctx context.Context, conn *pgx.Conn) error {
	if err := setHNSWIterativeScan(ctx, conn); err != nil {
		return err
	}
	registerPgvectorTypes(ctx, conn)
	return nil
}

// iterativeScanWarnLogged ensures the "iterative_scan unavailable" warning
// fires once per process lifetime instead of once per new connection — a
// pool churning connections during a deploy would otherwise flood the log.
var iterativeScanWarnLogged atomic.Bool

func setHNSWIterativeScan(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx, "SET hnsw.iterative_scan = 'relaxed_order'"); err != nil {
		if !iterativeScanWarnLogged.Swap(true) {
			slog.Warn("pgvector iterative_scan unavailable; filtered ANN queries may under-recall",
				"error", err,
				"hint", "requires pgvector >= 0.8")
		}
	}
	return nil
}

// defaultConnectAttempts is the number of times Connect retries an initial
// Ping before giving up when cfg.ConnectAttempts is unset. The loop sleeps
// BETWEEN attempts only (no sleep after the final attempt), so five attempts
// with exponential backoff produce four waits of 1, 2, 4, 8s totaling ~15 s —
// enough to ride out a Docker Compose / Kubernetes pod startup race where the
// DB process is up but not yet accepting connections, without making operator
// failure feedback feel sluggish. Raise via DB_CONNECT_ATTEMPTS (cfg.ConnectAttempts)
// for clusters where a post-migration init container delays readiness past 15s;
// size pod init / readiness-probe budgets against the resulting total.
const defaultConnectAttempts = 5

// connectWithRetry opens a pool and pings until either the ping succeeds or
// the attempt budget is exhausted. The returned pool is alive (Ping ok).
// Cancellation via ctx aborts the wait and propagates ctx.Err().
func connectWithRetry(ctx context.Context, label string, cfg config.DBConfig) (*pgxpool.Pool, error) {
	poolCfg, err := BuildPoolConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("%s DB config: %w", label, err)
	}

	attempts := cfg.ConnectAttempts
	if attempts <= 0 {
		attempts = defaultConnectAttempts
	}

	// Open the pool once and retry only Ping. pgxpool.NewWithConfig does not
	// dial eagerly (MinConns are filled lazily in the background and never
	// block or fail the constructor), so the thing actually being waited on is
	// network/DB reachability — which Ping probes. Re-allocating the pool
	// struct and its background goroutines on every attempt was wasted work
	// during the startup race; a failed Ping leaves no usable connection in
	// the pool for pgxpool to hand out, so reuse is safe.
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("opening %s DB pool: %w", label, err)
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if pingErr := pool.Ping(ctx); pingErr == nil {
			return pool, nil
		} else {
			lastErr = pingErr
		}

		if attempt == attempts-1 {
			break
		}
		wait := time.Duration(1<<attempt) * time.Second
		slog.Info("waiting for database",
			"db", label, "host", cfg.Host, "port", cfg.Port,
			"attempt", attempt+1, "retry_in", wait, "error", lastErr)
		// time.NewTimer + Stop() instead of time.After: when ctx cancels
		// mid-backoff (Docker Compose bring-down during a slow DB wait),
		// the timer goroutine is freed immediately rather than living up
		// to 8s. Mirrors completion.go and embedding.go.
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			pool.Close()
			return nil, ctx.Err()
		}
	}
	pool.Close()
	return nil, fmt.Errorf("connecting to %s DB after %d attempts: %w", label, attempts, lastErr)
}

func Connect(ctx context.Context, mainCfg, vectorCfg config.DBConfig) (*DB, error) {
	mainPool, err := connectWithRetry(ctx, "main", mainCfg)
	if err != nil {
		return nil, err
	}
	slog.Info("connected to main database", "host", mainCfg.Host, "port", mainCfg.Port)

	var vectorPool *pgxpool.Pool
	// User + Password are part of the equivalence check so deployments that
	// run pgvector under a separate role (read-only vector user, RLS scoping)
	// don't silently reuse the main pool's credentials. Same host:port/db
	// with different creds means open a second pool.
	if vectorCfg.Host == mainCfg.Host &&
		vectorCfg.Port == mainCfg.Port &&
		vectorCfg.Name == mainCfg.Name &&
		vectorCfg.User == mainCfg.User &&
		vectorCfg.Password == mainCfg.Password {
		vectorPool = mainPool
		slog.Info("vector DB shares main DB connection pool")
	} else {
		vectorPool, err = connectWithRetry(ctx, "vector", vectorCfg)
		if err != nil {
			mainPool.Close()
			return nil, err
		}
		slog.Info("connected to vector database", "host", vectorCfg.Host, "port", vectorCfg.Port)
	}

	return &DB{Main: mainPool, Vector: vectorPool}, nil
}

// ConnectReadOnly opens a small pool from a raw DSN for the sql_query MCP
// tool's dedicated SELECT-only role. It is deliberately separate from
// Connect: the DSN already carries the operator's chosen user/host/db, so
// it bypasses the structured DBConfig path. MaxConns is capped low because
// the tool is a low-frequency meta-query surface, not a hot path. The
// AfterConnect HNSW hook is intentionally omitted — this pool only reads the
// allowlisted metadata tables and never issues ANN queries.
//
// A non-nil error here is non-fatal for the server: the caller falls back to
// a disabled sql_query stub rather than refusing to start.
func ConnectReadOnly(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse read-only DSN: %w", err)
	}
	// Default the pool small (low-frequency meta-query surface), but let an
	// operator override it via `pool_max_conns` in the DSN — ParseConfig has
	// already applied that value, so only clamp to our default when the DSN
	// did not specify one. Without this guard the override is silently
	// discarded.
	if !strings.Contains(dsn, "pool_max_conns") {
		poolCfg.MaxConns = 4
	}
	poolCfg.MinConns = 0
	poolCfg.HealthCheckPeriod = 30 * time.Second
	// Bound connection age explicitly. This pool backs a least-privilege
	// SELECT-only role; unbounded connections can outlive a credential
	// rotation, accumulate after a burst of low-frequency tool calls, or
	// survive a PgBouncer restart with stale session state. Recycling caps
	// each of those at 30 min (or 5 min idle) instead of process lifetime.
	poolCfg.MaxConnLifetime = 30 * time.Minute
	poolCfg.MaxConnIdleTime = 5 * time.Minute
	// Cap query runtime at the Postgres layer. This pool runs LLM-authored
	// SQL (sql_query / table_query); a malformed or adversarial SELECT can
	// trivially trigger a full-table scan or runaway aggregation. The 10s MCP
	// context timeout (mcp/dispatch.go) cancels the Go-side wait, but pgx's
	// cancellation is best-effort — a server-side statement_timeout is the
	// authoritative ceiling that stops the backend from churning after the
	// client has given up. Operators can override by putting their own
	// statement_timeout in the DSN; ParseConfig surfaces that in RuntimeParams,
	// so we only set our 5s default when they did not.
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	if _, ok := poolCfg.ConnConfig.RuntimeParams["statement_timeout"]; !ok {
		poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open read-only pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping read-only pool: %w", err)
	}
	return pool, nil
}

func (db *DB) Close() {
	slog.Info("closing database connections")
	if db.Vector != nil && db.Vector != db.Main {
		db.Vector.Close()
	}
	if db.Main != nil {
		db.Main.Close()
	}
}

func (db *DB) CheckHealth(ctx context.Context) error {
	if err := db.Main.Ping(ctx); err != nil {
		return fmt.Errorf("main db: %w", err)
	}
	if db.Vector != nil && db.Vector != db.Main {
		if err := db.Vector.Ping(ctx); err != nil {
			return fmt.Errorf("vector db: %w", err)
		}
	}
	return nil
}
