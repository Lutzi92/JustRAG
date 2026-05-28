package main

import "strings"

// tokenizeIDs splits a gold_doc_ids cell on commas, semicolons, and whitespace,
// dropping empty tokens. Returns nil when the input has no tokens (so that
// downstream JSON-encoders do not emit `[""]`).
func tokenizeIDs(s string) []string {
	var out []string
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		if t := strings.TrimSpace(tok); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// mapCategoryToQueryType translates the XLSX "Kategorie" field to the repo's
// query_type taxonomy (lookup | enumeration | complex_reasoning |
// global_synthesis). Returns skip=true for categories that have no
// must_cite_file_ids (currently only "unanswerable") so the caller can drop
// the row before the loader's non-empty validation rejects it.
//
// Unknown / empty categories pass through with qt="" and skip=false: the
// resulting Question is still valid, it just won't show up in per-route
// aggregates.
func mapCategoryToQueryType(c string) (qt string, skip bool) {
	switch c {
	case "single_hop", "temporal", "yes_no", "negation", "ambiguous":
		return "lookup", false
	case "aggregation":
		return "enumeration", false
	case "multi_hop", "comparison", "procedural":
		return "complex_reasoning", false
	case "unanswerable":
		return "", true
	default:
		return "", false
	}
}

// applyMapping translates a list of source IDs (e.g. Confluence page IDs) to
// destination IDs (e.g. files.id UUIDs) using the provided lookup table.
// Resolved IDs preserve the input order; unresolved IDs are returned in the
// missing slice for diagnostics. Output order is stable so JSONL diffs across
// runs are deterministic.
func applyMapping(srcIDs []string, mapping map[string]string) (mapped, missing []string) {
	for _, id := range srcIDs {
		if dst, ok := mapping[id]; ok {
			mapped = append(mapped, dst)
		} else {
			missing = append(missing, id)
		}
	}
	return mapped, missing
}
