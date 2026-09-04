//go:build integration

// Value-lookup and query-log tests require a live main Postgres with
// migration 0069 applied (tabular_column_values, tabular_query_log). Gated
// by the `integration` build tag; skipped when DB_* env is unset
// (openMainPool, seedFile in materializer_integration_test.go).

package tabular

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestLookupValuesExactPrefixSubstring(t *testing.T) {
	ctx := context.Background()
	pool := openMainPool(t)
	fileID, kbID := seedFile(t, pool)

	cat := NewCatalog(pool)
	tableName := TableNameForRegion(fileID, 0, 0)
	entry := CatalogEntry{
		FileID:    fileID,
		KBID:      kbID,
		SheetName: "Liegenschaften",
		TableName: tableName,
		Columns:   []ColumnSpec{{Original: "Liegenschaft", Name: "liegenschaft", Type: TypeText}},
		RowCount:  6,
		SheetKind: "table",
		HeaderRow: -1,
	}
	if err := cat.Insert(ctx, entry); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	t.Cleanup(func() { cat.DeleteByFile(ctx, fileID) })

	// Seed values for (sheet_..._0_0, liegenschaft): "Goethestraße 55" (3
	// rows), "Goethestraße 57" (1), "Am Goetheplatz 1" (2).
	values := map[string]int64{
		"Goethestraße 55":  3,
		"Goethestraße 57":  1,
		"Am Goetheplatz 1": 2,
	}
	if err := cat.ReplaceColumnValues(ctx, tableName, "liegenschaft", values); err != nil {
		t.Fatalf("ReplaceColumnValues: %v", err)
	}
	t.Cleanup(func() { cat.DeleteValuesForTables(ctx, []string{tableName}) })

	entries, err := cat.ListByKB(ctx, kbID)
	if err != nil {
		t.Fatalf("ListByKB: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ListByKB len = %d, want 1", len(entries))
	}

	// "goethestraße 55" -> exact hit: Value "Goethestraße 55", Match
	// "exact", RowCount 3.
	hits, err := cat.LookupValues(ctx, entries, []string{"goethestraße 55"}, 5)
	if err != nil {
		t.Fatalf("LookupValues (exact): %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("exact hits len = %d, want 1: %+v", len(hits), hits)
	}
	if hits[0].Value != "Goethestraße 55" || hits[0].Match != "exact" || hits[0].RowCount != 3 {
		t.Errorf("exact hit = %+v, want {Value: Goethestraße 55, Match: exact, RowCount: 3}", hits[0])
	}
	if hits[0].TableName != tableName || hits[0].ColumnName != "liegenschaft" || hits[0].SheetName != "Liegenschaften" || hits[0].FileName != "n.xlsx" {
		t.Errorf("exact hit metadata = %+v", hits[0])
	}

	// "Goethe" -> prefix hits "Goethestraße 55" then "Goethestraße 57"
	// (ordered by row_count desc among equal match quality), then substring
	// "Am Goetheplatz 1"; perLiteral=2 caps the result to exactly the two
	// prefix hits.
	hits, err = cat.LookupValues(ctx, entries, []string{"Goethe"}, 2)
	if err != nil {
		t.Fatalf("LookupValues (prefix): %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("prefix hits len = %d, want 2: %+v", len(hits), hits)
	}
	if hits[0].Value != "Goethestraße 55" || hits[0].Match != "prefix" {
		t.Errorf("hit[0] = %+v, want Value=Goethestraße 55 Match=prefix", hits[0])
	}
	if hits[1].Value != "Goethestraße 57" || hits[1].Match != "prefix" {
		t.Errorf("hit[1] = %+v, want Value=Goethestraße 57 Match=prefix", hits[1])
	}

	// "x" -> too short (< 2 runes), skipped: no hits, no error.
	hits, err = cat.LookupValues(ctx, entries, []string{"x"}, 5)
	if err != nil {
		t.Fatalf("LookupValues (short literal): %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("short-literal hits = %+v, want none", hits)
	}

	// A literal containing LIKE metacharacters ('%', '_') must not error
	// and must not be interpreted as a wildcard: "a%" would (if unescaped)
	// match every seeded value, since each contains the letter "a"/"A".
	// Escaped, none of the values contain the literal substring "a%".
	hits, err = cat.LookupValues(ctx, entries, []string{"a%"}, 5)
	if err != nil {
		t.Fatalf("LookupValues (percent literal): %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("percent-literal hits = %+v, want none (LIKE metachar must be escaped, not treated as wildcard)", hits)
	}

	// Same for "_" (single-char wildcard): "am_" would (if unescaped) match
	// "Am Goetheplatz 1" (the "_" standing in for the space after "Am").
	hits, err = cat.LookupValues(ctx, entries, []string{"am_"}, 5)
	if err != nil {
		t.Fatalf("LookupValues (underscore literal): %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("underscore-literal hits = %+v, want none (LIKE metachar must be escaped, not treated as wildcard)", hits)
	}
}

func TestInsertQueryLog(t *testing.T) {
	ctx := context.Background()
	pool := openMainPool(t)
	_, kbID := seedFile(t, pool)

	cat := NewCatalog(pool)

	msgID := uuid.NewString()
	full := QueryLogEntry{
		KBID:      kbID,
		MessageID: msgID,
		Question:  "Wie viele Gebäude haben Denkmalschutz?",
		SQL:       "SELECT count(*) FROM tabular.sheet_x_0_0 WHERE denkmalschutz IS NOT NULL",
		RowCount:  3,
		Outcome:   "fired_ok",
	}
	if err := cat.InsertQueryLog(ctx, full); err != nil {
		t.Fatalf("InsertQueryLog (full): %v", err)
	}

	var gotMsgID, gotSQL string
	var gotRows int
	var gotOutcome, gotQuestion string
	if err := pool.QueryRow(ctx,
		`SELECT message_id::text, question, sql, row_count, outcome FROM tabular_query_log WHERE kb_id = $1 AND outcome = 'fired_ok'`,
		kbID).Scan(&gotMsgID, &gotQuestion, &gotSQL, &gotRows, &gotOutcome); err != nil {
		t.Fatalf("select full row: %v", err)
	}
	if gotMsgID != msgID || gotQuestion != full.Question || gotSQL != full.SQL || gotRows != full.RowCount || gotOutcome != full.Outcome {
		t.Errorf("full row = msgID=%q question=%q sql=%q rows=%d outcome=%q, want msgID=%q question=%q sql=%q rows=%d outcome=%q",
			gotMsgID, gotQuestion, gotSQL, gotRows, gotOutcome, msgID, full.Question, full.SQL, full.RowCount, full.Outcome)
	}

	// Empty MessageID/SQL and RowCount == -1 round-trip to SQL NULL.
	sparse := QueryLogEntry{
		KBID:     kbID,
		Question: "skipped question",
		RowCount: -1,
		Outcome:  "skipped_no_tables",
	}
	if err := cat.InsertQueryLog(ctx, sparse); err != nil {
		t.Fatalf("InsertQueryLog (sparse): %v", err)
	}

	var msgIsNull, sqlIsNull, rowsIsNull bool
	if err := pool.QueryRow(ctx,
		`SELECT message_id IS NULL, sql IS NULL, row_count IS NULL FROM tabular_query_log WHERE kb_id = $1 AND outcome = 'skipped_no_tables'`,
		kbID).Scan(&msgIsNull, &sqlIsNull, &rowsIsNull); err != nil {
		t.Fatalf("select sparse row: %v", err)
	}
	if !msgIsNull {
		t.Error("message_id IS NULL = false, want true")
	}
	if !sqlIsNull {
		t.Error("sql IS NULL = false, want true")
	}
	if !rowsIsNull {
		t.Error("row_count IS NULL = false, want true")
	}
}
