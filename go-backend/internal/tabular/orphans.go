package tabular

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/logctx"
)

// orphanFileIDRe validates the 32-hex-char file id embedded in a materialized
// table name (sheet_<32 hex>_<sheetIdx>_<regionIdx>[__stage]).
var orphanFileIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// OrphanSweeper periodically drops materialized tabular tables (and their
// tabular_column_values rows) whose owning `files` row no longer exists
// (R65). A table whose file exists but whose tabular_catalog row is missing
// is left alone — the next ingest recreates it.
type OrphanSweeper struct {
	pool *pgxpool.Pool
}

// NewOrphanSweeper constructs an OrphanSweeper against the main Postgres pool
// (the `tabular` schema and `files` table both live there).
func NewOrphanSweeper(pool *pgxpool.Pool) *OrphanSweeper {
	return &OrphanSweeper{pool: pool}
}

// Sweep lists every tabular.sheet_% table (including __stage siblings),
// parses the embedded 32-hex file id, and drops the table plus its
// tabular_column_values rows when no `files` row with that id exists. Stops
// after dropping `limit` tables in this run (a future call resumes where
// this one left off, since the next listing simply won't include what was
// already dropped). Never touches a table whose name doesn't match the
// sheet_<32 hex>_<sheetIdx>_<regionIdx>[__stage] shape.
func (s *OrphanSweeper) Sweep(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}

	const listSQL = `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'tabular'
		  AND table_name LIKE 'sheet\_%' ESCAPE '\'
		ORDER BY table_name`

	rows, err := s.pool.Query(ctx, listSQL)
	if err != nil {
		return nil, fmt.Errorf("tabular: list orphan candidates: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("tabular: scan orphan candidate: %w", err)
		}
		names = append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tabular: iterate orphan candidates: %w", err)
	}

	var dropped []string
	for _, name := range names {
		if len(dropped) >= limit {
			break
		}
		fileHex, ok := extractOrphanFileHex(name)
		if !ok {
			continue
		}

		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM files WHERE replace(id::text,'-','') = $1)`,
			fileHex).Scan(&exists); err != nil {
			logctx.From(ctx).Warn("tabular.orphan_cleanup: file existence check failed", "table", name, "error", err)
			continue
		}
		if exists {
			continue
		}

		if _, err := s.pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s.%s`, q(TabularSchema), q(name))); err != nil {
			logctx.From(ctx).Warn("tabular.orphan_cleanup: drop table failed", "table", name, "error", err)
			continue
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM tabular_column_values WHERE table_name = $1`, name); err != nil {
			logctx.From(ctx).Warn("tabular.orphan_cleanup: delete column values failed", "table", name, "error", err)
		}
		dropped = append(dropped, name)
	}

	logctx.From(ctx).Info("tabular.orphan_cleanup", "dropped", len(dropped), "candidates", len(names))
	return dropped, nil
}

// extractOrphanFileHex pulls the 32-hex file id out of a
// sheet_<hex>_<sheetIdx>_<regionIdx>[__stage] table name, validating both
// the hex shape and that the name has the expected "sheet_" prefix length.
func extractOrphanFileHex(name string) (string, bool) {
	const prefix = "sheet_"
	if len(name) < len(prefix)+32 {
		return "", false
	}
	if name[:len(prefix)] != prefix {
		return "", false
	}
	hex := name[len(prefix) : len(prefix)+32]
	if !orphanFileIDRe.MatchString(hex) {
		return "", false
	}
	// Remainder must be "_<digits>_<digits>" or "_<digits>_<digits>__stage".
	rest := name[len(prefix)+32:]
	if !orphanTableSuffixRe.MatchString(rest) {
		return "", false
	}
	return hex, true
}

var orphanTableSuffixRe = regexp.MustCompile(`^_\d+_\d+(__stage)?$`)
