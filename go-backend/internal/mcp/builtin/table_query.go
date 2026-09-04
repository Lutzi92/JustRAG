package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/tabular/sqlcheck"
	"github.com/justrag/go-backend/internal/tabular/sqlexec"
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
    "sql": { "type": "string", "description": "Read-only SQL SELECT against tabular.* tables returned by describe. Single statement, no comments. Numeric values are returned as decimal strings." }
  }
}`

// Security caps for the table_query tool. Timeout / RowCap are passed
// straight through to sqlexec.Executor; maxLimit for sqlcheck.Validate uses
// the same row cap so a router/LLM-supplied LIMIT above it is wrapped down
// rather than trusted.
const (
	tableQueryTimeout = 5 * time.Second
	tableQueryRowCap  = 200
)

// tableColumn / tableEntry are the discovery-shaped projections the tool
// exposes to the LLM (decoupled from tabular.CatalogEntry so the test seam
// doesn't import a pool).
type tableColumn struct {
	Name        string `json:"column"`
	Type        string `json:"type"`
	Original    string `json:"header"`
	Role        string `json:"role,omitempty"`
	Description string `json:"description,omitempty"`
	// ShadowOf names the primary column this one shadows (e.g. "baujahr" for
	// the coerced "baujahr_num" numeric-shadow column). Phase-2 profiler
	// field; empty for Phase-1 rows and for columns that aren't a shadow.
	ShadowOf string `json:"shadow_of,omitempty"`
}

type tableEntry struct {
	TableName string        `json:"table"`
	SheetName string        `json:"sheet"`
	FileName  string        `json:"file"`
	RowCount  int64         `json:"row_count"`
	Columns   []tableColumn `json:"columns"`
	// IDColumns names the columns with role "id" — the columns safe to join
	// on across tables (Rule: "join only on id-role columns" in the tool
	// description).
	IDColumns []string `json:"id_columns,omitempty"`
	// Hidden/SheetKind are the Phase-2 profiler's region classification: a
	// hidden sheet or a non-"table" region (notes, pivots) the LLM should
	// generally not query for tabular aggregation.
	Hidden    bool   `json:"hidden,omitempty"`
	SheetKind string `json:"sheet_kind,omitempty"`
	// FileID is carried for source attribution on query results; it is
	// deliberately NOT part of the describe JSON (describe already reports
	// FileName, and file_id is only useful alongside the actual rows).
	FileID string `json:"-"`
}

// tableQuerySource is one entry of a query result's "source" array: the
// file/sheet/table the returned rows came from. Table is always populated;
// the rest are empty when the referenced table isn't in the KB's catalog
// (shouldn't happen post sqlcheck.Validate, but the tool degrades to
// table-only attribution rather than failing the whole call).
type tableQuerySource struct {
	FileID   string `json:"file_id,omitempty"`
	FileName string `json:"file_name,omitempty"`
	Sheet    string `json:"sheet,omitempty"`
	Table    string `json:"table"`
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
		var idCols []string
		for i, c := range e.Columns {
			cols[i] = tableColumn{
				Name: c.Name, Type: string(c.Type), Original: c.Original,
				Role: c.Role, Description: c.Description, ShadowOf: c.ShadowOf,
			}
			if c.Role == "id" {
				idCols = append(idCols, c.Name)
			}
		}
		out = append(out, tableEntry{
			TableName: tabular.TabularSchema + "." + e.TableName,
			SheetName: e.SheetName, FileName: e.FileName, FileID: e.FileID, RowCount: e.RowCount, Columns: cols,
			IDColumns: idCols, Hidden: e.Hidden, SheetKind: e.SheetKind,
		})
	}
	return out, nil
}

// NewTableQuery builds the production tool. roPool backs catalog discovery
// (tabular_catalog reads); exec is the time-boxed read-only executor query
// SQL runs through (production: sqlexec.NewReadOnly(roPool), same
// SELECT-only role).
func NewTableQuery(roPool *pgxpool.Pool, exec sqlexec.Executor, enabled func(context.Context) bool) mcp.Tool {
	return newTableQueryWithDeps(pgCatalogReader{cat: tabular.NewCatalog(roPool)}, exec, enabled)
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

func newTableQueryWithDeps(cat catalogReader, exec sqlexec.Executor, enabled func(context.Context) bool) mcp.Tool {
	return mcp.Tool{
		Name: "table_query",
		Description: "Query uploaded spreadsheets with read-only SQL (exact lookups, SUM/AVG/COUNT, GROUP BY, filter/sort). " +
			"The system prompt's TABLES block lists every table with its columns, roles and stored values; call " +
			"{\"describe\": true} if you need the full column list. Rules: quote identifiers exactly as listed; text ID " +
			"columns are compared as strings; use a column's `_num` shadow for arithmetic; join only on id-role columns. " +
			"Every sheet has a synthetic `_rowid` column: to aggregate over rows found via search, read the table name and " +
			"the row range from the `[tabular.<table> rows <a>–<b>]` marker above a block of rows in kb_search results and " +
			"filter with `WHERE _rowid BETWEEN <a> AND <b>`. Returns up to 200 rows with the source file/sheet.",
		InputSchema: json.RawMessage(tableQueryInputSchema),
		Handler:     mcp.ToolHandlerFunc(tableQueryHandler(cat, exec, enabled)),
	}
}

func tableQueryHandler(cat catalogReader, exec sqlexec.Executor, enabled func(context.Context) bool) mcp.ToolHandlerFunc {
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

		// An empty catalog short-circuits BOTH discovery and execution mode:
		// there is nothing to describe, and there is no allowlisted table any
		// SQL could legally reference, so running the SQL through the
		// validator would only ever surface a confusing "no tabular table"
		// parser error instead of this same friendly message.
		if len(entries) == 0 {
			return mcp.ToolResult{
				Text: "No spreadsheet tables available in this knowledge base.",
				Meta: map[string]any{"table_count": 0},
			}, nil
		}

		// Discovery mode (explicit, or when no SQL provided).
		if args.Describe || strings.TrimSpace(args.SQL) == "" {
			structured, _ := json.Marshal(map[string]any{"tables": entries})
			return mcp.ToolResult{Structured: structured, Meta: map[string]any{"table_count": len(entries)}}, nil
		}

		// Execution mode: build the per-request allowlist from the catalog,
		// and an index back from table name to catalog entry for source
		// attribution once the query runs.
		allow := make(map[string]bool, len(entries))
		byTable := make(map[string]tableEntry, len(entries))
		for _, e := range entries {
			key := strings.ToLower(e.TableName)
			allow[key] = true
			byTable[key] = e
		}

		// Gate 1: AST validation. Parses the statement, checks every table
		// reference and function call against allowlists, and returns the
		// SQL to actually run — the original text when its own LIMIT is
		// within cap, otherwise wrapped down to LIMIT tableQueryRowCap. The
		// validator's own error is returned verbatim: it names the
		// offending relation/function so the LLM can repair its query.
		execSQL, info, err := sqlcheck.Validate(args.SQL, allow, tableQueryRowCap)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("table_query: %w", err)
		}
		// Gate 2 (defence-in-depth, on the ORIGINAL text): the legacy
		// regex/allowlist scan. Cheap, and catches comma-join / quoted-schema
		// smuggling shapes independently of the AST gate above.
		if err := validateTableQuery(args.SQL, allow); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("table_query: %w", err)
		}
		if exec == nil {
			return mcp.ToolResult{}, fmt.Errorf("table_query: no executor configured")
		}
		result, err := exec.Execute(ctx, execSQL, sqlexec.Options{Timeout: tableQueryTimeout, RowCap: tableQueryRowCap})
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("table_query: %w", err)
		}
		// I1: Executor.Truncated is only set on an (RowCap+1)-th row, but
		// Validate already wrapped the statement to `LIMIT tableQueryRowCap`
		// when the proposal's own LIMIT exceeded it — so a result that
		// exactly fills the cap never trips Truncated even though more rows
		// may exist. RowCount hitting the cap is the same signal.
		result.Truncated = result.Truncated || result.RowCount >= tableQueryRowCap

		sources := make([]tableQuerySource, 0, len(info.Tables))
		for _, t := range info.Tables {
			if e, ok := byTable[strings.ToLower(t)]; ok {
				sources = append(sources, tableQuerySource{
					FileID: e.FileID, FileName: e.FileName, Sheet: e.SheetName, Table: e.TableName,
				})
			} else {
				// Not expected post-Validate (every table reference is
				// allowlist-checked), but degrade to table-only attribution
				// rather than failing an otherwise-successful query.
				sources = append(sources, tableQuerySource{Table: t})
			}
		}

		structured, _ := json.Marshal(map[string]any{
			"rows": result.Rows, "columns": result.Columns, "row_count": result.RowCount,
			"truncated": result.Truncated, "source": sources, "sql": execSQL,
		})
		return mcp.ToolResult{
			Structured: structured,
			Meta:       map[string]any{"row_count": result.RowCount, "truncated": result.Truncated, "row_cap": tableQueryRowCap},
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

// cteFirstNameRe captures the alias of the first CTE declared by a WITH
// clause: WITH [RECURSIVE] <name> [(cols)] AS (.
var cteFirstNameRe = regexp.MustCompile(`(?i)\bwith\s+(?:recursive\s+)?([a-zA-Z_]\w*)\s*(?:\([^)]*\))?\s+as\s*\(`)

// cteNextNameRe captures each subsequent comma-separated CTE alias:
// , <name> [(cols)] AS (.
var cteNextNameRe = regexp.MustCompile(`(?i),\s*([a-zA-Z_]\w*)\s*(?:\([^)]*\))?\s+as\s*\(`)

// collectCTENames returns the lower-cased set of CTE aliases a WITH clause
// declares at the start of scan (already quote-stripped). R53: the regex
// second gate has no notion of CTEs otherwise, so a perfectly legal
// "WITH t AS (SELECT ... FROM tabular.x) SELECT * FROM t" was rejected with
// a misleading "must be schema-qualified" error — the outer "FROM t" refers
// to the CTE, not a real relation, and must be exempted from the
// tabular-prefix check (the tabularRefRe scan of tabular.<name> references
// is unaffected: a CTE alias never matches that pattern).
//
// Returns an empty set when the query has no WITH clause at all, so the
// subsequent-alias scan (which is otherwise just "comma, then <name> AS (")
// can only ever fire once a real WITH clause has been found — it must not
// degrade into exempting arbitrary bare identifiers.
func collectCTENames(scan string) map[string]bool {
	names := map[string]bool{}
	m := cteFirstNameRe.FindStringSubmatchIndex(scan)
	if m == nil {
		return names
	}
	names[strings.ToLower(scan[m[2]:m[3]])] = true
	for _, sm := range cteNextNameRe.FindAllStringSubmatch(scan[m[1]:], -1) {
		if len(sm) < 2 {
			continue
		}
		names[strings.ToLower(sm[1])] = true
	}
	return names
}

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
//
// This is the second gate, run on the original query text after
// sqlcheck.Validate's AST-based check (see tableQueryHandler): the two are
// intentionally independent implementations so one cannot silently regress
// past what the other still catches.
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
	cteNames := collectCTENames(scan)

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
		if cteNames[name] {
			// A CTE alias, not a real relation — exempt it from the
			// tabular-prefix check (R53). The tabularRefRe scan below is
			// unaffected: a bare alias never matches "tabular.<name>".
			continue
		}
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
