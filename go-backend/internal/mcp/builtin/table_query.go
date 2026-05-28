package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/tabular"
)

// TableQueryArgs is the documented arg shape. kb_id is injected by the
// orchestrator (never the LLM). With describe=true the tool returns the KB's
// available sheet schemas; otherwise it runs the provided read-only SELECT.
type TableQueryArgs struct {
	SQL      string `json:"sql,omitempty"`
	Describe bool   `json:"describe,omitempty"`
	KbID     string `json:"kb_id,omitempty"` // populated by the orchestrator
}

const tableQueryInputSchema = `{
  "type": "object",
  "properties": {
    "describe": { "type": "boolean", "description": "Set true to list available spreadsheet tables and their columns before querying." },
    "sql": { "type": "string", "description": "Read-only SQL SELECT against tabular.* tables returned by describe. Single statement, no comments." }
  }
}`

// tableColumn / tableEntry are the discovery-shaped projections the tool
// exposes to the LLM (decoupled from tabular.CatalogEntry so the test seam
// doesn't import a pool).
type tableColumn struct {
	Name     string `json:"column"`
	Type     string `json:"type"`
	Original string `json:"header"`
}

type tableEntry struct {
	TableName string        `json:"table"`
	SheetName string        `json:"sheet"`
	FileName  string        `json:"file"`
	RowCount  int64         `json:"row_count"`
	Columns   []tableColumn `json:"columns"`
}

// catalogReader is the tool's view of the catalog (test seam).
type catalogReader interface {
	listForKB(ctx context.Context, kbID string) ([]tableEntry, error)
}

// pgCatalogReader adapts tabular.Catalog (reading through the read-only pool).
type pgCatalogReader struct{ cat *tabular.Catalog }

func (r pgCatalogReader) listForKB(ctx context.Context, kbID string) ([]tableEntry, error) {
	entries, err := r.cat.ListByKB(ctx, kbID)
	if err != nil {
		return nil, err
	}
	out := make([]tableEntry, 0, len(entries))
	for _, e := range entries {
		cols := make([]tableColumn, len(e.Columns))
		for i, c := range e.Columns {
			cols[i] = tableColumn{Name: c.Name, Type: string(c.Type), Original: c.Original}
		}
		out = append(out, tableEntry{
			TableName: tabular.TabularSchema + "." + e.TableName,
			SheetName: e.SheetName, FileName: e.FileName, RowCount: e.RowCount, Columns: cols,
		})
	}
	return out, nil
}

// NewTableQuery builds the production tool against the read-only pool.
func NewTableQuery(roPool *pgxpool.Pool, enabled func(context.Context) bool) mcp.Tool {
	return newTableQueryWithDeps(pgCatalogReader{cat: tabular.NewCatalog(roPool)}, &pgxPoolExecutor{pool: roPool}, enabled)
}

// NewTableQueryUnconfigured returns the disabled stub (no read-only pool).
func NewTableQueryUnconfigured() mcp.Tool {
	return mcp.Tool{
		Name:        "table_query",
		Description: "Query uploaded spreadsheets with read-only SQL. Currently DISABLED: no read-only database role is configured.",
		InputSchema: json.RawMessage(tableQueryInputSchema),
		Handler: mcp.ToolHandlerFunc(func(context.Context, json.RawMessage) (mcp.ToolResult, error) {
			return mcp.ToolResult{}, fmt.Errorf("table_query: tool not configured; set JUSTRAG_DB_URL_READONLY to a SELECT-only database role to enable it")
		}),
	}
}

func newTableQueryWithDeps(cat catalogReader, exec SQLExecutor, enabled func(context.Context) bool) mcp.Tool {
	return mcp.Tool{
		Name: "table_query",
		Description: "Query large uploaded spreadsheets with read-only SQL (exact lookups, SUM/AVG/COUNT, GROUP BY, filter/sort). " +
			"Call with {\"describe\": true} first to list available tables and columns, then send a SELECT against a tabular.* table. " +
			"Each sheet also has a synthetic `_rowid` column: to aggregate over rows found via fuzzy search, read the ids from the " +
			"`[tabular.<table> row <id>]` headers in kb_search results and filter with `WHERE _rowid IN (...)`. Returns up to 100 rows.",
		InputSchema: json.RawMessage(tableQueryInputSchema),
		Handler:     mcp.ToolHandlerFunc(tableQueryHandler(cat, exec, enabled)),
	}
}

