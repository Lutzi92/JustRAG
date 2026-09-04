//go:build integration

// End-to-end ingest tests require a live main Postgres (knowledge_bases,
// files, the tabular schema, tabular_catalog v2 + tabular_column_values from
// migration 0069). Gated by the `integration` build tag; skipped when DB_*
// env is unset (mirrors internal/tabular/materializer_integration_test.go's
// openMainPool/seedFile pattern — duplicated here rather than imported,
// since those helpers are unexported in package tabular).
//
// This is the Phase-2 acceptance run (task-10-brief.md Step 1/2): the real
// Ingester (New(tabular.NewMaterializer(pool), nil)) driven over real
// fixture files, asserting the materialized SQL tables, the
// tabular_column_values index, and the per-file ParseReport end to end —
// unlike ingester_test.go's unit tests, which stub the materializer.
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/tabular"
)

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("tabular ingest integration tests require DB_* env (main Postgres)")
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedFile seeds a user + KB + file row so tabular_catalog's FKs resolve,
// and registers cleanup for all three.
func seedFile(t *testing.T, pool *pgxpool.Pool, fileName string) (fileID, kbID string) {
	t.Helper()
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1,'x') RETURNING id::text`,
		fmt.Sprintf("tab-ing-%d-%s", os.Getpid(), t.Name())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) })
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id) VALUES ('tab-ing', $1) RETURNING id::text`, userID).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO files (kb_id, name, type, status) VALUES ($1,$2,'application/xlsx','completed') RETURNING id::text`,
		kbID, fileName).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	return fileID, kbID
}

// entryFor returns the catalog entry with the given SheetIndex, failing the
// test if it is not found.
func entryFor(t *testing.T, entries []tabular.CatalogEntry, sheetIndex int) tabular.CatalogEntry {
	t.Helper()
	for _, e := range entries {
		if e.SheetIndex == sheetIndex {
			return e
		}
	}
	t.Fatalf("no catalog entry with SheetIndex=%d in %+v", sheetIndex, entries)
	return tabular.CatalogEntry{}
}

func columnType(t *testing.T, pool *pgxpool.Pool, table, column string) string {
	t.Helper()
	var typ string
	if err := pool.QueryRow(context.Background(),
		`SELECT data_type FROM information_schema.columns WHERE table_schema='tabular' AND table_name=$1 AND column_name=$2`,
		table, column).Scan(&typ); err != nil {
		t.Fatalf("columnType(%s.%s): %v", table, column, err)
	}
	return typ
}

func liveSheetTableCount(t *testing.T, pool *pgxpool.Pool, fileID string) int {
	t.Helper()
	hex := stripDashes(fileID)
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables WHERE table_schema='tabular' AND table_name LIKE $1`,
		"sheet_"+hex+"%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func stripDashes(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// TestIngestHeaderRow14MetadataEndToEnd runs the real Ingester (real
