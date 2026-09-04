package tabular

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

// Materializer loads a spreadsheet region into a native-typed table in the
// `tabular` schema and records it in tabular_catalog. It owns the main R/W
// pool (CREATE TABLE + COPY) and is idempotent per file: DropTablesForFile
// removes a file's tables/catalog rows/values before re-materializing.
type Materializer struct {
	pool    *pgxpool.Pool
	catalog *Catalog
}

// NewMaterializer's signature is unchanged from Phase 1 — internal/cascade
// depends on it.
func NewMaterializer(pool *pgxpool.Pool) *Materializer {
	return &Materializer{pool: pool, catalog: NewCatalog(pool)}
}

// RegionInput is one KindTable region to materialize.
type RegionInput struct {
	Source                 sheetsource.Source
	SheetIndex             int
	Sheet                  sheetsource.SheetInfo
	Profile                profile.RegionProfile // must be KindTable
	RegionIndex            int
	FileID, KBID, FileName string
	MaxRows                int                // tabular_max_rows; rows beyond are dropped and counted (default 2 000 000)
	MaxDistinct            int                // tabular_column_values_max_distinct
	Progress               func(rowsDone int) // called every 10 000 data rows; may be nil
}

// RegionResult reports what MaterializeRegion did.
type RegionResult struct {
	TableName                                                                     string
	Columns                                                                       []ColumnSpec // primaries + shadows in table order (after _rowid)
	Stats                                                                         []ColumnStat
	RowsRead, RowsMaterialised, RowsDropped, DerivedRowsSkipped, CoercionFailures int64
}

// rowSource adapts a channel of COPY-ready rows to pgx.CopyFromSource.
type rowSource struct {
	ch  <-chan []any
	cur []any
}

func (r *rowSource) Next() bool {
	v, ok := <-r.ch
	if !ok {
		return false
	}
	r.cur = v
	return true
}
func (r *rowSource) Values() ([]any, error) { return r.cur, nil }
func (r *rowSource) Err() error             { return nil }

