//go:build integration

// C1/R20 end to end: deleting a file through the real HTTP handler and the
// real PGStore must also drop the `tabular.sheet_*` tables that file
// materialised, its tabular_column_values rows and its tabular_catalog rows.
// Requires a live main Postgres (migration 0069); skipped when DB_* is unset.

package files_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/tabular"
)

// seedTabularFile seeds a private KB with an owner and one spreadsheet file
// row, cleaning all three up afterwards.
func seedTabularFile(t *testing.T, pool *pgxpool.Pool) (kbID, fileID string) {
	t.Helper()
	ctx := context.Background()
	var userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash) VALUES ($1,'x') RETURNING id::text`,
		fmt.Sprintf("files-tab-%d", t.Name()[len(t.Name())-1])).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id=$1::uuid`, userID) }) //nolint:errcheck
	if err := pool.QueryRow(ctx,
		`INSERT INTO knowledge_bases (name, user_id) VALUES ('files-tabular-test', $1::uuid) RETURNING id::text`,
		userID).Scan(&kbID); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id=$1::uuid`, kbID) }) //nolint:errcheck
	if err := pool.QueryRow(ctx,
		`INSERT INTO files (kb_id, name, type, status, storage_path)
		 VALUES ($1::uuid, 'ledger.xlsx', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', 'completed', 'u/k/ledger.xlsx')
		 RETURNING id::text`, kbID).Scan(&fileID); err != nil {
		t.Fatalf("insert file: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM files WHERE id=$1::uuid`, fileID) }) //nolint:errcheck
	return kbID, fileID
}

func tableExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables WHERE table_schema='tabular' AND table_name=$1`,
		name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestDeleteFileDropsMaterialisedTables(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	kbID, fileID := seedTabularFile(t, pool)

	// Materialise a tiny table the way the ingester would: physical table,
	// catalog row, value index.
	table := tabular.TableNameForRegion(fileID, 0, 0)
	specs := []tabular.ColumnSpec{
		{Original: "Gebäude", Name: "gebaeude", Role: "text", Type: tabular.TypeText},
		{Original: "BGF", Name: "bgf", Role: "measure", Type: tabular.TypeNumeric},
	}
	// Plain DDL rather than tabular.BuildTypedTableSQL: that one builds the
	// typed table as a CREATE TABLE ... AS SELECT over the materialiser's
	// staging table, which does not exist here.
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE tabular.%q ("_rowid" bigint PRIMARY KEY, "gebaeude" text, "bgf" numeric)`, table)); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS tabular.%q`, table)) //nolint:errcheck
	})
	if _, err := pool.Exec(ctx, fmt.Sprintf(`INSERT INTO tabular.%q ("_rowid","gebaeude","bgf") VALUES (1,'Haus 1',100)`, table)); err != nil {
		t.Fatalf("insert row: %v", err)
	}
	cat := tabular.NewCatalog(pool)
	if err := cat.Insert(ctx, tabular.CatalogEntry{
		FileID: fileID, KBID: kbID, SheetName: "Sheet1", TableName: table, FileName: "ledger.xlsx",
		Columns: specs, RowCount: 1, SheetIndex: 0, RegionIndex: 0, SheetKind: "table", HeaderRow: 0,
	}); err != nil {
		t.Fatalf("catalog insert: %v", err)
	}
	if err := cat.ReplaceColumnValues(ctx, table, "gebaeude", map[string]int64{"Haus 1": 1}); err != nil {
		t.Fatalf("column values: %v", err)
	}

	var values int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tabular_column_values WHERE table_name=$1`, table).Scan(&values); err != nil {
		t.Fatal(err)
	}
	if values != 1 || !tableExists(t, pool, table) {
		t.Fatalf("fixture not set up: values=%d tableExists=%v", values, tableExists(t, pool, table))
	}

	// Delete through the real handler + real store + real materialiser.
	h := files.NewHandler(files.NewStore(pool), &mockStorage{}, noopChunks())
	h.SetTableDropper(tabular.NewMaterializer(pool))

	req := newRequest(http.MethodDelete, "/api/files/"+fileID)
	req.SetPathValue("id", fileID)
	req = withUser(req, &auth.Claims{ID: "00000000-0000-0000-0000-000000000001", Username: "root", Role: "superadmin"})
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d, body %s", rr.Code, rr.Body.String())
	}

	// The files row is gone...
	var fileRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE id=$1::uuid`, fileID).Scan(&fileRows); err != nil {
		t.Fatal(err)
	}
	if fileRows != 0 {
		t.Errorf("files row still present after delete")
	}
	// ...and so are the table, its values and its catalog row. Before C1
	// the catalog row went with the files row (FK cascade) while the
	// physical table stayed behind, unreachable.
	if tableExists(t, pool, table) {
		t.Errorf("tabular.%s survived the file delete — orphaned, and no catalog row is left to find it again", table)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tabular_column_values WHERE table_name=$1`, table).Scan(&values); err != nil {
		t.Fatal(err)
	}
	if values != 0 {
		t.Errorf("tabular_column_values rows remaining = %d, want 0", values)
	}
	entries, err := cat.ListByFile(ctx, fileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("catalog rows remaining = %d, want 0", len(entries))
	}
}
