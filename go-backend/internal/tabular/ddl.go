package tabular

import (
	"fmt"
	"strings"
)

// TabularSchema is the dedicated Postgres schema all per-region tables live in.
const TabularSchema = "tabular"

// RowIDColumn is the synthetic primary key added to every materialized
// table: the 1-based ordinal of the row within the materialized region. It
// is the join key for the fuzzy-search -> exact-SQL pivot and for cell-level
// citations. Underscore-prefixed so it can never collide with a sanitized
// user header (SanitizeIdentifier trims leading underscores).
const RowIDColumn = "_rowid"

// numericRe matches the canonical numeric text Canonical() emits (optionally
// signed integer/decimal, optional exponent). Used only inside a Postgres
// regex literal — it contains no user data.
const numericRe = `^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`

// q double-quotes a SQL identifier, escaping embedded quotes. The only
// identifier-quoting helper in this package: every table/column name in
// generated DDL/DML goes through it, so no identifier is ever interpolated
// unquoted.
func q(ident string) string { return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"` }

// stagingName derives the text-typed staging table name from the final
// table name.
func stagingName(table string) string { return table + "__stage" }

// BuildStagingTableSQL renders the CREATE TABLE statement for a region's
// staging table: a leading "_rowid" bigint column plus one TEXT column per
// (already sanitized) name, in order. Every value lands here as text first;
// BuildTypedTableSQL casts it server-side in a second pass.
func BuildStagingTableSQL(table string, names []string) string {
	parts := make([]string, 0, len(names)+1)
	parts = append(parts, q(RowIDColumn)+" bigint")
	for _, n := range names {
		parts = append(parts, q(n)+" text")
	}
	return fmt.Sprintf(`CREATE TABLE %s.%s (%s)`, q(TabularSchema), q(stagingName(table)), strings.Join(parts, ", "))
}

// castValue renders the cast expression for one column, without the
// trailing "AS <name>" — the fragment countCoercionFailures reuses to detect
// values that fail their target cast. src is the staging column the value
// comes from: the column's own name normally, or ShadowOf for a shadow
// column (a shadow never has its own staging column; it recasts the primary
// text column it shadows). A guarded CASE means "value is NULL" and "value
// failed to cast" are indistinguishable from the typed table alone — that is
// what countCoercionFailures exists to recover.
func castValue(c ColumnSpec) string {
	src := c.Name
	if c.ShadowOf != "" {
		src = c.ShadowOf
	}
	switch c.Type {
	case TypeNumeric:
		return fmt.Sprintf(`CASE WHEN %s ~ '%s' THEN %s::numeric END`, q(src), numericRe, q(src))
	case TypeDate:
		return fmt.Sprintf(`CASE WHEN %s ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' THEN %s::date END`, q(src), q(src))
	case TypeTimestamp:
		return fmt.Sprintf(`CASE WHEN %s ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}' THEN %s::timestamp END`, q(src), q(src))
	case TypeBool:
		return fmt.Sprintf(`CASE WHEN %s IN ('true','false') THEN %s::boolean END`, q(src), q(src))
	default:
		return q(src)
	}
}

// castExpr renders one output column of the typed-table CTAS: castValue
// aliased to the column's own (final, deduped) name.
func castExpr(c ColumnSpec) string {
	return castValue(c) + " AS " + q(c.Name)
}

// BuildTypedTableSQL renders the server-side "pass 2": a CREATE TABLE ... AS
// SELECT that casts every staging column to its inferred type in one
// statement, ordered by _rowid. Column names are sanitized identifiers
// ([a-z0-9_] only, deduped) and the regex literals in castValue contain no
// user data, so no cell value is ever interpolated into this SQL — values
// only ever traveled through the earlier COPY.
func BuildTypedTableSQL(table string, cols []ColumnSpec) string {
	exprs := make([]string, 0, len(cols)+1)
	exprs = append(exprs, q(RowIDColumn))
	for _, c := range cols {
		exprs = append(exprs, castExpr(c))
	}
	return fmt.Sprintf(`CREATE TABLE %s.%s AS SELECT %s FROM %s.%s ORDER BY %s`,
		q(TabularSchema), q(table), strings.Join(exprs, ", "),
		q(TabularSchema), q(stagingName(table)), q(RowIDColumn))
}
