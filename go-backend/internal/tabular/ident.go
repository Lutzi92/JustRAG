// Identifier sanitization for materialized tabular tables/columns (Phase 2).
// Exported so both the materializer and the profiler can share one
// transliteration + dedup rule; infer.go's unexported sanitizeIdentifier/
// dedupeIdentifiers are the Phase-1 originals and are deleted in Task 4.
package tabular

import (
	"fmt"
	"regexp"
	"strings"
)

var identTranslit = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "Ä", "ae", "Ö", "oe", "Ü", "ue",
	"ß", "ss", "²", "2", "³", "3", "%", "pct", "€", "eur",
)
var nonIdentV2 = regexp.MustCompile(`[^a-z0-9]+`)

const maxIdentBytes = 63

// SanitizeIdentifier turns an arbitrary spreadsheet header into a safe SQL
// identifier: transliterates German/common symbols, lowercases, collapses
// any run of non [a-z0-9] into a single underscore, trims leading/trailing
// underscores, prefixes "col_" when the result is empty or starts with a
// digit, and truncates to maxIdentBytes.
func SanitizeIdentifier(s string) string {
	s = strings.ToLower(identTranslit.Replace(s))
	s = strings.Trim(nonIdentV2.ReplaceAllString(s, "_"), "_")
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		s = "col_" + s
	}
	if len(s) > maxIdentBytes {
		s = strings.TrimRight(s[:maxIdentBytes], "_")
	}
	return s
}

// DedupeIdentifiers appends _2, _3, … to later occurrences of a repeated
// name so every entry in the returned slice is unique, staying within
// maxIdentBytes including the suffix.
func DedupeIdentifiers(names []string) []string {
	out := make([]string, len(names))
	seen := map[string]bool{}
	for i, n := range names {
		cand := n
		for k := 2; seen[cand]; k++ {
			suffix := fmt.Sprintf("_%d", k)
			cand = trimTo(n, maxIdentBytes-len(suffix)) + suffix
		}
		seen[cand] = true
		out[i] = cand
	}
	return out
}

func trimTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimRight(s[:n], "_")
}

// TableNameForRegion builds the collision-safe physical table name for one
// materialized region within a sheet: sheet_<fileuuid-without-dashes>_<sheetIdx>_<regionIdx>.
func TableNameForRegion(fileID string, sheetIdx, regionIdx int) string {
	return fmt.Sprintf("sheet_%s_%d_%d", strings.ReplaceAll(fileID, "-", ""), sheetIdx, regionIdx)
}
