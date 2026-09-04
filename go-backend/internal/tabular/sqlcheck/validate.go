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
	"strings"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/types"
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
	Tables      []string // "tabular.<name>", per the parser's own identifier folding (CTE names excluded)
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

	// CTE aliases are matched by identity against the parser's own folded
	// Name (unquoted parts already lower-cased, quoted parts preserved
	// verbatim) — no extra case-folding here, so a quoted mixed-case CTE
	// alias behaves the same way Postgres itself would treat it (I3).
	cteNames := map[string]bool{}
	if sel.With != nil {
		info.HasCTE = true
		for _, c := range sel.With.CTEList {
			cteNames[string(c.Name.Alias)] = true
		}
	}

	// R48(d): a qualified column/star reference (`alias.col`, `schema.table.col`,
	// `schema.table.*`) names a table too, and that table must be one this
	// query actually declared — collect the set of acceptable single-token
	// qualifiers (every AliasedTableExpr alias, every CTE name, and every
	// bare table object name, aliased or not) with a separate, best-effort
	// collector pass before the real validation walk. Best-effort because a
	// query that trips the fail-closed default gets rejected regardless of
	// how much of the tree the collector reached (see collectAliases).
	declaredAliases := collectAliases(sel, cteNames)

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
		case *tree.CastExpr:
			if err := checkCastType(n.Type); err != nil {
				verr = err
				return true
			}
		case *tree.AnnotateTypeExpr:
			// R55: the ::: annotate-type syntax reaches the same target-type
			// field shape as a cast (ResolvableTypeReference) and is subject
			// to the exact same audit requirement.
			if err := checkCastType(n.Type); err != nil {
				verr = err
				return true
			}
		case *tree.UnresolvedName:
			if err := checkQualifiedName(n, declaredAliases, allowedTables); err != nil {
				verr = err
				return true
			}
		case *tree.Subquery:
			info.HasSubquery = true
		case disallowed:
			verr = fmt.Errorf("sqlcheck: %s", n.reason)
			return true
		case unrecognized:
			// R38 fail-closed: the walker's structural switch has no case
			// for this concrete AST type, so it cannot vouch that no table
			// or function reference is hiding inside it. Reject rather
			// than silently skip.
			verr = fmt.Errorf("sqlcheck: unsupported SQL construct %T", n.node)
			return true
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

	// I1: info.Limit reflects only the TOP-LEVEL statement's own LIMIT, read
	// directly off sel.Limit rather than recorded by the generic walk (which
	// would fire for every *tree.Limit anywhere in the tree, including one
	// on a nested subquery — letting an inner "LIMIT 10" mask an outer
	// LIMIT 5000, or mask the absence of any outer LIMIT at all). The walk
	// above still descends into every Limit node's Count/Offset expressions
	// (including nested ones) for table/function validation; this is a
	// separate, narrower read solely for the wrap-or-not decision.
	if sel.Limit != nil {
		info.Limit = limitCount(sel.Limit.Count)
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

// checkTable validates a single table reference (I3). It uses the
// PARSER's OWN structured fields (SchemaName/ObjectName/ExplicitSchema)
// rather than rendering the name via String() and lower-casing the whole
// result: cockroachdb-parser already applies the correct SQL identifier
// folding at parse time (an unquoted part is folded to lower case, a
// quoted part is preserved verbatim), so re-lower-casing after the fact
// would wrongly conflate a quoted `"SHEET_X"` (a different relation) with
// unquoted/lower-quoted `sheet_x`, and stripping quote characters out of
// the rendered string would wrongly conflate a single quoted identifier
// that merely CONTAINS a literal dot (e.g. "tabular.sheet_x", one
// unqualified relation) with an actual schema.table reference.
//
// CTE aliases are skipped (they aren't real relations). A zero-value
// TableName (e.g. surfaced via *tree.Order.Table when ordering by an
// expression, not an index) has an empty ObjectName and is skipped rather
// than rejected.
func checkTable(tn *tree.TableName, cteNames, allowedTables, tables map[string]bool, verr *error) bool {
	object := string(tn.ObjectName)
	if object == "" {
		return false
	}
	if !tn.ExplicitSchema {
		if cteNames[object] {
			return false
		}
		*verr = fmt.Errorf("sqlcheck: relation %q is outside the tabular schema", object)
		return true
	}
	// R48(c): a 3-part reference (catalog.schema.table) targets a
	// different database than the one sqlexec's pool is connected to —
	// Postgres itself has no cross-database queries within one connection,
	// so this can only be an attempt to smuggle a name past the schema
	// check (or, at best, a guaranteed execution error). Reject outright
	// rather than silently ignoring the catalog part.
	if tn.ExplicitCatalog {
		*verr = fmt.Errorf("sqlcheck: relation %q must not specify a catalog", string(tn.CatalogName)+"."+string(tn.SchemaName)+"."+object)
		return true
	}
	schema := string(tn.SchemaName)
	full := schema + "." + object
	if schema != "tabular" {
		*verr = fmt.Errorf("sqlcheck: relation %q is outside the tabular schema", full)
		return true
	}
	if !allowedTables[full] {
		*verr = fmt.Errorf("sqlcheck: relation %q is not a table of this knowledge base", full)
		return true
	}
	tables[full] = true
	return false
}

// collectAliases gathers every single-token qualifier a qualified column
// reference in this statement may legitimately use: table aliases
// (AliasedTableExpr.As), CTE names (already collected by the caller), and
// the bare object name of every table reference, aliased or not (so
// `FROM tabular."sheet_x"` — no alias — still lets a query write
// `sheet_x."col"`, matching how Postgres itself resolves it). This is a
// separate, best-effort pass over the same tree using the same visitor:
// best-effort because if the tree also contains something the main walk
// will fail-closed reject, this collector may hit the same node and stop
// early too — harmless, since that query is rejected either way.
func collectAliases(sel *tree.Select, cteNames map[string]bool) map[string]bool {
	aliases := map[string]bool{}
	for name := range cteNames {
		aliases[name] = true
	}
	c := &visitor{Fn: func(node any) bool {
		switch n := node.(type) {
		case *tree.AliasedTableExpr:
			if n.As.Alias != "" {
				aliases[string(n.As.Alias)] = true
			}
		case *tree.TableName:
			if n.ObjectName != "" {
				aliases[string(n.ObjectName)] = true
			}
		case tree.TableName:
			if n.ObjectName != "" {
				aliases[string(n.ObjectName)] = true
			}
		}
		return false
	}}
	c.walk([]tree.Statement{sel})
	return aliases
}

// checkQualifiedName is R48(d): a qualified *tree.UnresolvedName — a
// column reference (`alias.col`) or star (`schema.table.*`) with one or
// more qualifying parts — must resolve to something this query actually
// declared. Parts is stored in REVERSE order (column, table, schema,
// catalog); NumParts says how many of those (from the front) are
// populated. A bare column/star (NumParts < 2) has no qualifier to check.
func checkQualifiedName(n *tree.UnresolvedName, declaredAliases, allowedTables map[string]bool) error {
	switch n.NumParts {
	case 0, 1:
		return nil
	case 2:
		qualifier := n.Parts[1]
		if declaredAliases[qualifier] {
			return nil
		}
		return fmt.Errorf("sqlcheck: qualified reference %q does not resolve to a table declared in this query", qualifier)
	case 3:
		table, schema := n.Parts[1], n.Parts[2]
		full := schema + "." + table
		if schema == "tabular" && allowedTables[full] {
			return nil
		}
		return fmt.Errorf("sqlcheck: qualified reference %q is not a table of this knowledge base", full)
	default:
		return fmt.Errorf("sqlcheck: qualified reference with a catalog part is not allowed")
	}
}

// checkCastType is R48(a) + R55: a cast/annotate-type target that is NOT
// a plain, parser-resolved built-in type (*types.T) — e.g. a
// schema-qualified type name like pg_catalog.regclass or
// public.mydomain, which parses to *tree.UnresolvedObjectName instead —
// cannot be inspected for the reg* family at all (the *types.T type
// assertion would just silently miss it, letting an unaudited cast
// through), so it is rejected outright rather than let past unaudited.
// Among *types.T targets, the reg* family (regclass, regproc, regtype,
// regnamespace, ...) resolves an arbitrary string to a catalog OID at
// execution time — a second, un-audited object-name-to-existence oracle
// alongside the table/function allowlists — and is rejected too.
func checkCastType(t tree.ResolvableTypeReference) error {
	typ, ok := t.(*types.T)
	if !ok {
		return fmt.Errorf("sqlcheck: qualified or unknown cast type is not allowed")
	}
	if strings.HasPrefix(strings.ToLower(typ.PGName()), "reg") {
		return fmt.Errorf("sqlcheck: cast to %q is not allowed", typ.PGName())
	}
	return nil
}

// limitCount extracts a top-level LIMIT count as a plain int for the
// wrap-or-not decision (I1). Anything that isn't a bare integer literal —
// absent, LIMIT ALL, a placeholder, an expression, a subquery — returns 0
// ("treat as absent", i.e. wrap), which is the safe default: only a LIMIT
// we can read and prove is small enough is allowed to skip wrapping.
func limitCount(count tree.Expr) int {
	if count == nil {
		return 0
	}
	lit, ok := count.(*tree.NumVal)
	if !ok {
		return 0
	}
	i, err := lit.AsInt64()
	if err != nil {
		return 0
	}
	return int(i)
}
