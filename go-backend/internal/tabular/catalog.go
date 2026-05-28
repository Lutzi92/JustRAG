package tabular

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CatalogEntry is one materialized sheet as recorded in tabular_catalog.
type CatalogEntry struct {
	FileID    string
	KBID      string
	SheetName string
	TableName string
	FileName  string
	Columns   []ColumnSpec
	RowCount  int64
}

// TableNameForFile builds the collision-safe physical table name for a sheet:
// sheet_<fileuuid-without-dashes>_<sheetIndex>. Stays within 63 bytes.
func TableNameForFile(fileID string, sheetIdx int) string {
	clean := strings.ReplaceAll(fileID, "-", "")
	return fmt.Sprintf("sheet_%s_%d", clean, sheetIdx)
}

// BuildSummaryCard renders the one-chunk discoverability text that REPLACES
// the spreadsheet's embedded body. It lists the sheet's columns + types and
// row count so semantic search still surfaces the file, and the answer LLM
// knows to reach for table_query.
func BuildSummaryCard(fileName, sheetName, tableName string, cols []ColumnSpec, rowCount int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Structured spreadsheet sheet %q from file %q (%d rows).\n", sheetName, fileName, rowCount)
	fmt.Fprintf(&b, "Queryable via the table_query tool as table %q.%q. Columns:\n", TabularSchema, tableName)
	for _, c := range cols {
		fmt.Fprintf(&b, "- %s (%s) [original header: %s]\n", c.Name, c.Type, c.Original)
	}
	return b.String()
}

// Catalog persists and reads tabular_catalog rows. Backed by the main R/W pool
// on the write path; the table_query tool reads it through the read-only pool.
type Catalog struct{ pool *pgxpool.Pool }

func NewCatalog(pool *pgxpool.Pool) *Catalog { return &Catalog{pool: pool} }

// Insert records one materialized sheet.
func (c *Catalog) Insert(ctx context.Context, e CatalogEntry) error {
	colsJSON, err := json.Marshal(e.Columns)
	if err != nil {
		return err
	}
	_, err = c.pool.Exec(ctx, `
		INSERT INTO tabular_catalog (kb_id, file_id, sheet_name, table_name, columns, row_count)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		e.KBID, e.FileID, e.SheetName, e.TableName, colsJSON, e.RowCount)
	return err
}

// ListByKB returns the catalog entries (with file name joined) for one KB.
// Used by the tool's discovery mode and to build the per-request allowlist.
func (c *Catalog) ListByKB(ctx context.Context, kbID string) ([]CatalogEntry, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT tc.file_id::text, tc.sheet_name, tc.table_name, tc.columns, tc.row_count,
		       COALESCE(f.file_name, '')
		FROM tabular_catalog tc
		LEFT JOIN files f ON f.id = tc.file_id
		WHERE tc.kb_id = $1
		ORDER BY tc.created_at`, kbID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogEntry
	for rows.Next() {
		var e CatalogEntry
		var colsJSON []byte
		if err := rows.Scan(&e.FileID, &e.SheetName, &e.TableName, &colsJSON, &e.RowCount, &e.FileName); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(colsJSON, &e.Columns); err != nil {
			return nil, err
		}
		e.KBID = kbID
		out = append(out, e)
	}
	return out, rows.Err()
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

// HasDataForKB reports whether the KB has any materialized tabular sheets.
// Cheap indexed EXISTS; used by the Phase-3 chart-guidance gate to avoid
// injecting the snippet on pure-document turns.
func (c *Catalog) HasDataForKB(ctx context.Context, kbID string) (bool, error) {
	var exists bool
	err := c.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM tabular_catalog WHERE kb_id = $1)`, kbID).Scan(&exists)
	return exists, err
}
