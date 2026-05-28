package tabular

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// inferSampleRows caps how many rows the type inferrer samples per column.
const inferSampleRows = 1000

var dateLayouts = []string{"2006-01-02", "2006/01/02", "02.01.2006"}

// inferColumnType picks the narrowest type every non-empty value satisfies.
// Empty strings are treated as NULL and ignored. An all-empty column is text.
func inferColumnType(vals []string) ColumnType {
	allInt, allFloat, allDate, allBool, seen := true, true, true, true, false
	for _, raw := range vals {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		seen = true
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			allInt = false
		}
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			allFloat = false
		}
		if !isBool(v) {
			allBool = false
		}
		if !isDate(v) {
			allDate = false
		}
	}
	if !seen {
		return TypeText
	}
	switch {
	case allInt:
		return TypeBigint
	case allFloat:
		return TypeFloat
	case allBool:
		return TypeBool
	case allDate:
		return TypeDate
	default:
		return TypeText
	}
}

func isBool(v string) bool {
	switch strings.ToLower(v) {
	case "true", "false":
		return true
	}
	return false
}

func isDate(v string) bool {
	for _, l := range dateLayouts {
		if _, err := time.Parse(l, v); err == nil {
			return true
		}
	}
	return false
}

var nonIdent = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeIdentifier lowercases, strips non-ASCII-alphanumerics to single
// underscores, trims leading/trailing underscores, prefixes a leading digit
// or empty result with "col", and truncates to 63 bytes (Postgres limit).
func sanitizeIdentifier(s string) string {
	out := nonIdent.ReplaceAllString(strings.ToLower(s), "_")
	out = strings.Trim(out, "_")
	if out == "" {
		return "col"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "col_" + out
	}
	if len(out) > 63 {
		out = out[:63]
	}
	return out
}

// dedupeIdentifiers makes a list of sanitized names unique by appending _2,
// _3, … to collisions (in order).
func dedupeIdentifiers(names []string) []string {
	seen := map[string]int{}
	out := make([]string, len(names))
	for i, n := range names {
		seen[n]++
		if seen[n] == 1 {
			out[i] = n
			continue
		}
		out[i] = fmt.Sprintf("%s_%d", n, seen[n])
	}
	return out
}

// detectHeader returns the header names and the remaining data rows. If the
// first row is entirely numeric it is treated as data and synthetic col_N
// names are generated.
func detectHeader(rows [][]string) (headers []string, data [][]string) {
	if len(rows) == 0 {
		return nil, nil
	}
	first := rows[0]
	numeric := len(first) > 0
	for _, c := range first {
		v := strings.TrimSpace(c)
		if v == "" {
			continue
		}
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			numeric = false
			break
		}
	}
	if numeric {
		headers = make([]string, len(first))
		for i := range first {
			headers[i] = fmt.Sprintf("col_%d", i+1)
		}
		return headers, rows
	}
	return first, rows[1:]
}

// BuildColumnSpecs detects the header, infers each column's type from a sample,
// returns sanitized, de-duplicated column specs plus the data rows (header
// excluded). When opts.Enabled, free-text columns are flagged for embedding.
func BuildColumnSpecs(rows [][]string, opts SemanticOptions) (cols []ColumnSpec, data [][]string) {
	headers, data := detectHeader(rows)
	sanitized := dedupeIdentifiers(sanitizeNames(headers))
	cols = make([]ColumnSpec, len(headers))
	for i := range headers {
		cols[i] = ColumnSpec{
			Original: headers[i],
			Name:     sanitized[i],
			Type:     inferColumnType(columnSample(data, i)),
		}
	}
	if opts.Enabled {
		flagSemanticColumns(cols, data, opts.MinAvgLen, opts.MinDistinctRatio)
	}
	return cols, data
}

// flagSemanticColumns sets Embedded on each TEXT column whose sampled values
// are long (mean length >= minAvgLen) AND high-cardinality (distinct ratio of
// non-empty values >= minDistinctRatio). A threshold of 0 disables that filter.
// Non-TEXT columns are never flagged. Uses the same capped sample window as
// type inference (columnSample), so cost stays bounded by inferSampleRows.
func flagSemanticColumns(cols []ColumnSpec, data [][]string, minAvgLen int, minDistinctRatio float64) {
	for i := range cols {
		if cols[i].Type != TypeText {
			continue
		}
		sample := columnSample(data, i)
		var totalLen, nonEmpty int
		seen := map[string]struct{}{}
		for _, v := range sample {
			t := strings.TrimSpace(v)
			if t == "" {
				continue
			}
			nonEmpty++
			totalLen += len(t)
			seen[t] = struct{}{}
		}
		if nonEmpty == 0 {
			continue
		}
		avgLen := totalLen / nonEmpty
		distinctRatio := float64(len(seen)) / float64(nonEmpty)
		if minAvgLen > 0 && avgLen < minAvgLen {
			continue
		}
		if minDistinctRatio > 0 && distinctRatio < minDistinctRatio {
			continue
		}
		cols[i].Embedded = true
	}
}

func sanitizeNames(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = sanitizeIdentifier(s)
	}
	return out
}

func columnSample(data [][]string, col int) []string {
	n := len(data)
	if n > inferSampleRows {
		n = inferSampleRows
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if col < len(data[i]) {
			out = append(out, data[i][col])
		} else {
			out = append(out, "")
		}
	}
	return out
}
