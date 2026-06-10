package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/mcp"
)

// SQLQueryArgs documents the sql_query tool input. The query is
// validated by string-level rules (denylisted keywords, allowlisted
// tables, single-statement, no comments) and then executed verbatim
// against the read-only connection pool. The string-level validation
// is defence-in-depth; the REAL security boundary is the DB role
// the connection pool authenticates as — operators MUST configure
// that role with SELECT-only grants on the allowlisted tables.
type SQLQueryArgs struct {
	Query string `json:"query"`
}

const sqlQueryInputSchema = `{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query": { "type": "string", "description": "Read-only SQL SELECT statement. Single statement, no comments, only allowlisted tables." }
  }
}`

// allowedTablesDefault is the minimal safe set the tool exposes by
// default. Operators can extend via sql_query_allowed_tables (Phase B
// site_config; not yet wired — operators that need more tables today
// edit this slice). The default omits `knowledge_bases` because its
// `system_prompt` column carries operator-authored prompts that may
// contain sensitive policy text.
var allowedTablesDefault = map[string]bool{
	"messages":        true,
	"chats":           true,
	"files":           true,
	"agent_decisions": true,
}

// deniedKeywords is the explicit blacklist for whole-word DDL/DML
// keywords. Word-boundary matched (?i)\bX\b so column names
// containing the substring don't false-positive (a column named
// "update_count" wouldn't collide with the UPDATE keyword).
//
// Conservative: the production read-only role would reject these
// at the database level too, but rejecting at the tool layer
// surfaces a clean error to the LLM instead of an opaque DB error.
var deniedKeywords = []string{
	"INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "TRUNCATE",
	"CREATE", "GRANT", "REVOKE", "EXECUTE", "COPY", "SET",
	"VACUUM", "ANALYZE", "REINDEX", "CLUSTER", "LOCK", "NOTIFY",
	"LISTEN", "UNLISTEN", "PREPARE", "DEALLOCATE",
}

var fromOrJoinRe = regexp.MustCompile(`(?i)\b(?:from|join|into|update)\s+(?:only\s+)?["]?([a-zA-Z_][a-zA-Z0-9_]*)["]?`)

// SQLExecutor is the abstraction the tool talks to. The production
// path passes a small adapter around pgxpool.Pool; tests pass a
// fake. Keeps the pgx import out of the test surface.
type SQLExecutor interface {
	Execute(ctx context.Context, query string, rowCap, byteCap int) (cols []string, rows []map[string]any, truncated bool, err error)
}

// pgxPoolExecutor wraps pgxpool.Pool with the row-cap + byte-cap
// + JSON-rendering logic the tool needs. Production-only; tests
// inject their own SQLExecutor.
type pgxPoolExecutor struct{ pool *pgxpool.Pool }

func (e *pgxPoolExecutor) Execute(ctx context.Context, query string, rowCap, byteCap int) ([]string, []map[string]any, bool, error) {
	rs, err := e.pool.Query(ctx, query)
	if err != nil {
		return nil, nil, false, err
	}
	defer rs.Close()

	cols := make([]string, 0)
	for _, fd := range rs.FieldDescriptions() {
		cols = append(cols, string(fd.Name))
	}

	out := make([]map[string]any, 0, rowCap)
	var bytesAccum int
	truncated := false
	for rs.Next() {
		if len(out) >= rowCap {
			truncated = true
			break
		}
		vals, err := rs.Values()
		if err != nil {
			return nil, nil, false, err
		}
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			if i < len(vals) {
				row[name] = vals[i]
			}
		}
		rowBytes, _ := json.Marshal(row)
		if bytesAccum+len(rowBytes) > byteCap {
			truncated = true
			break
		}
		bytesAccum += len(rowBytes)
		out = append(out, row)
	}
	if err := rs.Err(); err != nil {
		return nil, nil, false, err
	}
	return cols, out, truncated, nil
}

// Security caps for the sql_query tool. Promoted to package scope so the
// security-relevant numbers are visible at a glance during review and aren't
// buried inside the handler body.
//
//   - sqlQueryRowCap — truncate results past this row count, with a Meta note;
//     the LLM should narrow its WHERE if it would have returned more.
//   - sqlQueryByteCap — total result payload cap; truncate on the row that
//     would push the payload past this limit.
const (
	sqlQueryRowCap  = 100
	sqlQueryByteCap = 64 * 1024
)

