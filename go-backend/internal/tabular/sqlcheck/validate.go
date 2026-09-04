// Package sqlcheck validates router-generated SQL before it is executed
// against the per-KB tabular schema: single read-only SELECT (optionally
// WITH a CTE prefix), every table reference inside the tabular schema and
// on the caller's allowlist, every function call on a closed allowlist,
// and a LIMIT enforced by wrapping rather than trusting the router's text.
package sqlcheck

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
)

// DefaultFunctionAllowlist is the closed set of functions router SQL may
// call. Lower-case. Aggregates, math, string, date/time, window.
var DefaultFunctionAllowlist = map[string]bool{
	"count": true, "sum": true, "avg": true, "min": true, "max": true,
	"string_agg": true, "array_agg": true, "bool_and": true, "bool_or": true,
	"round": true, "abs": true, "ceil": true, "ceiling": true, "floor": true, "trunc": true,
	"coalesce": true, "nullif": true, "greatest": true, "least": true,
	"lower": true, "upper": true, "trim": true, "ltrim": true, "rtrim": true, "btrim": true,
	"length": true, "char_length": true, "substring": true, "substr": true, "replace": true,
	"concat": true, "concat_ws": true, "left": true, "right": true, "split_part": true,
	"position": true, "strpos": true, "initcap": true, "to_char": true, "to_number": true,
	"date_trunc": true, "extract": true, "date_part": true, "now": true, "current_date": true,
	"age": true, "to_date": true, "to_timestamp": true, "make_date": true,
	"row_number": true, "rank": true, "dense_rank": true, "lag": true, "lead": true,
	"cast": true,
}

// aggregateNames is the subset of DefaultFunctionAllowlist that makes a
// top-level select-list expression an aggregate.
var aggregateNames = map[string]bool{
	"count": true, "sum": true, "avg": true, "min": true, "max": true,
	"string_agg": true, "array_agg": true, "bool_and": true, "bool_or": true,
}

// Info is what the validator learned about an accepted statement.
type Info struct {
	Tables      []string // schema-qualified, lower-cased, as referenced (CTE names excluded)
	Functions   []string // lower-cased
	HasCTE      bool
	HasSubquery bool
	Limit       int  // 0 when absent
	IsAggregate bool // top-level select list contains an aggregate call
}

// ErrNotSelect is returned when the parsed statement is not a *tree.Select.
var ErrNotSelect = errors.New("sqlcheck: not a single SELECT statement")

// Validate parses sql as a single Postgres SELECT, checks every table
// reference against allowedTables (keys are "tabular.<name>", lower case)
// and every function against DefaultFunctionAllowlist, then returns the
// statement text that may be executed: the trimmed original when it has
// a LIMIT <= maxLimit, otherwise the original wrapped as
// SELECT * FROM (<original>) AS _validated LIMIT <maxLimit>.
func Validate(sql string, allowedTables map[string]bool, maxLimit int) (string, Info, error) {
	var info Info
	if maxLimit <= 0 {
		maxLimit = 200
	}
	trimmed := strings.TrimSpace(sql)
	trimmed = strings.TrimRight(trimmed, "; \n\t")
	if err := ReadOnlyShape(trimmed); err != nil {
		return "", info, err
	}
	stmts, err := parser.Parse(trimmed)
	if err != nil {
		return "", info, fmt.Errorf("sqlcheck: parse: %w", err)
	}
	if len(stmts) != 1 {
		return "", info, fmt.Errorf("sqlcheck: expected 1 statement, got %d", len(stmts))
	}
	sel, ok := stmts[0].AST.(*tree.Select)
	if !ok {
		return "", info, ErrNotSelect
	}

	cteNames := map[string]bool{}
	if sel.With != nil {
		info.HasCTE = true
		for _, c := range sel.With.CTEList {
			cteNames[strings.ToLower(string(c.Name.Alias))] = true
		}
	}

	tables := map[string]bool{}
	funcs := map[string]bool{}
	var verr error
	v := &visitor{Fn: func(node any) bool {
		switch n := node.(type) {
		case *tree.TableName:
			return checkTable(n, cteNames, allowedTables, tables, &verr)
		case tree.TableName:
			return checkTable(&n, cteNames, allowedTables, tables, &verr)
		case *tree.FuncExpr:
			fn := strings.ToLower(strings.ReplaceAll(n.Func.String(), `"`, ""))
			if !DefaultFunctionAllowlist[fn] {
				verr = fmt.Errorf("sqlcheck: function %q is not allowed", fn)
				return true
			}
			funcs[fn] = true
		case *tree.Subquery:
			info.HasSubquery = true
		case *tree.Limit:
			if n.Count != nil {
				if lit, ok := n.Count.(*tree.NumVal); ok {
					if i, err := strconv.Atoi(lit.OrigString()); err == nil {
						info.Limit = i
					}
				}
			}
		}
		return false
	}}
	v.walk([]tree.Statement{sel})
	if verr != nil {
		return "", info, verr
	}
	if len(tables) == 0 {
		return "", info, errors.New("sqlcheck: statement references no tabular table")
	}

	for t := range tables {
		info.Tables = append(info.Tables, t)
	}
	for f := range funcs {
		info.Functions = append(info.Functions, f)
	}
	sort.Strings(info.Tables)
	sort.Strings(info.Functions)

	if sc, ok := sel.Select.(*tree.SelectClause); ok {
		for _, e := range sc.Exprs {
			if fe, ok := e.Expr.(*tree.FuncExpr); ok && aggregateNames[strings.ToLower(strings.ReplaceAll(fe.Func.String(), `"`, ""))] {
				info.IsAggregate = true
			}
		}
	}

	if info.Limit > 0 && info.Limit <= maxLimit {
		return trimmed, info, nil
	}
	return fmt.Sprintf("SELECT * FROM (%s) AS _validated LIMIT %d", trimmed, maxLimit), info, nil
}

// checkTable validates a single table reference: CTE aliases are skipped
// (they aren't real relations), everything else must be schema-qualified
// under "tabular." and present in allowedTables. A zero-value TableName
// (e.g. surfaced via *tree.Order.Table when ordering by an expression, not
// an index) stringifies to "" and is skipped rather than rejected.
func checkTable(tn *tree.TableName, cteNames, allowedTables, tables map[string]bool, verr *error) bool {
	name := strings.ToLower(tn.String())
	name = strings.ReplaceAll(name, `"`, "")
	if name == "" {
		return false
	}
	if cteNames[name] {
		return false
	}
	if !strings.HasPrefix(name, "tabular.") {
		*verr = fmt.Errorf("sqlcheck: relation %q is outside the tabular schema", name)
		return true
	}
	if !allowedTables[name] {
		*verr = fmt.Errorf("sqlcheck: relation %q is not a table of this knowledge base", name)
		return true
	}
	tables[name] = true
	return false
}
