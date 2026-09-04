package tabular

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CatalogEntry is one materialized sheet (Phase-1) / materialized region
// (Phase-2) as recorded in tabular_catalog.
//
// SheetIndex/RegionIndex/SheetKind/Hidden/HeaderRow/Profile/ColumnStats are
// the Phase-2 (v2, migration 0069) columns: the profiler's view of the
// region. HeaderRow of -1 means "no header row detected" and round-trips to
// SQL NULL. A Phase-1 row (written before 0069, or by a writer that never
// sets these) reads back with SheetKind defaulting to "table" (the column
// default), Hidden false, HeaderRow -1, and Profile/ColumnStats nil.
type CatalogEntry struct {
	FileID    string
	KBID      string
	SheetName string
	TableName string
	FileName  string
	Columns   []ColumnSpec
	RowCount  int64

	SheetIndex  int
	RegionIndex int
	SheetKind   string
	Hidden      bool
	HeaderRow   int // -1 = no header row detected
	Profile     json.RawMessage
	ColumnStats []ColumnStat
}

// Catalog persists and reads tabular_catalog rows. Backed by the main R/W pool
// on the write path; the table_query tool reads it through the read-only pool.
type Catalog struct{ pool *pgxpool.Pool }

func NewCatalog(pool *pgxpool.Pool) *Catalog { return &Catalog{pool: pool} }

const insertCatalogSQL = `INSERT INTO tabular_catalog
  (kb_id, file_id, sheet_name, table_name, columns, row_count, sheet_index, region_index, sheet_kind, hidden, header_row, profile, column_stats, coercion_stats)
  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`

// Insert records one materialized sheet/region.
func (c *Catalog) Insert(ctx context.Context, e CatalogEntry) error {
	colsJSON, err := json.Marshal(e.Columns)
	if err != nil {
		return err
	}

	var columnStatsJSON []byte
	if e.ColumnStats != nil {
		columnStatsJSON, err = json.Marshal(e.ColumnStats)
		if err != nil {
			return err
		}
	}

	var profile any
	if e.Profile != nil {
		profile = []byte(e.Profile)
	}

	var headerRow any
	if e.HeaderRow >= 0 {
		headerRow = e.HeaderRow
	}

	sheetKind := e.SheetKind
	if sheetKind == "" {
		sheetKind = "table"
	}

	var coercionFailed int64
	for _, s := range e.ColumnStats {
		coercionFailed += s.CoercionFailed
	}
	coercionStatsJSON, err := json.Marshal(map[string]int64{"failed": coercionFailed})
	if err != nil {
		return err
	}

	_, err = c.pool.Exec(ctx, insertCatalogSQL,
		e.KBID, e.FileID, e.SheetName, e.TableName, colsJSON, e.RowCount,
		e.SheetIndex, e.RegionIndex, sheetKind, e.Hidden, headerRow,
		profile, columnStatsJSON, coercionStatsJSON)
	return err
}

const selectCatalogCols = `
	SELECT tc.file_id::text, tc.sheet_name, tc.table_name, tc.columns, tc.row_count,
	       COALESCE(f.name, ''), tc.sheet_index, tc.region_index, tc.sheet_kind, tc.hidden,
	       COALESCE(tc.header_row, -1), tc.profile, tc.column_stats
	FROM tabular_catalog tc
	LEFT JOIN files f ON f.id = tc.file_id`

