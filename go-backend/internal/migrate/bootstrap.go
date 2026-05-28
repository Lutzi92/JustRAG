package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// ensureGooseVersionZero checks whether the goose_db_version table exists and,
// if so, ensures it contains the version 0 seed row that goose v3 requires.
//
// This handles the edge case where a previous migration run created the table
// but failed before committing the version 0 row (e.g. container killed
// mid-migration, or the table was manually created without the seed). Without
// version 0, goose.Provider.Up returns "missing zero version migration".
//
// On a fresh database (no goose table yet), this is a no-op — goose will
// create the table and seed version 0 itself.
func ensureGooseVersionZero(ctx context.Context, db *sql.DB) error {
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'goose_db_version'
		)`).Scan(&exists)
	if err != nil || !exists {
		return nil // Table does not exist yet — goose will handle creation.
	}

	// Table exists. Ensure version 0 is present.
	_, err = db.ExecContext(ctx, `
		INSERT INTO goose_db_version (version_id, is_applied, tstamp)
		SELECT 0, true, now()
		WHERE NOT EXISTS (
			SELECT 1 FROM goose_db_version WHERE version_id = 0
		)`)
	if err != nil {
		return fmt.Errorf("insert goose version 0: %w", err)
	}
	return nil
}

// reconcileExistingSchema is a one-time repair for databases whose schema was
// fully applied by Drizzle (or a prior deployment) but whose
// __drizzle_migrations table no longer exists. Without it bootstrapFromDrizzle
// is a no-op, goose has no version tracking, and provider.Up re-applies SQL
// that already exists in the schema.
//
// Trigger condition (ALL must be true):
//  1. goose_db_version either does not exist or has NO entries with version > 0.
//  2. The public schema contains application tables (evidence of prior migration).
//
// When triggered, goose_db_version is created (if needed) and all migration
// file versions are seeded as applied.
//
// Once goose has actively tracked at least one migration (version > 0), this
// function is permanently a no-op — any gap between tracked versions and
// migration files is a legitimate pending migration for goose to apply.
func reconcileExistingSchema(ctx context.Context, db *sql.DB, migrations fs.FS) error {
	// Count migration SQL files.
	files, err := fs.Glob(migrations, "*.sql")
	if err != nil || len(files) == 0 {
		return nil
	}

	// If goose has actively tracked any migration (version > 0), this database
	// is goose-managed. Any untracked file versions are legitimate pending
	// migrations — do NOT seed them.
	var gooseTableExists bool
	_ = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'goose_db_version'
		)`).Scan(&gooseTableExists)

	if gooseTableExists {
		var trackedAboveZero int
		err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM goose_db_version WHERE version_id > 0`).Scan(&trackedAboveZero)
		if err == nil && trackedAboveZero > 0 {
			return nil // Goose-managed database — let goose handle pending migrations.
		}
	}

	// No goose tracking beyond the base version 0. Check if the database has
	// application tables (evidence of a prior migration run not tracked by goose).
	var hasAppTables bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_type = 'BASE TABLE'
			  AND table_name NOT IN ('goose_db_version', '__drizzle_migrations')
		)`).Scan(&hasAppTables)
	if err != nil || !hasAppTables {
		return nil // Fresh database — goose will handle from scratch.
	}

	slog.Info("reconciling existing schema with goose — schema exists but goose has no version tracking",
		"migration_files", len(files),
	)

	// The CREATE + per-version INSERTs run inside one transaction so a
	// mid-loop crash either leaves goose tracking untouched or fully
	// reconciled. Without this, a partial seed would still satisfy the
	// "trackedAboveZero > 0" early-return on the next run and goose would
	// then try to re-apply already-existing migrations.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin reconcile tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Ensure goose_db_version table exists.
	if _, err = tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS goose_db_version (
			id         serial    NOT NULL,
			version_id bigint    NOT NULL,
			is_applied boolean   NOT NULL,
			tstamp     timestamp NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create goose table: %w", err)
	}

	// Seed versions 0 through len(files)-1. File 0000 = version 0, 0001 = version 1, etc.
	for v := 0; v < len(files); v++ {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO goose_db_version (version_id, is_applied, tstamp)
			SELECT $1, true, now()
			WHERE NOT EXISTS (
				SELECT 1 FROM goose_db_version WHERE version_id = $1
			)`, v); err != nil {
			return fmt.Errorf("seed goose version %d: %w", v, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reconcile: %w", err)
	}

	slog.Info("schema reconciliation complete", "versions_seeded", len(files))
	return nil
}

// bootstrapFreshDatabase handles the fresh-deployment edge case that goose v3
// cannot handle on its own: file 0000_*.sql is parsed as version 0, and goose
// reserves version 0 as the "initial state" sentinel — it silently skips that
// file and starts applying at version 1. On Drizzle-migrated or
// previously-deployed DBs, [bootstrapFromDrizzle] / [reconcileExistingSchema]
// hide the bug by seeding the goose tracking table so 0001+ proceeds against
// the already-existing schema. On a truly fresh DB none of those fire, so the
// CREATE TABLE statements in 0000 never run and 0001 fails with
// "relation ai_models does not exist".
//
// When triggered, this executes the Up block of 0000_*.sql directly and seeds
// goose_db_version with version 0 marked applied, leaving the database in the
// state goose expects for the subsequent Up call.
//
// Trigger condition (ALL must be true):
//  1. goose_db_version is absent OR contains only the version-0 sentinel
//     (the latter happens when a prior run got past goose's own table
//     initialisation but failed to apply any actual migration file).
//  2. __drizzle_migrations is absent OR has no applied rows.
//  3. No application tables exist in the public schema.
//
// Returns true when the fresh-bootstrap path ran. Callers MUST skip
// [reconcileExistingSchema] in that case — after this function, app tables
// exist but only version 0 is tracked, which would otherwise cause reconcile
// to mark every later version as applied without ever running it.
func bootstrapFreshDatabase(ctx context.Context, db *sql.DB, migrations fs.FS) (bool, error) {
	var gooseExists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = 'goose_db_version'
		)`).Scan(&gooseExists); err != nil {
		return false, fmt.Errorf("check goose table: %w", err)
	}
	if gooseExists {
		var trackedAboveZero int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM goose_db_version WHERE version_id > 0`).Scan(&trackedAboveZero); err != nil {
			return false, fmt.Errorf("count goose rows: %w", err)
		}
		if trackedAboveZero > 0 {
			return false, nil // Goose has applied at least one real migration.
		}
		// Only the version-0 sentinel — DB is still effectively fresh.
	}

	var drizzleExists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = '__drizzle_migrations'
		)`).Scan(&drizzleExists); err != nil {
		return false, fmt.Errorf("check drizzle table: %w", err)
	}
	if drizzleExists {
		var drizzleCount int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM public.__drizzle_migrations`).Scan(&drizzleCount); err != nil {
			return false, fmt.Errorf("count drizzle rows: %w", err)
		}
		if drizzleCount > 0 {
			return false, nil // Drizzle bootstrap already ran (or will run).
		}
	}

	var hasAppTables bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_type = 'BASE TABLE'
			  AND table_name NOT IN ('goose_db_version', '__drizzle_migrations')
		)`).Scan(&hasAppTables); err != nil {
		return false, fmt.Errorf("check app tables: %w", err)
	}
	if hasAppTables {
		return false, nil
	}

	files, err := fs.Glob(migrations, "0000_*.sql")
	if err != nil {
		return false, fmt.Errorf("glob 0000 migration: %w", err)
	}
	if len(files) == 0 {
		return false, nil
	}
	if len(files) > 1 {
		return false, fmt.Errorf("multiple 0000_*.sql files found: %v", files)
	}

	raw, err := fs.ReadFile(migrations, files[0])
	if err != nil {
		return false, fmt.Errorf("read %s: %w", files[0], err)
	}
	upSQL := strings.TrimSpace(extractGooseUp(string(raw)))
	if upSQL == "" {
		return false, fmt.Errorf("no Up section in %s", files[0])
	}

	slog.Info("fresh database detected — applying version 0 schema directly",
		"file", files[0],
	)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Vector-side 0000 references vector(N) without enabling the extension —
	// Drizzle relied on it being created out-of-band. Pre-enable when the file
	// uses the type so this path works on a truly fresh pgvector DB.
	if strings.Contains(upSQL, "vector(") {
		if _, err := tx.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
			return false, fmt.Errorf("enable vector extension: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, upSQL); err != nil {
		return false, fmt.Errorf("apply %s: %w", files[0], err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS goose_db_version (
			id         serial    NOT NULL,
			version_id bigint    NOT NULL,
			is_applied boolean   NOT NULL,
			tstamp     timestamp NOT NULL DEFAULT now()
		)`); err != nil {
		return false, fmt.Errorf("create goose table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO goose_db_version (version_id, is_applied, tstamp)
		SELECT 0, true, now()
		WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 0)`); err != nil {
		return false, fmt.Errorf("seed goose version 0: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit fresh bootstrap: %w", err)
	}

	slog.Info("fresh database bootstrap complete", "file", files[0])
	return true, nil
}

// extractGooseUp returns the SQL between the `-- +goose Up` marker and the
// `-- +goose Down` marker (or end of file when no Down section exists).
// `-- +goose StatementBegin` / `StatementEnd` markers stay in the returned
// SQL — they are plain SQL line comments to PostgreSQL, and the DO $$ ... END $$
// blocks they wrap are a single statement that the simple query protocol
// handles natively.
func extractGooseUp(s string) string {
	const upMarker = "-- +goose Up"
	const downMarker = "-- +goose Down"

	start := strings.Index(s, upMarker)
	if start < 0 {
		return s
	}
	body := s[start+len(upMarker):]
	if i := strings.Index(body, downMarker); i >= 0 {
		body = body[:i]
	}
	return body
}

// bootstrapFromDrizzle handles the one-time transition from Drizzle-managed
// migrations to goose. It checks for the __drizzle_migrations table, counts
// applied migrations, and pre-populates the goose_db_version table so goose
// skips already-applied versions.
//
// This is idempotent: if goose_db_version already has rows, it is a no-op.
// On a fresh database (no Drizzle history), it is also a no-op.
//
// The version-seed INSERTs run inside a single transaction so a mid-loop
// crash leaves goose_db_version either untouched or fully seeded — never
// partially populated. A partial seed would silently skip the bootstrap on
// the next run (gooseCount > 0) and goose would then re-apply migrations
// whose schema already exists.
func bootstrapFromDrizzle(ctx context.Context, db *sql.DB) error {
	// Count Drizzle migrations applied. If the table does not exist
	// (fresh database or already cleaned up), there is nothing to bootstrap.
	var drizzleCount int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM public.__drizzle_migrations`).Scan(&drizzleCount)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return nil // Table does not exist — fresh database.
		}
		return fmt.Errorf("count drizzle migrations: %w", err)
	}
	if drizzleCount == 0 {
		return nil
	}

	// Ensure goose's version table exists.
	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS goose_db_version (
			id         serial    NOT NULL,
			version_id bigint    NOT NULL,
			is_applied boolean   NOT NULL,
			tstamp     timestamp NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("create goose table: %w", err)
	}

	// If goose already has rows (besides the initial version 0 row goose creates),
	// the bootstrap was already done.
	var gooseCount int
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM goose_db_version WHERE version_id > 0`).Scan(&gooseCount)
	if err != nil {
		return fmt.Errorf("count goose rows: %w", err)
	}
	if gooseCount > 0 {
		return nil // Already bootstrapped.
	}

	slog.Info("bootstrapping goose from drizzle",
		"drizzle_migrations", drizzleCount,
	)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin drizzle bootstrap tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Goose needs an initial version 0 row.
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO goose_db_version (version_id, is_applied)
		 SELECT 0, true
		 WHERE NOT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 0)`); err != nil {
		return fmt.Errorf("insert goose version 0: %w", err)
	}

	// Drizzle files are numbered 0000-NNNN. Insert a goose row for each
	// already-applied version. Drizzle idx 0 corresponds to file 0000 = goose version 0
	// (already inserted above), so we insert versions 1 through drizzleCount-1.
	for v := 1; v < drizzleCount; v++ {
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, v); err != nil {
			return fmt.Errorf("insert goose version %d: %w", v, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit drizzle bootstrap: %w", err)
	}

	slog.Info("bootstrap complete", "versions_seeded", drizzleCount)
	return nil
}