// NewSQLQuery builds the sql_query tool. Caps are sqlQueryRowCap rows /
// sqlQueryByteCap bytes (see constants above).
//
// pool MUST authenticate as a read-only Postgres role. The string
// validator is defence-in-depth — the role's GRANTs are the
// authoritative boundary.
func NewSQLQuery(pool *pgxpool.Pool) mcp.Tool {
	return newSQLQueryWithExecutor(&pgxPoolExecutor{pool: pool})
}

// NewSQLQueryUnconfigured returns the sql_query tool in a disabled state.
// It is registered when no dedicated read-only pool is configured
// (JUSTRAG_DB_URL_READONLY unset or unreachable) so the tool never silently
// runs against the main read/write pool. Every call returns a clear
// configuration error instead — surfaced to the planner/LLM so the failure
// is diagnosable rather than a missing-tool mystery.
func NewSQLQueryUnconfigured() mcp.Tool {
	return mcp.Tool{
		Name:        "sql_query",
		Description: "Run a read-only SQL SELECT against allowlisted JustRAG tables (messages, chats, files, agent_decisions). Currently DISABLED: no read-only database role is configured.",
		InputSchema: json.RawMessage(sqlQueryInputSchema),
		Handler: mcp.ToolHandlerFunc(func(_ context.Context, _ json.RawMessage) (mcp.ToolResult, error) {
			return mcp.ToolResult{}, fmt.Errorf("sql_query: tool not configured; set JUSTRAG_DB_URL_READONLY to a SELECT-only database role to enable it")
		}),
	}
}

// newSQLQueryWithExecutor is the test seam. Production callers use
// NewSQLQuery; tests inject a fake SQLExecutor.
func newSQLQueryWithExecutor(exec SQLExecutor) mcp.Tool {
	return mcp.Tool{
		Name:        "sql_query",
		Description: "Run a read-only SQL SELECT against allowlisted JustRAG tables (messages, chats, files, agent_decisions). Use for meta-questions: 'when was X uploaded', 'how many messages this week', 'most-cited file last month'. Returns up to 100 rows.",
		InputSchema: json.RawMessage(sqlQueryInputSchema),
		Handler:     mcp.ToolHandlerFunc(sqlQueryHandler(exec)),
	}
}

func sqlQueryHandler(exec SQLExecutor) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		var args SQLQueryArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("sql_query: parse args: %w", err)
		}
		if args.Query == "" {
			return mcp.ToolResult{}, fmt.Errorf("sql_query: query is required")
		}
		if err := validateSQLQuery(args.Query, allowedTablesDefault); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("sql_query: %w", err)
		}
		if exec == nil {
			return mcp.ToolResult{}, fmt.Errorf("sql_query: no executor configured")
		}

		cols, rows, truncated, err := exec.Execute(ctx, args.Query, sqlQueryRowCap, sqlQueryByteCap)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("sql_query: %w", err)
		}
		structured, _ := json.Marshal(map[string]any{
			"rows":      rows,
			"columns":   cols,
			"row_count": len(rows),
			"truncated": truncated,
		})
		return mcp.ToolResult{
			Structured: structured,
			Meta: map[string]any{
				"row_count": len(rows),
				"truncated": truncated,
				"row_cap":   sqlQueryRowCap,
				"byte_cap":  sqlQueryByteCap,
			},
		}, nil
	}
}

// validateReadOnlyShape applies the format rules shared by sql_query and
// table_query: SELECT-only, single statement, no comments, no DDL/DML
// keywords. Table-allowlist enforcement is the caller's responsibility (the
// two tools resolve their allowlists differently).
func validateReadOnlyShape(q string) error {
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		return fmt.Errorf("empty query")
	}
	upper := strings.ToUpper(trimmed)
	if !strings.HasPrefix(upper, "SELECT ") && !strings.HasPrefix(upper, "SELECT\n") && !strings.HasPrefix(upper, "SELECT\t") {
		return fmt.Errorf("query must start with SELECT")
	}
	if strings.Contains(strings.TrimRight(trimmed, "; \n\t"), ";") {
		return fmt.Errorf("only one statement allowed (no internal `;`)")
	}
	if strings.Contains(trimmed, "--") || strings.Contains(trimmed, "/*") {
		return fmt.Errorf("comments are not allowed")
	}
	for _, kw := range deniedKeywords {
		if regexp.MustCompile(`(?i)\b` + kw + `\b`).MatchString(trimmed) {
			return fmt.Errorf("disallowed keyword: %s", kw)
		}
	}
	return nil
}

