package sqlcheck

// Ported from github.com/auxten/postgresql-parser's pkg/walk/walker.go
// (same origin AST shapes), adapted to cockroachdb-parser's package paths,
// to a stop-propagating recursive walk (the upstream walker's `stop` only
// broke the innermost loop; ours short-circuits the whole traversal once
// Fn reports a violation), and — per the 2026-09 security review (R38) —
// to fail CLOSED: every node type this switch does not explicitly
// recognize (as either a container to descend into, or a leaf with no
// children worth inspecting) is treated as a validation error rather than
// silently skipped. The original port only added descend cases for the
// constructs its own test suite happened to exercise; anything else
// (ARRAY(subquery), a WINDOW clause, ORDER BY inside an aggregate call,
// OFFSET, COLLATE, NULLIF, a subscript, IS OF, IF(...)) was invisible to
// the walker, so a table or function reference hidden inside one of those
// bypassed both allowlists undetected. Fail-closed means a *future* gap of
// the same shape produces a rejected query, not a silent bypass.
import (
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
)

// visitor walks statements and expressions; Fn returns true to stop.
type visitor struct{ Fn func(node any) (stop bool) }

// unrecognized wraps a node the switch below has no explicit case for. Fn
// is still invoked with it (same call convention as every other node) so
// sqlcheck's callback is the single place that turns "I don't recognize
// this" into a rejection error.
type unrecognized struct{ node any }

// disallowed wraps a RECOGNIZED construct that this switch categorically
// rejects regardless of content (unlike unrecognized, which is for a
// concrete type this package has no case for at all). Used for CRDB-only
// syntax that cannot even execute against the real Postgres backend
// (AS OF SYSTEM TIME, index hints) — fail round 2, R48(b).
type disallowed struct{ reason string }

func (v *visitor) walk(stmts []tree.Statement) {
	for _, s := range stmts {
		if v.node(s) {
			return
		}
	}
}

// nodes visits each of ns in order, stopping as soon as one reports stop.
func (v *visitor) nodes(ns ...any) bool {
	for _, n := range ns {
		if v.node(n) {
			return true
		}
	}
	return false
}

