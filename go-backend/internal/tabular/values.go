package tabular

import (
	"context"
	"strings"
	"unicode/utf8"
)

// ValueHit is one stored value matched from the question.
type ValueHit struct {
	Literal    string // the question literal that matched
	TableName  string
	SheetName  string
	FileName   string
	ColumnName string
	Value      string // the stored value, exact casing
	RowCount   int64
	Match      string // "exact" | "prefix" | "substring"
}

const lookupValuesSQL = `
SELECT table_name, column_name, value, row_count,
       CASE WHEN lower(value) = lower($2) THEN 'exact'
            WHEN lower(value) LIKE lower($2) || '%' ESCAPE '\' THEN 'prefix'
            ELSE 'substring' END AS match
FROM tabular_column_values
WHERE table_name = ANY($1)
  AND lower(value) LIKE '%' || lower($3) || '%' ESCAPE '\'
ORDER BY CASE WHEN lower(value) = lower($2) THEN 0 WHEN lower(value) LIKE lower($2) || '%' ESCAPE '\' THEN 1 ELSE 2 END,
         row_count DESC
LIMIT $4`

// escapeLike escapes LIKE metacharacters (\, %, _) so a literal is matched
// verbatim inside a LIKE pattern with ESCAPE '\'.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

type tableMeta struct {
	sheetName string
	fileName  string
}

// LookupValues matches each literal against tabular_column_values of the
// given tables: exact (lower(value) = lower($lit)) first, then prefix,
// then substring; at most perLiteral hits per literal, ordered by match
// quality then row_count desc. Literals shorter than 2 runes are skipped.
func (c *Catalog) LookupValues(ctx context.Context, entries []CatalogEntry, literals []string, perLiteral int) ([]ValueHit, error) {
	if len(entries) == 0 || len(literals) == 0 || perLiteral <= 0 {
		return nil, nil
	}

	tables := make([]string, 0, len(entries))
	meta := make(map[string]tableMeta, len(entries))
	for _, e := range entries {
		tables = append(tables, e.TableName)
		meta[e.TableName] = tableMeta{sheetName: e.SheetName, fileName: e.FileName}
	}

	type key struct{ table, column, value string }
	seen := make(map[key]bool)
	var out []ValueHit

	for _, lit := range literals {
		if utf8.RuneCountInString(lit) < 2 {
			continue
		}
		escaped := escapeLike(lit)
		rows, err := c.pool.Query(ctx, lookupValuesSQL, tables, lit, escaped, perLiteral)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var tableName, columnName, value, match string
			var rowCount int64
			if err := rows.Scan(&tableName, &columnName, &value, &rowCount, &match); err != nil {
				rows.Close()
				return nil, err
			}
			k := key{tableName, columnName, value}
			if seen[k] {
				continue
			}
			seen[k] = true
			m := meta[tableName]
			out = append(out, ValueHit{
				Literal:    lit,
				TableName:  tableName,
				SheetName:  m.sheetName,
				FileName:   m.fileName,
				ColumnName: columnName,
				Value:      value,
				RowCount:   rowCount,
				Match:      match,
			})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	return out, nil
}