// scanCatalogRows reads the shared ListByKB/ListByFile column set.
func scanCatalogRows(rows pgx.Rows) ([]CatalogEntry, error) {
	defer rows.Close()
	var out []CatalogEntry
	for rows.Next() {
		var e CatalogEntry
		var colsJSON, profileJSON, columnStatsJSON []byte
		if err := rows.Scan(&e.FileID, &e.SheetName, &e.TableName, &colsJSON, &e.RowCount,
			&e.FileName, &e.SheetIndex, &e.RegionIndex, &e.SheetKind, &e.Hidden,
			&e.HeaderRow, &profileJSON, &columnStatsJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(colsJSON, &e.Columns); err != nil {
			return nil, err
		}
		if profileJSON != nil {
			e.Profile = json.RawMessage(profileJSON)
		}
		if columnStatsJSON != nil {
			if err := json.Unmarshal(columnStatsJSON, &e.ColumnStats); err != nil {
				return nil, err
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListByKB returns the catalog entries (with file name joined) for one KB.
// Used by the tool's discovery mode and to build the per-request allowlist.
func (c *Catalog) ListByKB(ctx context.Context, kbID string) ([]CatalogEntry, error) {
	rows, err := c.pool.Query(ctx, selectCatalogCols+`
	WHERE tc.kb_id = $1
	ORDER BY tc.created_at, tc.sheet_index, tc.region_index`, kbID)
	if err != nil {
		return nil, err
	}
	out, err := scanCatalogRows(rows)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].KBID = kbID
	}
	return out, nil
}

// ListByFile returns the catalog entries (with file name joined) for one file.
func (c *Catalog) ListByFile(ctx context.Context, fileID string) ([]CatalogEntry, error) {
	rows, err := c.pool.Query(ctx, selectCatalogCols+`
	WHERE tc.file_id = $1
	ORDER BY tc.created_at, tc.sheet_index, tc.region_index`, fileID)
	if err != nil {
		return nil, err
	}
	return scanCatalogRows(rows)
}

// TableNamesByFile returns the physical table names a file owns (for drop on
// delete / re-ingest). Reads through the main pool.
func (c *Catalog) TableNamesByFile(ctx context.Context, fileID string) ([]string, error) {
	rows, err := c.pool.Query(ctx, `SELECT table_name FROM tabular_catalog WHERE file_id = $1`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteByFile removes catalog rows for a file. Physical tables are dropped
// separately (DDL is not transactional with the catalog delete).
func (c *Catalog) DeleteByFile(ctx context.Context, fileID string) error {
	_, err := c.pool.Exec(ctx, `DELETE FROM tabular_catalog WHERE file_id = $1`, fileID)
	return err
}

// HasDataForKB reports whether the KB has any materialized tabular tables
// (sheet_kind = 'table' — excludes non-table regions such as notes/pivots).
// Cheap indexed EXISTS; used by the Phase-3 chart-guidance gate to avoid
// injecting the snippet on pure-document turns.
func (c *Catalog) HasDataForKB(ctx context.Context, kbID string) (bool, error) {
	var exists bool
	err := c.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM tabular_catalog WHERE kb_id = $1 AND sheet_kind = 'table')`, kbID).Scan(&exists)
	return exists, err
}

// ReplaceColumnValues atomically replaces the distinct-value rows for one
// materialized column: deletes the existing set for (tableName, columnName)
// and bulk-loads the new one in the same transaction, so a concurrent reader
// never sees a stale mix or an empty gap.
func (c *Catalog) ReplaceColumnValues(ctx context.Context, tableName, columnName string, values map[string]int64) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	if _, err := tx.Exec(ctx, `DELETE FROM tabular_column_values WHERE table_name = $1 AND column_name = $2`,
		tableName, columnName); err != nil {
		return err
	}

	if len(values) > 0 {
		rows := make([][]any, 0, len(values))
		for v, count := range values {
			rows = append(rows, []any{tableName, columnName, v, count})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"tabular_column_values"},
			[]string{"table_name", "column_name", "value", "row_count"}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// DeleteValuesForTables removes every tabular_column_values row for the given
// physical table names (drop on delete / re-ingest, alongside DeleteByFile).
func (c *Catalog) DeleteValuesForTables(ctx context.Context, tables []string) error {
	_, err := c.pool.Exec(ctx, `DELETE FROM tabular_column_values WHERE table_name = ANY($1)`, tables)
	return err
}
