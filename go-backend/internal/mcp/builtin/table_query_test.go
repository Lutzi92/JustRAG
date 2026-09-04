package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/tabular/sqlexec"
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

// TestTableQueryDiscoveryDescribesColumnRoleAndDescription pins that the
// Phase-2 profiler's ColumnSpec.Role/Description reach the describe output
// (tableColumn.Role/Description), not just Name/Type/Original.
func TestTableQueryDiscoveryDescribesColumnRoleAndDescription(t *testing.T) {
	cat := fakeCatalog{entries: []tableEntry{{
		TableName: "tabular.sheet_abc_0", SheetName: "Q1", FileName: "sales.csv",
		Columns: []tableColumn{{
			Name: "revenue", Type: "double precision", Original: "Revenue",
			Role: "measure", Description: "Quarterly revenue in EUR",
		}},
		RowCount: 5,
	}}}
	tool := newTableQueryWithDeps(cat, nil, alwaysEnabled)
	res, err := tool.Handler.Invoke(context.Background(), json.RawMessage(`{"kb_id":"k","describe":true}`))
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if !strings.Contains(string(res.Structured), `"description":"Quarterly revenue in EUR"`) {
		t.Fatalf("discovery missing column description: %s", res.Structured)
	}
	if !strings.Contains(string(res.Structured), `"role":"measure"`) {
		t.Fatalf("discovery missing column role: %s", res.Structured)
	}
}

func alwaysEnabled(context.Context) bool { return true }

// fakeExecutor is the sqlexec.Executor test seam. It records the exact SQL
// string it was asked to run (post sqlcheck.Validate wrapping) and returns a
// canned result unless err is set.
type fakeExecutor struct {
	lastSQL string
	result  *sqlexec.Result
	err     error
}

func (f *fakeExecutor) Execute(_ context.Context, sql string, _ sqlexec.Options) (*sqlexec.Result, error) {
	f.lastSQL = sql
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &sqlexec.Result{Columns: []string{"x"}, Rows: []map[string]any{{"x": "1"}}, RowCount: 1}, nil
}

// TestTableQueryDiscoveryDescribesShadowAndIDColumns pins that Phase-3
// discovery output surfaces the profiler's shadow-column linkage and the
// per-table id-role column names, not just Role/Description (already
// covered by TestTableQueryDiscoveryDescribesColumnRoleAndDescription).
func TestTableQueryDiscoveryDescribesShadowAndIDColumns(t *testing.T) {
	cat := fakeCatalog{entries: []tableEntry{{
		TableName: "tabular.sheet_x_0", SheetName: "Gebaeude", FileName: "buildings.xlsx",
		Columns: []tableColumn{
			{Name: "gebaeude", Type: "text", Original: "Gebaeude", Role: "id"},
			{Name: "baujahr", Type: "text", Original: "Baujahr", Role: "measure"},
			{Name: "baujahr_num", Type: "numeric", Original: "Baujahr", Role: "measure", ShadowOf: "baujahr"},
		},
		IDColumns: []string{"gebaeude"},
		RowCount:  3,
	}}}
	tool := newTableQueryWithDeps(cat, nil, alwaysEnabled)
	res, err := tool.Handler.Invoke(context.Background(), json.RawMessage(`{"kb_id":"k","describe":true}`))
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if !strings.Contains(string(res.Structured), `"shadow_of":"baujahr"`) {
		t.Fatalf("discovery missing shadow_of: %s", res.Structured)
	}
	if !strings.Contains(string(res.Structured), `"id_columns":["gebaeude"]`) {
		t.Fatalf("discovery missing id_columns: %s", res.Structured)
	}
}

