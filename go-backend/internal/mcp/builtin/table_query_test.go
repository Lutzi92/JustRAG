package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateTableQueryScoping(t *testing.T) {
	allow := map[string]bool{"tabular.sheet_abc_0": true}
	// In-scope query passes.
	if err := validateTableQuery(`SELECT SUM(revenue) FROM tabular.sheet_abc_0`, allow); err != nil {
		t.Fatalf("in-scope query rejected: %v", err)
	}
	// Cross-tenant table rejected.
	if err := validateTableQuery(`SELECT * FROM tabular.sheet_other_0`, allow); err == nil {
		t.Fatal("cross-tenant table must be rejected")
	}
	// App table rejected (only tabular.* allowed for this tool).
	if err := validateTableQuery(`SELECT * FROM messages`, allow); err == nil {
		t.Fatal("non-tabular table must be rejected")
	}
	// Non-SELECT rejected.
	if err := validateTableQuery(`DROP TABLE tabular.sheet_abc_0`, allow); err == nil {
		t.Fatal("DROP must be rejected")
	}
}

// TestValidateTableQueryBypass covers cross-tenant smuggling that a naive
// FROM/JOIN-only check would miss. The read-only role can read all of
// tabular.*, so these MUST be rejected by the tool's allowlist.
func TestValidateTableQueryBypass(t *testing.T) {
	allow := map[string]bool{"tabular.sheet_abc_0": true}
	// Comma-join smuggling another KB's table.
	if err := validateTableQuery(`SELECT a.x FROM tabular.sheet_abc_0 a, tabular.sheet_other_0 b`, allow); err == nil {
		t.Fatal("comma-join to non-allowlisted tabular table must be rejected")
	}
	// Subquery referencing another KB's table.
	if err := validateTableQuery(`SELECT x FROM tabular.sheet_abc_0 WHERE y IN (SELECT z FROM tabular.sheet_other_0)`, allow); err == nil {
		t.Fatal("subquery to non-allowlisted tabular table must be rejected")
	}
	// Case-insensitive smuggling.
	if err := validateTableQuery(`SELECT * FROM tabular.sheet_abc_0, TABULAR.SHEET_OTHER_0`, allow); err == nil {
		t.Fatal("case-variant cross-tenant table must be rejected")
	}
	// Bare (unqualified) FROM target must be rejected — must be tabular.-qualified.
	if err := validateTableQuery(`SELECT * FROM sheet_abc_0`, allow); err == nil {
		t.Fatal("unqualified table target must be rejected")
	}
	// Quoted schema in a comma-join must NOT dodge the allowlist scanner.
	if err := validateTableQuery(`SELECT a.x FROM tabular.sheet_abc_0 a, "tabular".sheet_other_0 b`, allow); err == nil {
		t.Fatal("quoted-schema comma-join cross-tenant table must be rejected")
	}
	// Fully-quoted schema+table cross-tenant reference must be rejected.
	if err := validateTableQuery(`SELECT * FROM "tabular"."sheet_other_0"`, allow); err == nil {
		t.Fatal("fully-quoted cross-tenant table must be rejected")
	}
	// Quoted-but-allowlisted table still passes.
	if err := validateTableQuery(`SELECT COUNT(*) FROM "tabular"."sheet_abc_0"`, allow); err != nil {
		t.Fatalf("quoted allowlisted table wrongly rejected: %v", err)
	}
	// The allowlisted table still passes after hardening.
	if err := validateTableQuery(`SELECT COUNT(*) FROM tabular.sheet_abc_0 WHERE x > 0`, allow); err != nil {
		t.Fatalf("allowlisted query wrongly rejected: %v", err)
	}
}

type fakeCatalog struct{ entries []tableEntry }

func (f fakeCatalog) listForKB(_ context.Context, _ string) ([]tableEntry, error) {
	return f.entries, nil
}

func TestTableQueryRequiresKBID(t *testing.T) {
	tool := newTableQueryWithDeps(fakeCatalog{}, nil, alwaysEnabled)
	_, err := tool.Handler.Invoke(context.Background(), json.RawMessage(`{"sql":"SELECT 1 FROM tabular.x"}`))
	if err == nil || !strings.Contains(err.Error(), "kb_id") {
		t.Fatalf("expected kb_id-missing error, got %v", err)
	}
}

func TestTableQueryDisabled(t *testing.T) {
	tool := newTableQueryWithDeps(fakeCatalog{}, nil, func(context.Context) bool { return false })
	_, err := tool.Handler.Invoke(context.Background(), json.RawMessage(`{"kb_id":"k","sql":"SELECT 1 FROM tabular.x"}`))
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
}

func TestTableQueryDiscovery(t *testing.T) {
	cat := fakeCatalog{entries: []tableEntry{{
		TableName: "tabular.sheet_abc_0", SheetName: "Q1", FileName: "sales.csv",
		Columns: []tableColumn{{Name: "revenue", Type: "double precision", Original: "Revenue"}}, RowCount: 5,
	}}}
	tool := newTableQueryWithDeps(cat, nil, alwaysEnabled)
	res, err := tool.Handler.Invoke(context.Background(), json.RawMessage(`{"kb_id":"k","describe":true}`))
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if !strings.Contains(string(res.Structured), "tabular.sheet_abc_0") {
		t.Fatalf("discovery missing table name: %s", res.Structured)
	}
}

func alwaysEnabled(context.Context) bool { return true }