func tableQueryHandler(cat catalogReader, exec SQLExecutor, enabled func(context.Context) bool) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		if enabled != nil && !enabled(ctx) {
			return mcp.ToolResult{}, fmt.Errorf("table_query: disabled (set chat_tabular_query_enabled)")
		}
		var args TableQueryArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("table_query: parse args: %w", err)
		}
		if args.KbID == "" {
			return mcp.ToolResult{}, fmt.Errorf("table_query: kb_id was not injected by the orchestrator")
		}
		entries, err := cat.listForKB(ctx, args.KbID)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("table_query: catalog: %w", err)
		}

		// Discovery mode (explicit, or when no SQL provided).
		if args.Describe || strings.TrimSpace(args.SQL) == "" {
			structured, _ := json.Marshal(map[string]any{"tables": entries})
			if len(entries) == 0 {
				return mcp.ToolResult{
					Text: "No spreadsheet tables available in this knowledge base.",
					Meta: map[string]any{"table_count": 0},
				}, nil
			}
			return mcp.ToolResult{Structured: structured, Meta: map[string]any{"table_count": len(entries)}}, nil
		}

		// Execution mode: build the per-request allowlist from the catalog.
		allow := make(map[string]bool, len(entries))
		for _, e := range entries {
			allow[strings.ToLower(e.TableName)] = true
		}
		if err := validateTableQuery(args.SQL, allow); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("table_query: %w", err)
		}
		if exec == nil {
			return mcp.ToolResult{}, fmt.Errorf("table_query: no executor configured")
		}
		cols, rows, truncated, err := exec.Execute(ctx, args.SQL, sqlQueryRowCap, sqlQueryByteCap)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("table_query: %w", err)
		}
		structured, _ := json.Marshal(map[string]any{
			"rows": rows, "columns": cols, "row_count": len(rows), "truncated": truncated,
		})
		return mcp.ToolResult{
			Structured: structured,
			Meta:       map[string]any{"row_count": len(rows), "truncated": truncated, "row_cap": sqlQueryRowCap},
		}, nil
	}
}

// tableQueryFromJoinRe captures FROM/JOIN targets including an optional schema
// qualifier (tabular.sheet_x). Quotes and surrounding whitespace are stripped
// by the caller.
var tableQueryFromJoinRe = regexp.MustCompile(`(?i)\b(?:from|join)\s+(?:only\s+)?("?[a-zA-Z_]\w*"?(?:\s*\.\s*"?[a-zA-Z_]\w*"?)?)`)

// tabularRefRe captures EVERY tabular.<name> reference anywhere in the query
// (not just FROM/JOIN anchors): comma-joins, subqueries, etc. The captured
// group is the bare table name after the schema qualifier.
var tabularRefRe = regexp.MustCompile(`(?i)\btabular\s*\.\s*"?([a-zA-Z_]\w*)"?`)

// validateTableQuery enforces the shared read-only shape plus a tabular-table
// allowlist. allow keys are fully-qualified lowercased names (tabular.<table>).
//
// Security note: the read-only DB role is granted SELECT on the ENTIRE tabular
// schema, so the per-request allowlist is the ONLY thing isolating one KB's
// sheets from another's. The FROM/JOIN regex alone is insufficient — it misses
// comma-joins and subqueries — so we additionally scan for every tabular.<name>
// reference in the query and reject any that isn't allowlisted. Bare
// (unqualified) FROM targets are rejected outright; they also can't resolve to
// a tabular table because the read-only role's search_path must not include the
// tabular schema (operator prerequisite).
func validateTableQuery(q string, allow map[string]bool) error {
	if err := validateReadOnlyShape(q); err != nil {
		return err
	}
	// Strip double-quote identifier delimiters before scanning. In SQL, double
	// quotes only delimit identifiers (string literals use single quotes), and
	// Postgres treats `"tabular"` identically to `tabular`. Without this, a
	// quoted schema in a non-FROM position (e.g. comma-join `, "tabular".other`)
	// would dodge both regexes below and reach another KB's table — a
	// cross-tenant leak. Quote-stripping makes every table reference scannable.
	scan := strings.ReplaceAll(strings.TrimSpace(q), `"`, "")

	fromMatches := tableQueryFromJoinRe.FindAllStringSubmatch(scan, -1)
	if len(fromMatches) == 0 {
		return fmt.Errorf("no FROM clause detected; SELECT must reference a tabular.* table from describe")
	}
	prefix := tabular.TabularSchema + "."
	for _, m := range fromMatches {
		if len(m) < 2 {
			continue
		}
		name := normalizeQualifiedName(m[1])
		if !strings.HasPrefix(name, prefix) {
			return fmt.Errorf("table %q must be schema-qualified as %s<name> from describe", m[1], prefix)
		}
		if !allow[name] {
			return fmt.Errorf("table %q not available in this knowledge base; call describe first", m[1])
		}
	}

	// Defense-in-depth: every tabular.<name> reference (comma-join, subquery,
	// anywhere) must be allowlisted, since the DB role can read all of tabular.*.
	for _, m := range tabularRefRe.FindAllStringSubmatch(scan, -1) {
		if len(m) < 2 {
			continue
		}
		name := prefix + strings.ToLower(m[1])
		if !allow[name] {
			return fmt.Errorf("table %q not available in this knowledge base; call describe first", prefix+m[1])
		}
	}
	return nil
}

func normalizeQualifiedName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, `"`, "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}
