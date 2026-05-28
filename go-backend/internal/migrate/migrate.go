// Package migrate provides database migration support using goose.
// It handles both main DB and vector DB migrations, includes a
// bootstrap step for transitioning from Drizzle-managed schemas,
// and can seed the admin user.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/database"
)

// RunMain applies pending main-database migrations using goose.
// It first bootstraps from Drizzle if needed, then runs goose.Up.
func RunMain(ctx context.Context, cfg config.DBConfig, migrations fs.FS) error {
	db, err := openSQL(ctx, cfg)
	if err != nil {
		return fmt.Errorf("migrate main: open db: %w", err)
	}
	defer db.Close()

	if err := bootstrapFromDrizzle(ctx, db); err != nil {
		return fmt.Errorf("migrate main: bootstrap: %w", err)
	}
	didFresh, err := bootstrapFreshDatabase(ctx, db, migrations)
	if err != nil {
		return fmt.Errorf("migrate main: fresh bootstrap: %w", err)
	}
	if err := ensureGooseVersionZero(ctx, db); err != nil {
		return fmt.Errorf("migrate main: ensure version zero: %w", err)
	}
	if !didFresh {
		// Reconcile would see app tables + only-version-0 tracking after a
		// fresh bootstrap and wrongly seed every later version as applied.
		if err := reconcileExistingSchema(ctx, db, migrations); err != nil {
			return fmt.Errorf("migrate main: reconcile schema: %w", err)
		}
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations)
	if err != nil {
		return fmt.Errorf("migrate main: create provider: %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate main: up: %w", err)
	}

	for _, r := range results {
		slog.Info("applied migration",
			"db", "main",
			"version", r.Source.Version,
			"name", r.Source.Path,
			"duration_ms", r.Duration.Milliseconds(),
		)
	}
	if len(results) == 0 {
		slog.Info("main database is up to date")
	}

	return nil
}

// IsSameDatabase returns true when two DB configs point to the same physical
// PostgreSQL database — the host:port/dbname triple. This is intentionally
// independent of credentials: a database is the same database regardless of
// which user connects to it, so DROP TABLE statements issued through either
// config target the same schema. The migration-skip path in cmd/migrate
// relies on that semantics — running vector migrations against a co-located
// main schema would destroy main-schema tables, and changing credentials
// does not protect against that.
//
// Comparison is purely textual: it does NOT resolve DNS and does NOT compare
// underlying server identities. `localhost` vs `127.0.0.1` (or two CNAMEs
// resolving to the same host) compare unequal even when they target the same
// PostgreSQL instance. Operators MUST keep host strings consistent across the
// DB_HOST and VECTOR_DB_HOST env vars for the equivalence to be detected.
//
// This is intentionally a STRICTER check than the pool-reuse decision in
// database.Connect, which additionally compares credentials before sharing
// a pool (different roles → separate pools, same physical DB → safe to skip
// vector migrations).
func IsSameDatabase(a, b config.DBConfig) bool {
	return a.Host == b.Host && a.Port == b.Port && a.Name == b.Name
}

// RunVector applies pending vector-database migrations using goose.
// The caller MUST verify that the vector DB is separate from the main DB
// before calling this — several vector migrations drop tables that exist
// in the main schema (0001_cleanup_unused_tables.sql).
func RunVector(ctx context.Context, cfg config.DBConfig, migrations fs.FS) error {
	db, err := openSQL(ctx, cfg)
	if err != nil {
		return fmt.Errorf("migrate vector: open db: %w", err)
	}
	defer db.Close()

	if err := bootstrapFromDrizzle(ctx, db); err != nil {
		return fmt.Errorf("migrate vector: bootstrap: %w", err)
	}
	didFresh, err := bootstrapFreshDatabase(ctx, db, migrations)
	if err != nil {
		return fmt.Errorf("migrate vector: fresh bootstrap: %w", err)
	}
	if err := ensureGooseVersionZero(ctx, db); err != nil {
		return fmt.Errorf("migrate vector: ensure version zero: %w", err)
	}
	if !didFresh {
		if err := reconcileExistingSchema(ctx, db, migrations); err != nil {
			return fmt.Errorf("migrate vector: reconcile schema: %w", err)
		}
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations)
	if err != nil {
		return fmt.Errorf("migrate vector: create provider: %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate vector: up: %w", err)
	}

	for _, r := range results {
		slog.Info("applied migration",
			"db", "vector",
			"version", r.Source.Version,
			"name", r.Source.Path,
			"duration_ms", r.Duration.Milliseconds(),
		)
	}
	if len(results) == 0 {
		slog.Info("vector database is up to date")
	}

	return nil
}

// Status prints the current migration status.
func Status(ctx context.Context, cfg config.DBConfig, migrations fs.FS, label string) error {
	db, err := openSQL(ctx, cfg)
	if err != nil {
		return fmt.Errorf("migrate status: open db: %w", err)
	}
	defer db.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations)
	if err != nil {
		return fmt.Errorf("migrate status: create provider: %w", err)
	}

	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("migrate status: get version: %w", err)
	}

	slog.Info("migration status", "db", label, "version", version)
	return nil
}

// openSQL opens a *sql.DB via the pgx stdlib adapter with the configured
// connection timeout applied. PingContext verifies connectivity up front so
// failures surface here with a clear "ping" error rather than appearing
// deep inside goose's first internal query.
//
// Migrations override the request-path StatementTimeout (default 120s) to 0
// (no limit). A non-concurrent index build, a backfill UPDATE, or a large
// table rewrite can run far longer than 120s, and hitting that wall would
// leave the schema in a partially-applied state. Migrations are an admin
// operation expected to take as long as they take; HTTP-request guarantees
// don't apply.
func openSQL(ctx context.Context, cfg config.DBConfig) (*sql.DB, error) {
	migrationCfg := cfg
	migrationCfg.StatementTimeout = 0
	connStr := database.BuildConnString(migrationCfg)
	connConfig, err := pgx.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("parse connection config: %w", err)
	}
	db := stdlib.OpenDB(*connConfig)
	// Cap the pool so migrations don't open unbounded connections; PingContext
	// below plus the dialer ConnectTimeout handle fail-fast on unreachable DBs.
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	// Unlimited connection lifetime/idle time, overriding the request-path pool
	// config. A single migration (non-concurrent index build, backfill UPDATE,
	// table rewrite) can hold one connection far longer than the app's
	// MaxConnLifetime; recycling it mid-DDL would leave the schema partially
	// applied. Same rationale as the StatementTimeout=0 override above. Safe
	// because the *sql.DB is short-lived — every caller defers db.Close().
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return db, nil
}
