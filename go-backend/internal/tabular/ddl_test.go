package tabular

import (
	"strings"
	"testing"
)

func TestBuildCreateTableSQL(t *testing.T) {
	cols := []ColumnSpec{
		{Original: "Name", Name: "name", Type: TypeText},
		{Original: "Revenue", Name: "revenue", Type: TypeFloat},
	}
	got := BuildCreateTableSQL("sheet_abc_0", cols, false)
	want := `CREATE TABLE tabular."sheet_abc_0" ("name" text, "revenue" double precision)`
	if got != want {
		t.Fatalf("BuildCreateTableSQL:\n got=%q\nwant=%q", got, want)
	}
}

func TestBuildCreateTableSQLWithRowID(t *testing.T) {
	cols := []ColumnSpec{
		{Name: "name", Type: TypeText},
		{Name: "revenue", Type: TypeFloat},
	}
	got := BuildCreateTableSQL("sheet_abc_0", cols, true)
	want := `CREATE TABLE tabular."sheet_abc_0" ("_rowid" bigint, "name" text, "revenue" double precision)`
	if got != want {
		t.Fatalf("with rowid:\n got=%q\nwant=%q", got, want)
	}
	// Without rowid, output is unchanged from Phase 1.
	got2 := BuildCreateTableSQL("sheet_abc_0", cols, false)
	want2 := `CREATE TABLE tabular."sheet_abc_0" ("name" text, "revenue" double precision)`
	if got2 != want2 {
		t.Fatalf("without rowid:\n got=%q\nwant=%q", got2, want2)
	}
}

func TestBuildRowChunkContent(t *testing.T) {
	cols := []ColumnSpec{
		{Name: "id", Type: TypeBigint},
		{Name: "notes", Original: "Notes", Type: TypeText, Embedded: true},
		{Name: "resolution", Original: "Resolution", Type: TypeText, Embedded: true},
	}
	row := []string{"42", "latency spike at peak", "added read replicas"}
	got, ok := BuildRowChunkContent("sheet_abc_0", 42, cols, row)
	if !ok {
		t.Fatal("expected a row-chunk for a row with flagged content")
	}
	for _, want := range []string{"[tabular.sheet_abc_0 row 42]", "Notes: latency spike at peak", "Resolution: added read replicas"} {
		if !strings.Contains(got, want) {
			t.Fatalf("row-chunk missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "42\n") && strings.Contains(got, "id:") {
		t.Fatal("non-flagged columns must not appear in the row-chunk")
	}
	// A row with no content in any flagged column produces nothing.
	if _, ok := BuildRowChunkContent("sheet_abc_0", 7, cols, []string{"7", "", ""}); ok {
		t.Fatal("empty flagged columns must produce no row-chunk")
	}
}

func TestCoerceValue(t *testing.T) {
	// Returns (value, ok). ok=false means the cell could not be coerced and
	// should be stored NULL with a coercion-stat bump.
	if v, ok := coerceValue("42", TypeBigint); !ok || v.(int64) != 42 {
		t.Fatalf("bigint coerce: got %v ok=%v", v, ok)
	}
	if _, ok := coerceValue("x", TypeBigint); ok {
		t.Fatalf("bigint coerce of 'x' should fail")
	}
	if v, ok := coerceValue("", TypeFloat); !ok || v != nil {
		t.Fatalf("empty should coerce to NULL, got %v ok=%v", v, ok)
	}
	if v, ok := coerceValue("hello", TypeText); !ok || v.(string) != "hello" {
		t.Fatalf("text coerce: got %v ok=%v", v, ok)
	}
}