// node dispatches on the concrete AST types the router can produce. Add a
// case here when a new construct must be inspected. The default arm is
// the fail-closed guarantee (R38): anything not listed below — container
// or leaf — is reported to Fn as unrecognized and stops the walk.
func (v *visitor) node(n any) bool { //nolint:gocyclo,funlen // one dispatch table over the AST is clearer than splitting it
	if n == nil {
		return false
	}
	if v.Fn != nil && v.Fn(n) {
		return true
	}
	if _, ok := n.(tree.Datum); ok {
		// Already-resolved literal values (NULL, and any constant the
		// parser folded to a typed Datum) are leaves.
		return false
	}
	switch t := n.(type) {
	case *tree.AliasedTableExpr:
		// R48(b): index hints (@{FORCE_INDEX=...}) are CRDB-only syntax
		// that can't execute against the real Postgres backend anyway, and
		// aren't part of the router's supported surface.
		if t.IndexFlags != nil {
			if v.Fn != nil {
				v.Fn(disallowed{reason: "index hints are not allowed"})
			}
			return true
		}
		return v.node(t.Expr)
	case *tree.ParenTableExpr:
		return v.node(t.Expr)
	case *tree.AndExpr:
		return v.nodes(t.Left, t.Right)
	case *tree.AnnotateTypeExpr:
		return v.node(t.Expr)
	case *tree.Array:
		return v.node(t.Exprs)
	case *tree.ArrayFlatten:
		return v.node(t.Subquery)
	case *tree.BinaryExpr:
		return v.nodes(t.Left, t.Right)
	case *tree.CaseExpr:
		if v.nodes(t.Expr, t.Else) {
			return true
		}
		for _, when := range t.Whens {
			if v.nodes(when.Cond, when.Val) {
				return true
			}
		}
	case *tree.RangeCond:
		return v.nodes(t.Left, t.From, t.To)
	case *tree.CastExpr:
		return v.node(t.Expr)
	case *tree.CoalesceExpr:
		for _, e := range t.Exprs {
			if v.node(e) {
				return true
			}
		}
	case *tree.CollateExpr:
		return v.node(t.Expr)
	case *tree.ComparisonExpr:
		return v.nodes(t.Left, t.Right)
	case *tree.CTE:
		return v.node(t.Stmt)
	case tree.Exprs:
		for _, e := range t {
			if v.node(e) {
				return true
			}
		}
	case *tree.From:
		// R48(b): AS OF SYSTEM TIME is CRDB-only syntax (real Postgres has
		// no equivalent), and its expression is otherwise never visited.
		if t.AsOf.Expr != nil {
			if v.Fn != nil {
				v.Fn(disallowed{reason: "AS OF SYSTEM TIME is not allowed"})
			}
			return true
		}
		for _, tbl := range t.Tables {
			if v.node(tbl) {
				return true
			}
		}
	case *tree.FuncExpr:
		if t.WindowDef != nil && v.node(t.WindowDef) {
			return true
		}
		if len(t.OrderBy) > 0 && v.node(t.OrderBy) {
			return true
		}
		return v.nodes(t.Exprs, t.Filter)
	case *tree.IfExpr:
		return v.nodes(t.Cond, t.True, t.Else)
	case *tree.IndirectionExpr:
		if v.node(t.Expr) {
			return true
		}
		for _, sub := range t.Indirection {
			if sub == nil {
				continue
			}
			if v.nodes(sub.Begin, sub.End) {
				return true
			}
		}
	case *tree.IsOfTypeExpr:
		return v.node(t.Expr)
	case *tree.IsNullExpr:
		return v.node(t.Expr)
	case *tree.IsNotNullExpr:
		return v.node(t.Expr)
	case *tree.JoinTableExpr:
		return v.nodes(t.Left, t.Right, t.Cond)
	case *tree.NotExpr:
		return v.node(t.Expr)
	case *tree.NullIfExpr:
		return v.nodes(t.Expr1, t.Expr2)
	case *tree.OnJoinCond:
		return v.node(t.Expr)
	case *tree.UsingJoinCond, tree.NaturalJoinCond:
		// Leaf: USING (...) carries only column NAMES (tree.NameList), and
		// NATURAL carries nothing at all — neither can hide a relation or
		// function reference. Fix round 2 (regression): both hit the
		// fail-closed default before this case existed.
	case *tree.Order:
		return v.nodes(t.Expr, t.Table)
	case tree.OrderBy:
		for _, o := range t {
			if v.node(o) {
				return true
			}
		}
	case *tree.OrExpr:
		return v.nodes(t.Left, t.Right)
	case *tree.ParenExpr:
		return v.node(t.Expr)
	case *tree.ParenSelect:
		return v.node(t.Select)
	case *tree.RowsFromExpr:
		for _, e := range t.Items {
			if v.node(e) {
				return true
			}
		}
	case *tree.Select:
		if t.With != nil && v.node(t.With) {
			return true
		}
		if len(t.OrderBy) > 0 && v.node(t.OrderBy) {
			return true
		}
		if t.Limit != nil && v.node(t.Limit) {
			return true
		}
		return v.node(t.Select)
	case *tree.Limit:
		if t.Count != nil && v.node(t.Count) {
			return true
		}
		if t.Offset != nil {
			return v.node(t.Offset)
		}
	case *tree.SelectClause:
		if v.node(t.Exprs) {
			return true
		}
		if t.Where != nil && v.node(t.Where) {
			return true
		}
		if t.Having != nil && v.node(t.Having) {
			return true
		}
		for _, d := range t.DistinctOn {
			if v.node(d) {
				return true
			}
		}
		for _, g := range t.GroupBy {
			if v.node(g) {
				return true
			}
		}
		for _, w := range t.Window {
			if v.node(w) {
				return true
			}
		}
		return v.node(&t.From)
	case tree.SelectExpr:
		return v.node(t.Expr)
	case tree.SelectExprs:
		for _, e := range t {
			if v.node(e) {
				return true
			}
		}
	case *tree.Subquery:
		return v.node(t.Select)
	case tree.TableExprs:
		for _, e := range t {
			if v.node(e) {
				return true
			}
		}
	case *tree.TableName, tree.TableName:
		// Leaf: already passed to Fn above.
	case *tree.Tuple:
		for _, e := range t.Exprs {
			if v.node(e) {
				return true
			}
		}
	case *tree.UnaryExpr:
		return v.node(t.Expr)
	case *tree.UnionClause:
		return v.nodes(t.Left, t.Right)
	case *tree.ValuesClause:
		for _, row := range t.Rows {
			if v.node(row) {
				return true
			}
		}
	case *tree.Where:
		return v.node(t.Expr)
	case tree.Window:
		for _, w := range t {
			if v.node(w) {
				return true
			}
		}
	case *tree.WindowDef:
		if v.node(t.Partitions) {
			return true
		}
		if len(t.OrderBy) > 0 && v.node(t.OrderBy) {
			return true
		}
		if t.Frame != nil {
			return v.node(t.Frame)
		}
	case *tree.WindowFrame:
		if t.Bounds.StartBound != nil && v.node(t.Bounds.StartBound) {
			return true
		}
		if t.Bounds.EndBound != nil {
			return v.node(t.Bounds.EndBound)
		}
	case *tree.WindowFrameBound:
		return v.node(t.OffsetExpr)
	case *tree.With:
		for _, e := range t.CTEList {
			if v.node(e) {
				return true
			}
		}

	// Leaves: no children worth inspecting (no relation or function
	// reference can hide inside these). Listed explicitly rather than
	// falling through to a permissive default, per R38.
	//
	// *tree.AllColumnsSelector and *tree.ColumnItem are deliberately NOT
	// listed here (fix round 2, R48(d)): both are documented in
	// cockroachdb-parser itself as intermediate, post-name-resolution
	// structures ("ColumnItems... still need to undergo name resolution"),
	// and empirically parser.Parse (no resolver in this pipeline) never
	// produces them — every qualified column/star reference this package
	// has observed parses to *tree.UnresolvedName instead (see the case
	// below, and checkQualifiedName in validate.go). If some SQL shape or
	// a future parser version ever does produce one, it now fails closed
	// via the default arm rather than being silently treated as safe.
	case *tree.NumVal, *tree.StrVal, tree.UnqualifiedStar, tree.DefaultVal:
		return false

	case *tree.UnresolvedName:
		// Leaf structurally (a name has no sub-expressions to descend
		// into), but NOT unconditionally safe: a qualified reference
		// (NumParts >= 2) names a table, and that table must be one this
		// query is actually allowed to touch (R48(d) — checked in Fn via
		// checkQualifiedName, since it needs the allowlist and the
		// declared-alias set that Validate collects).
		return false

	default:
		if v.Fn != nil {
			v.Fn(unrecognized{node: t})
		}
		return true
	}
	return false
}