// TestTableQueryRejectsForeignTable pins that the AST validator
// (sqlcheck.Validate), not just the legacy regex, rejects a table that
// isn't in this KB's catalog — with an error the LLM can act on.
func TestTableQueryRejectsForeignTable(t *testing.T) {
	cat := fakeCatalog{entries: []tableEntry{{
		TableName: "tabular.sheet_abc_0", SheetName: "Q1", FileName: "sales.csv", RowCount: 5,
	}}}
	tool := newTableQueryWithDeps(cat, &fakeExecutor{}, alwaysEnabled)
	argsJSON, _ := json.Marshal(map[string]any{"kb_id": "k", "sql": `SELECT * FROM tabular."other"`})
	_, err := tool.Handler.Invoke(context.Background(), argsJSON)
	if err == nil || !strings.Contains(err.Error(), "not a table of this knowledge base") {
		t.Fatalf("expected rejection mentioning 'not a table of this knowledge base', got %v", err)
	}
}

// TestTableQueryWrapsLimitTo200 pins that a router/LLM-supplied LIMIT above
// the cap reaches the executor already wrapped down to LIMIT 200 by
// sqlcheck.Validate, rather than being trusted verbatim.
func TestTableQueryWrapsLimitTo200(t *testing.T) {
	cat := fakeCatalog{entries: []tableEntry{{
		TableName: "tabular.sheet_abc_0", SheetName: "Q1", FileName: "sales.csv", RowCount: 5,
	}}}
	fe := &fakeExecutor{}
	tool := newTableQueryWithDeps(cat, fe, alwaysEnabled)
	argsJSON, _ := json.Marshal(map[string]any{"kb_id": "k", "sql": "SELECT * FROM tabular.sheet_abc_0 LIMIT 5000"})
	if _, err := tool.Handler.Invoke(context.Background(), argsJSON); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(fe.lastSQL, "LIMIT 200") {
		t.Fatalf("expected executor to receive SQL wrapped with LIMIT 200, got %q", fe.lastSQL)
	}
}

// TestTableQueryResultIncludesSource pins that a successful query result
// carries the source file/sheet/table the rows came from (R27 / task-6),
// not just rows/columns/row_count/truncated.
func TestTableQueryResultIncludesSource(t *testing.T) {
	cat := fakeCatalog{entries: []tableEntry{{
		TableName: "tabular.sheet_abc_0", SheetName: "Q1", FileName: "sales.csv", FileID: "f1", RowCount: 5,
	}}}
	fe := &fakeExecutor{result: &sqlexec.Result{
		Columns: []string{"revenue"}, Rows: []map[string]any{{"revenue": "100"}}, RowCount: 1,
	}}
	tool := newTableQueryWithDeps(cat, fe, alwaysEnabled)
	argsJSON, _ := json.Marshal(map[string]any{"kb_id": "k", "sql": "SELECT revenue FROM tabular.sheet_abc_0"})
	res, err := tool.Handler.Invoke(context.Background(), argsJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed struct {
		Source []struct {
			FileName string `json:"file_name"`
		} `json:"source"`
	}
	if err := json.Unmarshal(res.Structured, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(parsed.Source) == 0 || parsed.Source[0].FileName != "sales.csv" {
		t.Fatalf("expected source[0].file_name = sales.csv, got %+v", parsed.Source)
	}
}

// TestTableQueryRejectsPgReadFile pins that the AST validator's function
// allowlist — not just the regex/keyword denylist — rejects a
// non-allowlisted function call. Mutation coverage: skipping
// sqlcheck.Validate would let this through, since pg_read_file is neither a
// denied keyword nor a FROM/JOIN target the legacy regex inspects.
func TestTableQueryRejectsPgReadFile(t *testing.T) {
	cat := fakeCatalog{entries: []tableEntry{{
		TableName: "tabular.sheet_abc_0", SheetName: "Q1", FileName: "sales.csv", RowCount: 5,
	}}}
	tool := newTableQueryWithDeps(cat, &fakeExecutor{}, alwaysEnabled)
	argsJSON, _ := json.Marshal(map[string]any{
		"kb_id": "k", "sql": "SELECT pg_read_file('/etc/passwd') FROM tabular.sheet_abc_0",
	})
	_, err := tool.Handler.Invoke(context.Background(), argsJSON)
	if err == nil {
		t.Fatal("expected pg_read_file to be rejected")
	}
}
