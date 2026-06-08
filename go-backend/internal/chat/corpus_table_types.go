package chat

import (
	"path/filepath"
	"regexp"
	"strings"
)

// StructuredTable is the assembled, exportable comparison table produced by the
// corpus-table orchestrator. It is the single source of truth for both the
// in-chat rich-table render and the client-side .xlsx export, so it carries
// enough type info for the frontend to format cells. Persisted as jsonb on the
// message and emitted over SSE as {"structuredTable": <this>}.
type StructuredTable struct {
	Columns []TableColumn `json:"columns"`
	Rows    []TableRow    `json:"rows"`
	// Truncated is true when more in-scope files existed than the cap; the
	// table covers only the top-N.
	Truncated bool `json:"truncated"`
	// TotalFiles is the number of in-scope files found before the cap.
	TotalFiles int `json:"totalFiles"`
}

// TableColumn describes one column. Type drives frontend cell formatting and
// xlsx cell typing: "string" | "number" | "date".
type TableColumn struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"`
	// DeriveFrom, when "filename", means the value is computed in code from the
	// source file name (never extracted by the LLM). Empty otherwise.
	DeriveFrom string `json:"deriveFrom,omitempty"`
}

// TableRow is a column-key -> value map. Reserved keys carry provenance:
//
//	_file_id    source file UUID
//	_file_name  source file name (drives filename-derived columns)
//	_source_idx 1-based citation index into ChatContext.Sources
type TableRow map[string]any

// farmIDRe captures a leading "<LETTERS> [<LETTER>] <NUMBER>" identifier from a
// case-study file name: "KZ 1", "KZ 10", "UZ K 2", "UZ S 6". Generic enough for
// any "<letters> [<letter>] <digits>" naming scheme.
var farmIDRe = regexp.MustCompile(`^([A-Za-z]{1,4})(?:\s+([A-Za-z]))?\s+(\d+)`)

// deriveFarmIDFromFilename computes an ID column value for a file name. It
// strips any directory and extension, then extracts the leading ID token. The
// generic default is the cleaned stem; the regex handles the KZ / UZ-K / UZ-S
// scheme ("KZ 4 Bayserke-Agro.docx" -> "KZ 4").
func deriveFarmIDFromFilename(name string) string {
	base := filepath.Base(name)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = strings.TrimSpace(stem)
	if m := farmIDRe.FindStringSubmatch(stem); m != nil {
		parts := []string{m[1]}
		if m[2] != "" {
			parts = append(parts, m[2])
		}
		parts = append(parts, m[3])
		return strings.Join(parts, " ")
	}
	return stem
}
