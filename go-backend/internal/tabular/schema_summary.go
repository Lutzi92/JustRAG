package tabular

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/splitter"
	"github.com/justrag/go-backend/internal/tabular/profile"
)

// SchemaSummary is the LLM-facing catalog text for one KB (design §5.1 step
// 4): a token-budgeted rendering of the per-table schema the router's SQL
// generator reads.
type SchemaSummary struct {
	Text   string   // markdown-ish, ~300-500 tokens per table
	Tables []string // included table names, in order
	Tokens int
	Pruned bool // true when tables were dropped to fit maxTokens

	// AllowedTables is "tabular.<name>" -> true for EVERY table of the KB
	// (never pruned) — the SQL validator's allowlist, independent of which
	// tables made it into Text.
	AllowedTables map[string]bool
}

// filterCatalogString drops a catalog-sourced string that looks like a
// prompt-injection attempt (profile.LooksLikeInstruction) — spreadsheet
// cells are attacker-controlled data, not instructions, and this applies to
// every catalog string that reaches the LLM prompt: headers, descriptions,
// samples, value-set entries, and sheet/file names.
func filterCatalogString(s string) string {
	if s == "" || profile.LooksLikeInstruction(s) {
		return ""
	}
	return s
}

// formatRowCount renders a row count with a thousands separator for the
// per-table heading only (min/max numbers stay raw, unformatted).
func formatRowCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var groups []string
	for len(s) > 3 {
		groups = append([]string{s[len(s)-3:]}, groups...)
		s = s[:len(s)-3]
	}
	groups = append([]string{s}, groups...)
	out := strings.Join(groups, " ")
	if neg {
		out = "-" + out
	}
	return out
}

// renderColumnLine renders one column's schema line. A shadow column (one
// whose ColumnSpec.ShadowOf is set) gets the terse "shadow of" form; its
// role/description/stats are not rendered separately (they belong to the
// primary column's line).
func renderColumnLine(col ColumnSpec, stat *ColumnStat) string {
	if col.ShadowOf != "" {
		return fmt.Sprintf("- %s (%s, shadow of %s)", col.Name, col.Type, col.ShadowOf)
	}

	var head strings.Builder
	head.WriteString("- ")
	head.WriteString(col.Name)
	head.WriteString(" (")
	typeParts := []string{string(col.Type)}
	if col.Role != "" {
		typeParts = append(typeParts, col.Role)
	}
	head.WriteString(strings.Join(typeParts, ", "))
	head.WriteString(")")

	if original := filterCatalogString(col.Original); original != "" {
		fmt.Fprintf(&head, " %q", original)
	}

	var parts []string
	if desc := filterCatalogString(col.Description); desc != "" {
		parts = append(parts, desc)
	}
	if stat != nil {
		if len(stat.Samples) > 0 {
			var samples []string
			for _, raw := range stat.Samples {
				s := filterCatalogString(raw)
				if s == "" {
					continue
				}
				samples = append(samples, s)
				if len(samples) == 3 {
					break
				}
			}
			if len(samples) > 0 {
				parts = append(parts, "e.g. "+strings.Join(samples, ", "))
			}
		}
		if stat.Min != "" || stat.Max != "" {
			var minMax []string
			if stat.Min != "" {
				minMax = append(minMax, "min "+stat.Min)
			}
			if stat.Max != "" {
				minMax = append(minMax, "max "+stat.Max)
			}
			parts = append(parts, strings.Join(minMax, " "))
		}
		if n := len(stat.ValueSet); n > 0 && n <= 20 {
			var vals []string
			for _, raw := range stat.ValueSet {
				v := filterCatalogString(raw)
				if v == "" {
					continue
				}
				vals = append(vals, v)
			}
			if len(vals) > 0 {
				parts = append(parts, "values: "+strings.Join(vals, " | "))
			}
		}
		if stat.ShadowColumn != "" {
			parts = append(parts, "numeric shadow: "+stat.ShadowColumn)
		}
	}

	if len(parts) > 0 {
		head.WriteString(": ")
		head.WriteString(strings.Join(parts, "; "))
	}
	return head.String()
}

