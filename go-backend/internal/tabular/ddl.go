package tabular

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TabularSchema is the dedicated Postgres schema all per-sheet tables live in.
const TabularSchema = "tabular"

// RowIDColumn is the synthetic primary key added to every materialized table
// when Phase-2 semantic columns are enabled. Underscore-prefixed so it can
// never collide with a sanitized user header (sanitizeIdentifier trims leading
// underscores). It is the join key for the fuzzy-search -> exact-SQL pivot.
const RowIDColumn = "_rowid"

// BuildCreateTableSQL renders the CREATE TABLE statement for a sheet. When
// withRowID is true, a leading "_rowid" bigint column is prepended. Identifiers
// are sanitized + double-quoted and types are from the fixed ColumnType set, so
// this is injection-safe.
func BuildCreateTableSQL(tableName string, cols []ColumnSpec, withRowID bool) string {
	parts := make([]string, 0, len(cols)+1)
	if withRowID {
		parts = append(parts, fmt.Sprintf("%q bigint", RowIDColumn))
	}
	for _, c := range cols {
		parts = append(parts, fmt.Sprintf("%q %s", c.Name, string(c.Type)))
	}
	return fmt.Sprintf("CREATE TABLE %s.%q (%s)", TabularSchema, tableName, strings.Join(parts, ", "))
}

// BuildRowChunkContent renders the embeddable content for one row's flagged
// (Embedded) columns: a parseable source header line followed by one
// `Original: value` line per non-empty flagged column. Returns ok=false when no
// flagged column has content (caller emits no chunk). The header lets the agent
// recover the table + _rowid to pivot to `table_query ... WHERE _rowid IN (...)`.
func BuildRowChunkContent(tableName string, rowID int64, cols []ColumnSpec, row []string) (string, bool) {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s.%s row %d]\n", TabularSchema, tableName, rowID)
	any := false
	for i, c := range cols {
		if !c.Embedded {
			continue
		}
		if i >= len(row) {
			continue
		}
		val := strings.TrimSpace(row[i])
		if val == "" {
			continue
		}
		label := c.Original
		if label == "" {
			label = c.Name
		}
		fmt.Fprintf(&b, "%s: %s\n", label, val)
		any = true
	}
	if !any {
		return "", false
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// ColumnNames returns the sanitized identifiers in order (the pgx.CopyFrom
// column list).
func ColumnNames(cols []ColumnSpec) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

// coerceValue converts a raw cell to the Go value pgx will COPY for the
// target type. Empty string -> (nil, true) i.e. NULL. A non-empty value that
// fails its target cast -> (nil, false): the caller stores NULL and records a
// coercion failure. Text never fails.
func coerceValue(raw string, t ColumnType) (any, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil, true
	}
	switch t {
	case TypeBigint:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, false
		}
		return n, true
	case TypeFloat:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, false
		}
		return f, true
	case TypeBool:
		if !isBool(v) {
			return nil, false
		}
		return strings.EqualFold(v, "true"), true
	case TypeDate:
		for _, l := range dateLayouts {
			if d, err := time.Parse(l, v); err == nil {
				return d, true
			}
		}
		return nil, false
	default:
		return raw, true
	}
}
