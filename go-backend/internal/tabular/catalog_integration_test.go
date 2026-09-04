//go:build integration

// Catalog v2 tests require a live main Postgres with migration 0069 applied
// (tabular_catalog's sheet_index/region_index/sheet_kind/hidden/header_row/
// profile/column_stats columns, plus tabular_column_values). Gated by the
// `integration` build tag; skipped when DB_* env is unset (openMainPool, in
// materializer_integration_test.go).

package tabular

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestCatalogV2RoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := openMainPool(t)

	var userID, kbID, fileID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1,'x') RETURNING id::text`,
		fmt.Sprintf("tab-catv2-%d", os.Getpid())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) })
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id) VALUES ('tab-catv2', $1) RETURNING id::text`, userID).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO files (kb_id, name, type, status) VALUES ($1,'catv2.xlsx','application/xlsx','completed') RETURNING id::text`, kbID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}

	cat := NewCatalog(pool)
	tableName := TableNameForRegion(fileID, 0, 0)
	entry := CatalogEntry{
		FileID:      fileID,
		KBID:        kbID,
		SheetName:   "Sheet1",
		TableName:   tableName,
		Columns:     []ColumnSpec{{Original: "A", Name: "a", Type: TypeText}},
		RowCount:    3,
		SheetIndex:  0,
		RegionIndex: 0,
		SheetKind:   "table",
		Hidden:      true,
		HeaderRow:   13,
		Profile:     json.RawMessage(`{"x":1}`),
		ColumnStats: []ColumnStat{{Name: "a", ValueSet: []string{"x"}}},
	}
	t.Cleanup(func() { cat.DeleteByFile(ctx, fileID) })
	if err := cat.Insert(ctx, entry); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	checkEntry := func(t *testing.T, got CatalogEntry) {
		t.Helper()
		if got.FileName != "catv2.xlsx" {
			t.Errorf("FileName = %q, want catv2.xlsx", got.FileName)
		}
		if got.SheetIndex != 0 || got.RegionIndex != 0 {
			t.Errorf("SheetIndex/RegionIndex = %d/%d, want 0/0", got.SheetIndex, got.RegionIndex)
		}
		if got.SheetKind != "table" {
			t.Errorf("SheetKind = %q, want table", got.SheetKind)
		}
		if !got.Hidden {
			t.Errorf("Hidden = false, want true")
		}
		if got.HeaderRow != 13 {
			t.Errorf("HeaderRow = %d, want 13", got.HeaderRow)
		}
		var profile map[string]int
		if err := json.Unmarshal(got.Profile, &profile); err != nil {
			t.Errorf("Profile unmarshal: %v (raw %s)", err, got.Profile)
		} else if profile["x"] != 1 {
			t.Errorf("Profile = %s, want {\"x\":1}", got.Profile)
		}
		if len(got.ColumnStats) != 1 || got.ColumnStats[0].Name != "a" || len(got.ColumnStats[0].ValueSet) != 1 || got.ColumnStats[0].ValueSet[0] != "x" {
			t.Errorf("ColumnStats = %+v, want [{Name:a ValueSet:[x]}]", got.ColumnStats)
		}
	}

	byKB, err := cat.ListByKB(ctx, kbID)
	if err != nil {
		t.Fatalf("ListByKB: %v", err)
	}
	if len(byKB) != 1 {
		t.Fatalf("ListByKB len = %d, want 1", len(byKB))
	}
	checkEntry(t, byKB[0])

	byFile, err := cat.ListByFile(ctx, fileID)
	if err != nil {
		t.Fatalf("ListByFile: %v", err)
	}
	if len(byFile) != 1 {
		t.Fatalf("ListByFile len = %d, want 1", len(byFile))
	}
	checkEntry(t, byFile[0])

	// ReplaceColumnValues: two values, then one (replace, not append).
	if err := cat.ReplaceColumnValues(ctx, "t", "c", map[string]int64{"a": 1, "b": 2}); err != nil {
		t.Fatalf("ReplaceColumnValues (2 values): %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tabular_column_values WHERE table_name='t'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 2 {
		t.Fatalf("count after first ReplaceColumnValues = %d, want 2", count)
	}

	if err := cat.ReplaceColumnValues(ctx, "t", "c", map[string]int64{"a": 1}); err != nil {
		t.Fatalf("ReplaceColumnValues (1 value): %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tabular_column_values WHERE table_name='t'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("count after second ReplaceColumnValues = %d, want 1", count)
	}

	if err := cat.DeleteValuesForTables(ctx, []string{"t"}); err != nil {
		t.Fatalf("DeleteValuesForTables: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tabular_column_values WHERE table_name='t'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Fatalf("count after DeleteValuesForTables = %d, want 0", count)
	}
}

func TestCatalogV2InsertNullsOptionalColumns(t *testing.T) {
	ctx := context.Background()
	pool := openMainPool(t)

	var userID, kbID, fileID string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1,'x') RETURNING id::text`,
		fmt.Sprintf("tab-catv2null-%d", os.Getpid())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) })
	if err := pool.QueryRow(ctx, `INSERT INTO knowledge_bases (name, user_id) VALUES ('tab-catv2null', $1) RETURNING id::text`, userID).Scan(&kbID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO files (kb_id, name, type, status) VALUES ($1,'null-test.xlsx','application/xlsx','completed') RETURNING id::text`, kbID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}

	cat := NewCatalog(pool)
	tableName := TableNameForRegion(fileID, 0, 0)
	entry := CatalogEntry{
		FileID:      fileID,
		KBID:        kbID,
		SheetName:   "Sheet1",
		TableName:   tableName,
		Columns:     []ColumnSpec{{Original: "A", Name: "a", Type: TypeText}},
		RowCount:    1,
		SheetKind:   "table",
		HeaderRow:   -1,
		Profile:     nil,
		ColumnStats: nil,
	}
	t.Cleanup(func() { cat.DeleteByFile(ctx, fileID) })
	if err := cat.Insert(ctx, entry); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Assert via SQL that profile, column_stats, and header_row are all NULL
	var profileIsNull, columnStatsIsNull, headerRowIsNull bool
	if err := pool.QueryRow(ctx,
		`SELECT profile IS NULL, column_stats IS NULL, header_row IS NULL FROM tabular_catalog WHERE table_name = $1`,
		tableName).Scan(&profileIsNull, &columnStatsIsNull, &headerRowIsNull); err != nil {
		t.Fatalf("SQL query: %v", err)
	}
	if !profileIsNull {
		t.Errorf("profile IS NULL = false, want true")
	}
	if !columnStatsIsNull {
		t.Errorf("column_stats IS NULL = false, want true")
	}
	if !headerRowIsNull {
		t.Errorf("header_row IS NULL = false, want true")
	}

	// Assert that ListByFile returns one entry with nil Profile/ColumnStats and HeaderRow == -1
	byFile, err := cat.ListByFile(ctx, fileID)
	if err != nil {
		t.Fatalf("ListByFile: %v", err)
	}
	if len(byFile) != 1 {
		t.Fatalf("ListByFile len = %d, want 1", len(byFile))
	}
	got := byFile[0]
	if got.Profile != nil && len(got.Profile) > 0 {
		t.Errorf("Profile = %s, want nil or empty", got.Profile)
	}
	if got.ColumnStats != nil && len(got.ColumnStats) > 0 {
		t.Errorf("ColumnStats = %+v, want nil or empty", got.ColumnStats)
	}
	if got.HeaderRow != -1 {
		t.Errorf("HeaderRow = %d, want -1", got.HeaderRow)
	}
}