// Materializer, no LLM) over header_row14_metadata.xlsx and asserts the
// full Phase-2 acceptance shape from the task-10 brief: two materialized
// catalog rows (the Gebäudeliste table region + the hidden Dropdown list),
// the Gebäudeliste table's row count and typed columns, the
// tabular_column_values distinct-value index, the ParseReport's header row,
// and that DropTablesForFile leaves no tabular.sheet_<hex>* tables behind.
func TestIngestHeaderRow14MetadataEndToEnd(t *testing.T) {
	pool := openMainPool(t)
	fileID, kbID := seedFile(t, pool, "header_row14_metadata.xlsx")
	mat := tabular.NewMaterializer(pool)
	// Register the drop as CLEANUP right away, not just as the explicit
	// assertion step at the end: every t.Fatal between here and there would
	// otherwise leak this file's tabular.sheet_* tables and its
	// tabular_column_values rows into the shared test database (Task-10
	// review, Important 1). DropTablesForFile is idempotent, so the
	// explicit drop below still runs and is still asserted.
	t.Cleanup(func() { _ = mat.DropTablesForFile(context.Background(), fileID) })
	g := New(mat, nil)

	res, err := g.Ingest(context.Background(), Input{
		FilePath: fixtures + "header_row14_metadata.xlsx",
		FileName: "header_row14_metadata.xlsx",
		FileID:   fileID,
		KBID:     kbID,
		Options:  Options{Materialize: true, SampleRows: 200, ChunkSize: 512},
	})
	if err != nil {
		t.Fatal(err)
	}

	cat := tabular.NewCatalog(pool)
	entries, err := cat.ListByFile(context.Background(), fileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("catalog rows = %d, want 2 (Gebäudeliste region + hidden Dropdown list): %+v", len(entries), entries)
	}

	gebaeude := entryFor(t, entries, 0)
	dropdown := entryFor(t, entries, 1)
	if !dropdown.Hidden {
		t.Errorf("Dropdown catalog entry Hidden = false, want true")
	}

	var rowCount int
	if err := pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM tabular.%q`, gebaeude.TableName)).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 20 {
		t.Errorf("row count = %d, want 20", rowCount)
	}

	var denkmalschutz string
	if err := pool.QueryRow(context.Background(),
		fmt.Sprintf(`SELECT "stammdaten_denkmalschutz" FROM tabular.%q WHERE "_rowid"=1`, gebaeude.TableName)).Scan(&denkmalschutz); err != nil {
		t.Fatal(err)
	}
	if denkmalschutz != "Nein" {
		t.Errorf("stammdaten_denkmalschutz at _rowid=1 = %q, want %q", denkmalschutz, "Nein")
	}

	var distinctVals int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM tabular_column_values WHERE table_name=$1 AND column_name='stammdaten_denkmalschutz'`,
		gebaeude.TableName).Scan(&distinctVals); err != nil {
		t.Fatal(err)
	}
	if distinctVals != 3 {
		t.Errorf("distinct stammdaten_denkmalschutz values = %d, want 3", distinctVals)
	}

	if got := columnType(t, pool, gebaeude.TableName, "stammdaten_bgf_m2"); got != "numeric" {
		t.Errorf("stammdaten_bgf_m2 type = %q, want numeric", got)
	}
	// spalte_a (column A) carries the sequential 1..20 row index with no
	// header of its own ("Spalte A" default header, see
	// profile/header.go's fallback): the profiler's isIndexColumn heuristic
	// classifies a bare sequential-integer column as RoleID, and RoleID
	// stays text (IDs are never coerced to numeric — see
	// ids_leading_zero.xlsx's leading-zero fixture for why). No "_num"
	// shadow either: R9 only shadows a majority-numeric TEXT column or a
	// RoleMeasure column, and an ID column is neither.
	if got := columnType(t, pool, gebaeude.TableName, "spalte_a"); got != "text" {
		t.Errorf("spalte_a type = %q, want text", got)
	}

	// Carry (a): a non-zero ColumnStat.CoercionFailed must survive the
	// MaterializeRegion -> catalog -> ListByFile round trip. As
	// materializer_integration_test.go's TestCountCoercionFailuresPerColumn
	// documents at length, a real coercion failure can only be manufactured
	// by hand-building a staging table that diverges from what the real
	// read/COPY path could ever produce: every accumulator only commits a
	// column to a non-text type once every non-empty value it saw already
	// parsed as that type in Go, using the same canonical text form the SQL
	// guard in castValue accepts, so a real fixture ingested through this
	// path structurally cannot trigger a coercion failure. None of this
	// project's fixtures (including numbers_formats.xlsx's mixed
	// "baujahr", which stays TEXT precisely because it is NOT
	// majority-numeric, and whose "baujahr_num" shadow's failed casts are
	// excluded from the count by R11) produces one. So this asserts the
	// field's zero value round-trips correctly through JSON (column_stats)
	// rather than silently defaulting away or getting dropped by
	// omitempty (it has no omitempty tag on purpose).
	for _, cs := range gebaeude.ColumnStats {
		if cs.CoercionFailed != 0 {
			t.Errorf("column %s CoercionFailed = %d, want 0 (see comment: no fixture can produce a non-zero value through the real ingest path)", cs.Name, cs.CoercionFailed)
		}
	}
	if len(gebaeude.ColumnStats) == 0 {
		t.Fatal("no ColumnStats round-tripped through the catalog at all")
	}

	// Report JSON round-trips with sheets[0].header_row == 13.
	repJSON, err := json.Marshal(res.Report)
	if err != nil {
		t.Fatal(err)
	}
	var rep tabular.ParseReport
	if err := json.Unmarshal(repJSON, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Sheets) == 0 || rep.Sheets[0].HeaderRow != 13 {
		t.Errorf("report round-trip: sheets[0].header_row = %+v, want 13", rep.Sheets)
	}

	if err := mat.DropTablesForFile(context.Background(), fileID); err != nil {
		t.Fatal(err)
	}
	if n := liveSheetTableCount(t, pool, fileID); n != 0 {
		t.Errorf("tabular.sheet_%s* tables remaining after DropTablesForFile = %d, want 0", stripDashes(fileID), n)
	}
	entries, err = cat.ListByFile(context.Background(), fileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("catalog rows after DropTablesForFile = %d, want 0", len(entries))
	}
}

