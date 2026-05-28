package tabular

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/logctx"
)

// Materializer loads a spreadsheet's sheets into native-typed tables in the
// `tabular` schema and records them in tabular_catalog. It owns the main R/W
// pool (CREATE TABLE + COPY) and is idempotent per file: any pre-existing
// tables/catalog rows for the file are dropped before re-materializing.
type Materializer struct {
	pool    *pgxpool.Pool
	catalog *Catalog
}

func NewMaterializer(pool *pgxpool.Pool) *Materializer {
	return &Materializer{pool: pool, catalog: NewCatalog(pool)}
}

// Result reports what was materialized so the caller can build summary cards.
type Result struct {
	Sheets []SheetResult
}

type SheetResult struct {
	SheetName string
	TableName string
	Columns   []ColumnSpec
	RowCount  int64
	RowChunks []RowChunk // Phase 2: embeddable free-text rows (empty when semantic off)
}

// Materialize reads the file, (re)creates one table per sheet, COPYs rows, and
// records catalog entries. Best-effort per file: a returned error means the
// caller should fall back to normal text ingestion for this file.
//
// On any error after the first sheet is written, partial work is rolled back
// (dropExisting): otherwise the file would have SOME sheets in the tabular
// store while ingestion falls back to text embedding, letting the LLM query an
// incompletely-materialized sheet and compute wrong aggregates.
//
// When opts.Enabled, each sheet table gains a leading "_rowid" bigint column
// (1-based ordinal) and SheetResult.RowChunks is populated with embeddable
// free-text row content built by BuildRowChunkContent.
func (m *Materializer) Materialize(ctx context.Context, filePath, fileName, fileID, kbID string, opts SemanticOptions) (_ *Result, err error) {
	if m == nil || m.pool == nil {
		return nil, fmt.Errorf("tabular: materializer not configured")
	}
	if err := m.dropExisting(ctx, fileID); err != nil {
		return nil, fmt.Errorf("tabular: drop existing: %w", err)
	}
	sheets, err := ReadSheets(filePath, fileName)
	if err != nil {
		return nil, fmt.Errorf("tabular: read: %w", err)
	}
	defer func() {
		if err != nil {
			// Best-effort rollback; the drop error (if any) must not mask the
			// original failure that the caller acts on.
			_ = m.dropExisting(ctx, fileID)
		}
	}()
	res := &Result{}
	for idx, sheet := range sheets {
		cols, data := BuildColumnSpecs(sheet.Rows, opts)
		if len(cols) == 0 {
			continue
		}
		tableName := TableNameForFile(fileID, idx)
		if _, err := m.pool.Exec(ctx, BuildCreateTableSQL(tableName, cols, opts.Enabled)); err != nil {
			return nil, fmt.Errorf("tabular: create %s: %w", tableName, err)
		}
		n, err := m.copyRows(ctx, tableName, cols, data, opts.Enabled)
		if err != nil {
			return nil, fmt.Errorf("tabular: copy %s: %w", tableName, err)
		}
		sr := SheetResult{SheetName: sheet.SheetName, TableName: tableName, Columns: cols, RowCount: n}
		if opts.Enabled {
			for r, row := range data {
				if content, ok := BuildRowChunkContent(tableName, int64(r+1), cols, row); ok {
					sr.RowChunks = append(sr.RowChunks, RowChunk{RowID: int64(r + 1), Text: content})
				}
			}
		}
		if err := m.catalog.Insert(ctx, CatalogEntry{
			FileID: fileID, KBID: kbID, SheetName: sheet.SheetName,
			TableName: tableName, Columns: cols, RowCount: n,
		}); err != nil {
			return nil, fmt.Errorf("tabular: catalog insert %s: %w", tableName, err)
		}
		res.Sheets = append(res.Sheets, sr)
	}
	return res, nil
}

func (m *Materializer) copyRows(ctx context.Context, tableName string, cols []ColumnSpec, data [][]string, withRowID bool) (int64, error) {
	colNames := ColumnNames(cols)
	if withRowID {
		colNames = append([]string{RowIDColumn}, colNames...)
	}
	rowIdx := 0
	rows := pgx.CopyFromFunc(func() ([]any, error) {
		if rowIdx >= len(data) {
			return nil, nil
		}
		row := data[rowIdx]
		rowIdx++
		vals := make([]any, 0, len(colNames))
		if withRowID {
			vals = append(vals, int64(rowIdx)) // 1-based; rowIdx already incremented
		}
		for i, c := range cols {
			raw := ""
			if i < len(row) {
				raw = row[i]
			}
			v, ok := coerceValue(raw, c.Type)
			if !ok {
				v = nil // coercion failure -> NULL (best-effort, never fail the file)
			}
			vals = append(vals, v)
		}
		return vals, nil
	})
	return m.pool.CopyFrom(ctx, pgx.Identifier{TabularSchema, tableName}, colNames, rows)
}

func (m *Materializer) dropExisting(ctx context.Context, fileID string) error {
	names, err := m.catalog.TableNamesByFile(ctx, fileID)
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, err := m.pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s.%q", TabularSchema, n)); err != nil {
			logctx.From(ctx).Warn("tabular: drop existing table failed", "table", n, "error", err)
		}
	}
	return m.catalog.DeleteByFile(ctx, fileID)
}

// DropTablesForFile drops a file's per-sheet tables and catalog rows. Called
// by the cascade deleter on file/KB deletion.
func (m *Materializer) DropTablesForFile(ctx context.Context, fileID string) error {
	return m.dropExisting(ctx, fileID)
}
