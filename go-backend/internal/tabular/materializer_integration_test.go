//go:build integration

// Materializer tests require a live main Postgres (knowledge_bases, files,
// the tabular schema, tabular_catalog v2 from migration 0069). Gated by the
// `integration` build tag; skipped when DB_* env is unset.

package tabular

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/sheetsource"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("tabular integration tests require DB_* env (main Postgres)")
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

// seedFile seeds a user + KB + file row so the tabular_catalog FKs resolve,
// and registers cleanup for all three.
func seedFile(t *testing.T, pool *pgxpool.Pool) (fileID, kbID string) {
	t.Helper()
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1,'x') RETURNING id::text`,
		fmt.Sprintf("tab-mat-%d-%s", os.Getpid(), t.Name())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) })
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id) VALUES ('tab-mat', $1) RETURNING id::text`, userID).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO files (kb_id, name, type, status) VALUES ($1,'n.xlsx','application/xlsx','completed') RETURNING id::text`, kbID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	return fileID, kbID
}

func TestMaterializeRegionTypesAndValues(t *testing.T) {
	pool := openMainPool(t)
	fileID, kbID := seedFile(t, pool)
	src, err := sheetsource.Open("../sheetsource/testdata/numbers_formats.xlsx", "numbers_formats.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	sample, err := sheetsource.CollectSample(src, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	sp := profile.ProfileSheet(sample, profile.Options{})
	m := NewMaterializer(pool)
	res, err := m.MaterializeRegion(context.Background(), RegionInput{Source: src, SheetIndex: 0, Sheet: sample.Info, Profile: sp.Regions[0],
		FileID: fileID, KBID: kbID, FileName: "numbers_formats.xlsx", MaxRows: 1000, MaxDistinct: 100})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.DropTablesForFile(context.Background(), fileID) })
	typeOf := func(col string) string {
		var typ string
		_ = pool.QueryRow(context.Background(), `SELECT data_type FROM information_schema.columns WHERE table_schema='tabular' AND table_name=$1 AND column_name=$2`, res.TableName, col).Scan(&typ)
		return typ
	}
	if typeOf("bgf") != "numeric" || typeOf("stand") != "date" || typeOf("datum2") != "date" || typeOf("baujahr") != "text" || typeOf("baujahr_num") != "numeric" || typeOf("betrag") != "numeric" || typeOf("anteil") != "numeric" {
		t.Errorf("types: bgf=%s stand=%s datum2=%s baujahr=%s baujahr_num=%s betrag=%s anteil=%s", typeOf("bgf"), typeOf("stand"), typeOf("datum2"), typeOf("baujahr"), typeOf("baujahr_num"), typeOf("betrag"), typeOf("anteil"))
	}
	var sum float64
	_ = pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT SUM("bgf") FROM tabular.%q`, res.TableName)).Scan(&sum)
	if sum < 21487.7 || sum > 21487.9 { // Σ (2143.28 + r), r = 1..10
		t.Errorf("SUM(bgf) = %v", sum)
	}
	var nums int
	_ = pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT count(*) FROM tabular.%q WHERE "baujahr_num" IS NOT NULL`, res.TableName)).Scan(&nums)
	if nums != 8 {
		t.Errorf("baujahr_num non-null = %d", nums)
	}
	var vals int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM tabular_column_values WHERE table_name=$1 AND column_name='gebaeude'`, res.TableName).Scan(&vals)
	if vals != 10 {
		t.Errorf("column values for gebaeude = %d", vals)
	}
	entries, err := NewCatalog(pool).ListByFile(context.Background(), fileID)
	if err != nil || len(entries) != 1 || entries[0].SheetKind != "table" || len(entries[0].ColumnStats) != len(res.Columns) || entries[0].FileName == "" {
		t.Errorf("catalog: %v %+v", err, entries)
	}
	if err := m.DropTablesForFile(context.Background(), fileID); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM tabular_column_values WHERE table_name=$1`, res.TableName).Scan(&vals)
	if vals != 0 {
		t.Errorf("values not dropped: %d", vals)
	}
}

func TestMaterializeRegionIDsAndDerivedRows(t *testing.T) {
	pool := openMainPool(t)
	fileID, kbID := seedFile(t, pool)
	m := NewMaterializer(pool)
	t.Cleanup(func() { _ = m.DropTablesForFile(context.Background(), fileID) })
	run := func(fixture string, sheet int) (*RegionResult, error) {
		src, err := sheetsource.Open("../sheetsource/testdata/"+fixture, fixture)
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		sample, _ := sheetsource.CollectSample(src, sheet, 200)
		sp := profile.ProfileSheet(sample, profile.Options{})
		var rp profile.RegionProfile
		for _, r := range sp.Regions {
			if r.Kind == profile.KindTable {
				rp = r
				break
			}
		}
		return m.MaterializeRegion(context.Background(), RegionInput{Source: src, SheetIndex: sheet, Sheet: sample.Info, Profile: rp, RegionIndex: 0, FileID: fileID, KBID: kbID, FileName: fixture, MaxDistinct: 100})
	}
	ids, err := run("ids_leading_zero.xlsx", 0)
	if err != nil {
		t.Fatal(err)
	}
	var lief, mat string
	_ = pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT "lieferantennummer", "material"::text FROM tabular.%q WHERE "_rowid"=1`, ids.TableName)).Scan(&lief, &mat)
	if lief != "0002001919" || mat != "931404826" {
		t.Errorf("ids: %q %q", lief, mat)
	}
	_ = m.DropTablesForFile(context.Background(), fileID)
	tot, err := run("totals_rows.xlsx", 0)
	if err != nil {
		t.Fatal(err)
	}
	if tot.RowsMaterialised != 10 || tot.DerivedRowsSkipped != 2 {
		t.Errorf("totals: %+v", tot)
	}
	// Every row counted in RowsRead lands in exactly one of the other
	// buckets (RegionResult's own contract).
	if tot.RowsRead != tot.RowsMaterialised+tot.RowsDropped+tot.DerivedRowsSkipped+tot.RowsSkippedEmpty {
		t.Errorf("row bucket identity broken: %+v", tot)
	}
	var sum float64
	_ = pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT SUM("bgf") FROM tabular.%q`, tot.TableName)).Scan(&sum)
	if sum != 10400 {
		t.Errorf("SUM(bgf) = %v want the workbook's own cached total 10400", sum)
	}
}

// TestMaterializeRegionMaxRowsCap covers item 3 of fix round 1: the MaxRows
// cap check must happen BEFORE any accumulator is touched, so rows past the
// cap are counted in RowsDropped without affecting stats or the
// distinct-value maps at all. A bug that ran Add() before checking the cap
// would inflate NonEmpty beyond RowsMaterialised, driving
// ColumnStat.NullCount (totalRows - NonEmpty) negative, and would leak
// dropped rows' values into tabular_column_values.
func TestMaterializeRegionMaxRowsCap(t *testing.T) {
	pool := openMainPool(t)
	fileID, kbID := seedFile(t, pool)
	dir := t.TempDir()
	path := filepath.Join(dir, "maxrows.csv")
	if err := os.WriteFile(path, []byte("Name,Value\nA,1\nB,2\nC,3\nD,4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := sheetsource.Open(path, "maxrows.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	sample, err := sheetsource.CollectSample(src, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	sp := profile.ProfileSheet(sample, profile.Options{})
	var rp profile.RegionProfile
	for _, r := range sp.Regions {
		if r.Kind == profile.KindTable {
			rp = r
			break
		}
	}
	m := NewMaterializer(pool)
	res, err := m.MaterializeRegion(context.Background(), RegionInput{Source: src, SheetIndex: 0, Sheet: sample.Info, Profile: rp,
		FileID: fileID, KBID: kbID, FileName: "maxrows.csv", MaxRows: 2, MaxDistinct: 100})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.DropTablesForFile(context.Background(), fileID) })

	if res.RowsMaterialised != 2 {
		t.Errorf("RowsMaterialised = %d, want 2", res.RowsMaterialised)
	}
	if res.RowsDropped != 2 {
		t.Errorf("RowsDropped = %d, want 2", res.RowsDropped)
	}
	for _, st := range res.Stats {
		if st.NullCount < 0 {
			t.Errorf("stat %+v has a negative NullCount — an accumulator ran on a dropped row", st)
		}
	}

	rows, err := pool.Query(context.Background(), `SELECT value FROM tabular_column_values WHERE table_name=$1 AND column_name='name' ORDER BY value`, res.TableName)
	if err != nil {
		t.Fatal(err)
	}
	var vals []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		vals = append(vals, v)
	}
	rows.Close()
	if len(vals) != 2 || vals[0] != "A" || vals[1] != "B" {
		t.Errorf("tabular_column_values for name = %v, want [A B] (only the first 2 rows, admitted under MaxRows, may appear)", vals)
	}
}

// TestCountCoercionFailuresPerColumn covers item 2 + Ruling R11 directly
// against a hand-built staging table (bypassing MaterializeRegion's own
// read/COPY pass, which — by construction — never produces a mismatch
// between Go's type inference and the SQL guard: every accumulator only
// commits a column to TypeNumeric/TypeBool/TypeDate once every non-empty
// value already parsed as that type in Go, and the canonical text it writes
// (strconv.FormatFloat, "true"/"false", ISO dates) always satisfies the
// matching SQL regex/IN-list too. So a real coercion failure only shows up
// as a defensive guard against a Go/SQL divergence, not from ordinary
// fixture data — this test manufactures that divergence directly in SQL.
func TestCountCoercionFailuresPerColumn(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	stage := "coercion_test_stage"
	_, _ = pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS tabular.%q`, stage))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS tabular.%q`, stage)) })
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE tabular.%q ("_rowid" bigint, "amount" text, "flag" text)`, stage)); err != nil {
		t.Fatal(err)
	}
	// "amount": two values that pass the guarded numeric cast, one
	// ("12.34.56") that doesn't. "flag": every value passes the boolean cast.
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`INSERT INTO tabular.%q ("_rowid","amount","flag") VALUES (1,'10.5','true'),(2,'20','false'),(3,'12.34.56','true')`, stage)); err != nil {
		t.Fatal(err)
	}
	// A shadow spec ("amount_num", ShadowOf: "amount") recasts the SAME
	// source column that already has a failing value, so R11 is exercised
	// for real: if the exclusion regressed, the total below would be 2, not 1.
	specs := []ColumnSpec{
		{Name: "amount", Type: TypeNumeric},
		{Name: "flag", Type: TypeBool},
		{Name: "amount_num", Type: TypeNumeric, ShadowOf: "amount"},
	}
	stats := []ColumnStat{{Name: "amount"}, {Name: "flag"}, {Name: "amount_num"}}
	m := NewMaterializer(pool)
	total, err := m.countCoercionFailures(ctx, stage, specs, stats)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("total coercion failures = %d, want 1", total)
	}
	if stats[0].CoercionFailed != 1 {
		t.Errorf("amount.CoercionFailed = %d, want 1", stats[0].CoercionFailed)
	}
	if stats[1].CoercionFailed != 0 {
		t.Errorf("flag.CoercionFailed = %d, want 0", stats[1].CoercionFailed)
	}
	if stats[2].CoercionFailed != 0 {
		t.Errorf("amount_num (shadow).CoercionFailed = %d, want 0 (R11: shadows are excluded from the count)", stats[2].CoercionFailed)
	}
}