// renderTableBlock renders one table's full schema block: heading + one
// line per column, in catalog column order.
func renderTableBlock(e CatalogEntry) string {
	statsByName := make(map[string]*ColumnStat, len(e.ColumnStats))
	for i := range e.ColumnStats {
		s := &e.ColumnStats[i]
		statsByName[s.Name] = s
	}

	fileName := filterCatalogString(e.FileName)
	sheetName := filterCatalogString(e.SheetName)

	var b strings.Builder
	fmt.Fprintf(&b, "### tabular.%s — %q › %s (%s rows)", e.TableName, fileName, sheetName, formatRowCount(e.RowCount))
	for _, col := range e.Columns {
		b.WriteString("\n")
		b.WriteString(renderColumnLine(col, statsByName[col.Name]))
	}
	return b.String()
}

// queryWords tokenizes query into lower-cased words of >= 4 runes, trimming
// boundary punctuation (so "Denkmalschutz?" matches "Denkmalschutz").
func queryWords(query string) []string {
	var words []string
	for _, w := range strings.Fields(query) {
		w = strings.ToLower(w)
		w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if utf8.RuneCountInString(w) >= 4 {
			words = append(words, w)
		}
	}
	return words
}

// overlapCount counts the words (already filtered to >= 4 runes,
// lower-cased) found as a substring of any column Original/Description or
// the sheet/file name (lower-cased), counting each query word at most once.
func overlapCount(e CatalogEntry, words []string) int {
	if len(words) == 0 {
		return 0
	}
	targets := make([]string, 0, len(e.Columns)*2+2)
	for _, col := range e.Columns {
		if col.Original != "" {
			targets = append(targets, strings.ToLower(col.Original))
		}
		if col.Description != "" {
			targets = append(targets, strings.ToLower(col.Description))
		}
	}
	if e.SheetName != "" {
		targets = append(targets, strings.ToLower(e.SheetName))
	}
	if e.FileName != "" {
		targets = append(targets, strings.ToLower(e.FileName))
	}

	count := 0
	for _, w := range words {
		for _, t := range targets {
			if strings.Contains(t, w) {
				count++
				break
			}
		}
	}
	return count
}

// CompactSchema renders entries into the LLM-facing schema text. When the
// full rendering exceeds maxTokens, tables are ranked by
// (value hits desc, header-overlap-with-query desc, row count desc) and the
// lowest-ranked tail is dropped (Pruned=true) — AllowedTables always covers
// every table regardless of pruning.
func CompactSchema(entries []CatalogEntry, hits []ValueHit, query string, maxTokens int) SchemaSummary {
	allowed := make(map[string]bool)
	type block struct {
		entry CatalogEntry
		text  string
	}
	var blocks []block
	for _, e := range entries {
		if e.SheetKind != "table" {
			continue
		}
		allowed["tabular."+e.TableName] = true
		blocks = append(blocks, block{entry: e, text: renderTableBlock(e)})
	}

	joinBlocks := func(bs []block) string {
		texts := make([]string, len(bs))
		for i, b := range bs {
			texts[i] = b.text
		}
		return strings.Join(texts, "\n\n")
	}

	fullText := joinBlocks(blocks)
	fullTokens := splitter.CountTokens(fullText)
	if fullTokens <= maxTokens {
		tables := make([]string, len(blocks))
		for i, b := range blocks {
			tables[i] = b.entry.TableName
		}
		return SchemaSummary{
			Text:          fullText,
			Tables:        tables,
			Tokens:        fullTokens,
			Pruned:        false,
			AllowedTables: allowed,
		}
	}

	hitCounts := make(map[string]int, len(hits))
	for _, h := range hits {
		hitCounts[h.TableName]++
	}
	words := queryWords(query)

	ranked := make([]block, len(blocks))
	copy(ranked, blocks)
	scoreOf := func(b block) float64 {
		hitsN := float64(hitCounts[b.entry.TableName])
		overlap := float64(overlapCount(b.entry, words))
		return 1000*hitsN + 10*overlap + math.Log10(float64(b.entry.RowCount)+1)
	}
	sort.SliceStable(ranked, func(i, j int) bool { return scoreOf(ranked[i]) > scoreOf(ranked[j]) })

	var selected []block
	for _, b := range ranked {
		trial := append(append([]block{}, selected...), b)
		if splitter.CountTokens(joinBlocks(trial)) > maxTokens {
			break
		}
		selected = trial
	}

	finalText := joinBlocks(selected)
	tables := make([]string, len(selected))
	for i, b := range selected {
		tables[i] = b.entry.TableName
	}
	return SchemaSummary{
		Text:          finalText,
		Tables:        tables,
		Tokens:        splitter.CountTokens(finalText),
		Pruned:        true,
		AllowedTables: allowed,
	}
}