// MaterializeRegion streams the region ONCE: statistics, value collection
// and a text-typed staging COPY happen in the same pass; the typed table is
// then created server-side from the staging table (spec §4.3 "pass 2" done
// by Postgres). On error the staging + final tables and values are removed
// and the catalog is left untouched.
func (m *Materializer) MaterializeRegion(ctx context.Context, in RegionInput) (res *RegionResult, err error) {
	if in.Profile.Kind != profile.KindTable {
		return nil, fmt.Errorf("tabular: region %d is %s, not a table", in.RegionIndex, in.Profile.Kind)
	}
	if in.MaxRows <= 0 {
		in.MaxRows = 2_000_000
	}
	table := TableNameForRegion(in.FileID, in.SheetIndex, in.RegionIndex)
	stage := stagingName(table)

	accs := NewAccumulators(in.Profile.Columns, StatsOptions{MaxDistinct: in.MaxDistinct})
	names := make([]string, len(accs))
	kept := make([]int, len(accs))
	for i, a := range accs {
		names[i], kept[i] = a.Name, a.Profile.Index
	}

	m.dropTable(ctx, stage)
	m.dropTable(ctx, table)
	if _, err = m.pool.Exec(ctx, BuildStagingTableSQL(table, names)); err != nil {
		return nil, fmt.Errorf("tabular: create staging: %w", err)
	}
	defer func() {
		bg := context.WithoutCancel(ctx)
		m.dropTable(bg, stage)
		if err != nil {
			m.dropTable(bg, table)
			_ = m.catalog.DeleteValuesForTables(bg, []string{table})
		}
	}()

	res = &RegionResult{TableName: table}
	ch := make(chan []any, 256)
	readErr := make(chan error, 1)
	derived := map[int]bool{}
	for _, r := range in.Profile.DerivedRows {
		derived[r] = true
	}

	go func() {
		defer close(ch)
		var ordinal int64
		_, rerr := in.Source.ReadSheet(in.SheetIndex, func(r int, cells []sheetsource.Cell) error {
			if cells == nil || r < in.Profile.DataStart || (!in.Profile.Region.OpenEnded && r > in.Profile.Region.Bottom) {
				return nil
			}
			res.RowsRead++
			if derived[r] || profile.IsDerivedRow(cells, kept, max(int(res.RowsRead), 4)) {
				res.DerivedRowsSkipped++
				return nil
			}
			row := make([]any, 1, len(accs)+1)
			empty := true
			for _, a := range accs {
				var c sheetsource.Cell
				if a.Profile.Index < len(cells) {
					c = cells[a.Profile.Index]
				}
				a.Add(c)
				if v, ok := a.Canonical(c); ok {
					row = append(row, v)
					empty = false
				} else {
					row = append(row, nil)
				}
			}
			if empty {
				return nil
			}
			if ordinal >= int64(in.MaxRows) {
				res.RowsDropped++
				return nil
			}
			ordinal++
			row[0] = ordinal
			select {
			case ch <- row:
			case <-ctx.Done():
				return ctx.Err()
			}
			if in.Progress != nil && ordinal%10_000 == 0 {
				in.Progress(int(ordinal))
			}
			return nil
		})
		readErr <- rerr
	}()

	cols := append([]string{RowIDColumn}, names...)
	n, copyErr := m.pool.CopyFrom(ctx, pgx.Identifier{TabularSchema, stage}, cols, &rowSource{ch: ch})
	for range ch { // drain if COPY failed first, so the reader goroutine can finish
	}
	rerr := <-readErr
	if copyErr != nil {
		err = fmt.Errorf("tabular: copy: %w", copyErr)
		return nil, err
	}
	if rerr != nil {
		err = fmt.Errorf("tabular: read: %w", rerr)
		return nil, err
	}
	res.RowsMaterialised = n

	specs, stats := assembleSpecs(accs, n)

	if _, err = m.pool.Exec(ctx, BuildTypedTableSQL(table, specs)); err != nil {
		return nil, fmt.Errorf("tabular: typed table: %w", err)
	}
	if _, err = m.pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s.%s ADD PRIMARY KEY (%s)`, q(TabularSchema), q(table), q(RowIDColumn))); err != nil {
		return nil, fmt.Errorf("tabular: primary key: %w", err)
	}
	if res.CoercionFailures, err = m.countCoercionFailures(ctx, stage, specs); err != nil {
		return nil, err
	}

	for i, a := range accs {
		if vals := a.Values(); vals != nil {
			if err = m.catalog.ReplaceColumnValues(ctx, table, names[i], vals); err != nil {
				return nil, fmt.Errorf("tabular: column values: %w", err)
			}
		}
	}

	headerRow := -1
	if len(in.Profile.HeaderRows) > 0 {
		headerRow = in.Profile.HeaderRows[len(in.Profile.HeaderRows)-1]
	}
	profJSON, err := json.Marshal(in.Profile)
	if err != nil {
		return nil, fmt.Errorf("tabular: marshal profile: %w", err)
	}
	res.Columns, res.Stats = specs, stats
	if err = m.catalog.Insert(ctx, CatalogEntry{
		FileID: in.FileID, KBID: in.KBID, SheetName: in.Sheet.Name, TableName: table, FileName: in.FileName,
		Columns: specs, RowCount: n, SheetIndex: in.SheetIndex, RegionIndex: in.RegionIndex, SheetKind: "table", Hidden: in.Sheet.Hidden,
		HeaderRow: headerRow, Profile: profJSON, ColumnStats: stats,
	}); err != nil {
		return nil, fmt.Errorf("tabular: catalog insert: %w", err)
	}
	return res, nil
}

// assembleSpecs turns the accumulators' per-column FinalSpec decisions into
// the table's final column list + catalog stats, applying Ruling R8: a
// shadow column's default "<name>_num" name is computed by FinalSpec without
// seeing sibling columns, so it can collide with a real (primary) column
// whose header happens to sanitize to that exact name. Primaries are already
// unique (NewAccumulators dedupes headers up front), so deduping the
// combined [primaries..., shadows...] name list can only ever rename a
// shadow — never a primary — and primaries keep first-claim on any name.
// The returned stats slice is kept 1:1 with specs (a shadow column gets its
// own ColumnStat via ShadowStat, immediately after its primary's), so the
// catalog carries a stat entry for every materialized column, not just every
// accumulator. Extracted from MaterializeRegion so it is unit-testable
// without a DB.
func assembleSpecs(accs []*ColumnAccumulator, n int64) ([]ColumnSpec, []ColumnStat) {
	primaries := make([]ColumnSpec, len(accs))
	shadows := make([]*ColumnSpec, len(accs)) // nil where the column has no shadow
	for i, a := range accs {
		p, s := a.FinalSpec()
		primaries[i] = p
		shadows[i] = s
	}

	names := make([]string, 0, len(accs)*2)
	for _, p := range primaries {
		names = append(names, p.Name)
	}
	shadowAt := make([]int, 0, len(accs)) // acc index for each shadow name appended, in order
	for i, s := range shadows {
		if s != nil {
			names = append(names, s.Name)
			shadowAt = append(shadowAt, i)
		}
	}
	deduped := DedupeIdentifiers(names)
	for k, i := range shadowAt {
		shadows[i].Name = deduped[len(primaries)+k]
	}

	specs := make([]ColumnSpec, 0, len(accs)*2)
	stats := make([]ColumnStat, 0, len(accs)*2)
	for i, a := range accs {
		specs = append(specs, primaries[i])
		stats = append(stats, a.Stat(primaries[i], shadows[i], n))
		if shadows[i] != nil {
			specs = append(specs, *shadows[i])
			stats = append(stats, a.ShadowStat(*shadows[i], n))
		}
	}
	return specs, stats
}

// countCoercionFailures runs one SELECT over the staging table that sums,
// per non-text typed column, the rows where the source text was non-NULL but
// the cast produced NULL — the CASE in castValue makes "empty" and
// "failed to cast" otherwise indistinguishable in the typed table alone.
func (m *Materializer) countCoercionFailures(ctx context.Context, stage string, specs []ColumnSpec) (int64, error) {
	var exprs []string
	for _, c := range specs {
		if c.Type == TypeText {
			continue
		}
		src := c.Name
		if c.ShadowOf != "" {
			src = c.ShadowOf
		}
		exprs = append(exprs, fmt.Sprintf(
			`COALESCE(SUM(CASE WHEN %s IS NOT NULL AND (%s) IS NULL THEN 1 ELSE 0 END), 0)`,
			q(src), castValue(c)))
	}
	if len(exprs) == 0 {
		return 0, nil
	}
	sql := fmt.Sprintf(`SELECT %s FROM %s.%s`, strings.Join(exprs, " + "), q(TabularSchema), q(stage))
	var total int64
	if err := m.pool.QueryRow(ctx, sql).Scan(&total); err != nil {
		return 0, fmt.Errorf("tabular: coercion failures: %w", err)
	}
	return total, nil
}

// dropTable drops one physical table if it exists. Best-effort: a failure
// here must never mask the caller's real error, so it is logged at debug and
// swallowed.
func (m *Materializer) dropTable(ctx context.Context, name string) {
	if _, err := m.pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s.%s`, q(TabularSchema), q(name))); err != nil {
		logctx.From(ctx).Debug("tabular: drop table failed", "table", name, "error", err)
	}
}

// DropTablesForFile drops a file's per-region tables (and any staging
// leftovers), its column-values rows, and its catalog rows. Called by the
// cascade deleter on file/KB deletion, and by MaterializeRegion's own error
// path to undo partial work.
func (m *Materializer) DropTablesForFile(ctx context.Context, fileID string) error {
	names, err := m.catalog.TableNamesByFile(ctx, fileID)
	if err != nil {
		return err
	}
	for _, n := range names {
		m.dropTable(ctx, n)
		m.dropTable(ctx, stagingName(n))
	}
	if len(names) > 0 {
		if err := m.catalog.DeleteValuesForTables(ctx, names); err != nil {
			return err
		}
	}
	return m.catalog.DeleteByFile(ctx, fileID)
}