// fromClauseTerminators are the keywords that end a FROM clause's table
// list when they appear at the clause's own parenthesis depth. JOIN/ON/
// USING are deliberately absent — they continue the table-reference
// context, and any commas they legitimately contain (USING (a, b), row
// comparisons in ON) sit inside parentheses at a deeper depth.
var fromClauseTerminators = map[string]bool{
	"where": true, "group": true, "having": true, "order": true,
	"limit": true, "offset": true, "fetch": true, "for": true,
	"union": true, "except": true, "intersect": true, "window": true,
}

// rejectFromListComma rejects comma-separated table lists in FROM clauses
// (`FROM a, b`). fromOrJoinRe validates only the identifier immediately
// after FROM/JOIN, so a comma-join would smuggle a non-allowlisted second
// table past the allowlist — including via a derived table:
// `FROM (SELECT … FROM messages) m, knowledge_bases kb`. Rather than try
// to extract every comma-joined table name, the syntax is rejected
// outright; the error reaches the LLM, which rewrites with an explicit
// JOIN (equivalent semantics, and the JOIN keyword re-anchors the regex).
//
// Mechanism: a single-pass scan tracking parenthesis depth. FROM at depth
// d opens a clause region at d; the region closes when depth drops below d
// or a terminator keyword appears at d. A comma at the region's own depth
// is a comma-join. Commas at deeper depth (function args, IN lists,
// subquery select lists) and commas outside any FROM region (select list,
// GROUP/ORDER BY) are untouched. Single-quoted literals and double-quoted
// identifiers are skipped so their contents can't corrupt depth tracking;
// comment markers are already rejected by validateReadOnlyShape.
func rejectFromListComma(q string) error {
	isWordByte := func(c byte) bool {
		return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
	}
	depth := 0
	var fromStack []int // depths with an open FROM table list
	for i := 0; i < len(q); {
		switch c := q[i]; {
		case c == '\'': // string literal; '' is an escaped quote
			i++
			for i < len(q) {
				if q[i] == '\'' {
					if i+1 < len(q) && q[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '"': // quoted identifier
			i++
			for i < len(q) && q[i] != '"' {
				i++
			}
			if i < len(q) {
				i++
			}
		case c == '(':
			depth++
			i++
		case c == ')':
			depth--
			for len(fromStack) > 0 && fromStack[len(fromStack)-1] > depth {
				fromStack = fromStack[:len(fromStack)-1]
			}
			i++
		case c == ',':
			if len(fromStack) > 0 && fromStack[len(fromStack)-1] == depth {
				return fmt.Errorf("comma-separated table lists in FROM are not allowed; use an explicit JOIN ... ON instead")
			}
			i++
		case isWordByte(c):
			j := i
			for j < len(q) && isWordByte(q[j]) {
				j++
			}
			switch word := strings.ToLower(q[i:j]); {
			case word == "from":
				if len(fromStack) == 0 || fromStack[len(fromStack)-1] != depth {
					fromStack = append(fromStack, depth)
				}
			case fromClauseTerminators[word]:
				if len(fromStack) > 0 && fromStack[len(fromStack)-1] == depth {
					fromStack = fromStack[:len(fromStack)-1]
				}
			}
			i = j
		default:
			i++
		}
	}
	return nil
}

// validateSQLQuery applies the string-level allowlist rules.
//  1. Must start with SELECT (case-insensitive, after whitespace).
//  2. No `;` outside trailing whitespace — single statement only.
//  3. No `--` or `/*` comment markers (would let the LLM hide
//     denylisted keywords inside a comment).
//  4. No denylisted DDL/DML keywords (whole-word match).
//  5. No comma-separated FROM lists (every table must be introduced by
//     FROM or JOIN so rule 6 sees it).
//  6. Every FROM/JOIN target must be in the allowlist.
func validateSQLQuery(q string, allow map[string]bool) error {
	if err := validateReadOnlyShape(q); err != nil {
		return err
	}
	if err := rejectFromListComma(q); err != nil {
		return err
	}
	matches := fromOrJoinRe.FindAllStringSubmatch(strings.TrimSpace(q), -1)
	if len(matches) == 0 {
		return fmt.Errorf("no FROM clause detected; SELECT must reference an allowlisted table")
	}
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		table := strings.ToLower(m[1])
		if !allow[table] {
			allowed := make([]string, 0, len(allow))
			for k := range allow {
				allowed = append(allowed, k)
			}
			return fmt.Errorf("table %q not in allowlist (allowed: %s)", m[1], strings.Join(allowed, ", "))
		}
	}
	return nil
}