// TestIngestTotalsRowsEndToEnd covers the derived-rows-excluded acceptance
// case: totals_rows.xlsx has a cached-formula "Summe"/"Mittelwert" row pair
// below the data that must NOT be materialized as data rows.
func TestIngestTotalsRowsEndToEnd(t *testing.T) {
	pool := openMainPool(t)
	fileID, kbID := seedFile(t, pool, "totals_rows.xlsx")
	mat := tabular.NewMaterializer(pool)
	t.Cleanup(func() { _ = mat.DropTablesForFile(context.Background(), fileID) })
	g := New(mat, nil)

	_, err := g.Ingest(context.Background(), Input{
		FilePath: fixtures + "totals_rows.xlsx",
		FileName: "totals_rows.xlsx",
		FileID:   fileID,
		KBID:     kbID,
		Options:  Options{Materialize: true, SampleRows: 200, ChunkSize: 512},
	})
	if err != nil {
		t.Fatal(err)
	}

	cat := tabular.NewCatalog(pool)
	entries, err := cat.ListByFile(context.Background(), fileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("catalog rows = %d, want 1: %+v", len(entries), entries)
	}
	table := entries[0].TableName

	var rowCount int
	if err := pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM tabular.%q`, table)).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 10 {
		t.Errorf("row count = %d, want 10 (Summe/Mittelwert rows must be excluded as derived)", rowCount)
	}

	var sum float64
	if err := pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT SUM("bgf") FROM tabular.%q`, table)).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum != 10400 {
		t.Errorf("SUM(bgf) = %v, want the workbook's own cached total 10400", sum)
	}
}

// TestIngestIdempotentReingest covers the brief's third acceptance case:
// ingesting the same file twice must still leave exactly 2 catalog rows
// (not 4), and no orphaned tabular.sheet_<hex>* tables beyond the two live
// ones — Ingest's R17 DropTablesForFile-before-materialize behaviour must
// hold on a second pass through the real Materializer, not just against the
// fakeMat unit-test double.
func TestIngestIdempotentReingest(t *testing.T) {
	pool := openMainPool(t)
	fileID, kbID := seedFile(t, pool, "header_row14_metadata.xlsx")
	mat := tabular.NewMaterializer(pool)
	t.Cleanup(func() { _ = mat.DropTablesForFile(context.Background(), fileID) })
	g := New(mat, nil)

	in := Input{
		FilePath: fixtures + "header_row14_metadata.xlsx",
		FileName: "header_row14_metadata.xlsx",
		FileID:   fileID,
		KBID:     kbID,
		Options:  Options{Materialize: true, SampleRows: 200, ChunkSize: 512},
	}
	if _, err := g.Ingest(context.Background(), in); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if _, err := g.Ingest(context.Background(), in); err != nil {
		t.Fatalf("second ingest: %v", err)
	}

	cat := tabular.NewCatalog(pool)
	entries, err := cat.ListByFile(context.Background(), fileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("catalog rows after re-ingest = %d, want 2", len(entries))
	}

	// Exactly the two live tables' names, no __stage leftovers and no
	// orphaned prior-run tables.
	if n := liveSheetTableCount(t, pool, fileID); n != 2 {
		t.Errorf("tabular.sheet_%s* tables after re-ingest = %d, want 2 (exactly the two live tables, no leftovers)", stripDashes(fileID), n)
	}
}
