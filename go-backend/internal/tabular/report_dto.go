package tabular

import (
	"sort"

	"github.com/justrag/go-backend/internal/promptsafety"
)

// maxDTOSamples/maxDTOValueSet cap the per-column sample/value-set arrays
// the "Tabellen" file-detail panel receives — the same shape the LLM-facing
// schema summary already caps to keep spreadsheet-derived cell content from
// growing unbounded in a stored/served payload.
const (
	maxDTOSamples  = 3
	maxDTOValueSet = 20
)

// FileTabularDTO is the wire shape of GET /api/kb/{id}/files/{fileId}/tabular
// (the "Tabellen" file-detail panel): the raw per-file ingest report plus
// the tabular_catalog projection for the file, joined by BuildFileTabularDTO.
//
// Report is nil when files.parse_report is NULL — a non-spreadsheet file, or
// a spreadsheet file that has not (yet) been materialised — and marshals to
// the JSON literal `"report":null` rather than being omitted, so a client
// can tell "no report yet" apart from a key it forgot to ask for. Tables is
// never nil — an empty slice marshals to `[]`.
type FileTabularDTO struct {
	Report *ParseReport `json:"report"`
	Tables []TableDTO   `json:"tables"`
}

// TableDTO is one materialized sheet/region — one tabular_catalog row.
type TableDTO struct {
	SheetIndex  int         `json:"sheet_index"`
	RegionIndex int         `json:"region_index"`
	SheetName   string      `json:"sheet_name"`
	TableName   string      `json:"table_name"`
	SheetKind   string      `json:"sheet_kind"`
	Hidden      bool        `json:"hidden"`
	HeaderRow   int         `json:"header_row"` // -1 = none
	RowCount    int64       `json:"row_count"`
	Columns     []ColumnDTO `json:"columns"`
}

// ColumnDTO is one materialized column: the Phase-1 ColumnSpec joined with
// its Phase-2 ColumnStat counterpart by Name. ShadowOf (set on the shadow
// column, e.g. "baujahr_num" naming "baujahr") comes from the ColumnSpec
// side; ShadowColumn (set on the primary column, e.g. "baujahr" naming
// "baujahr_num") comes from the ColumnStat side — see CatalogEntry's doc
// comment on why the two are asymmetric.
type ColumnDTO struct {
	Original       string   `json:"original"`
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	Role           string   `json:"role,omitempty"`
	Description    string   `json:"description,omitempty"`
	ShadowOf       string   `json:"shadow_of,omitempty"`
	ShadowColumn   string   `json:"shadow_column,omitempty"`
	NullCount      int64    `json:"null_count"`
	DistinctCount  int64    `json:"distinct_count"`
	CoercionFailed int64    `json:"coercion_failed"`
	Samples        []string `json:"samples,omitempty"`   // <= maxDTOSamples, instruction-filtered
	ValueSet       []string `json:"value_set,omitempty"` // <= maxDTOValueSet, instruction-filtered
}

// BuildFileTabularDTO joins report (the file's persisted ingest report, nil
// for a non-spreadsheet file) with entries (the file's tabular_catalog rows)
// into the wire shape for GET /api/kb/{id}/files/{fileId}/tabular.
//
// Every entry's ColumnStats are joined to its Columns by Name. Free-text
// fields that ultimately come from spreadsheet cells — column Description,
// Samples, ValueSet, and each SheetReport.Notes entry — are passed through
// promptsafety.LooksLikeInstruction first and dropped on a match: cells are
// attacker-controlled data, not instructions, and this is the second,
// deterministic line of defense before that text reaches stored metadata or
// the frontend (the first being the system prompt's own "data, not
// instructions" warning wherever this content later reaches an LLM). Tables
// are sorted by (sheet_index, region_index) — materialisation order —
// independent of tabular_catalog's created_at.
func BuildFileTabularDTO(report *ParseReport, entries []CatalogEntry) FileTabularDTO {
	dto := FileTabularDTO{
		Report: filterReport(report),
		Tables: make([]TableDTO, 0, len(entries)),
	}

	for _, e := range entries {
		dto.Tables = append(dto.Tables, buildTableDTO(e))
	}

	sort.Slice(dto.Tables, func(i, j int) bool {
		a, b := dto.Tables[i], dto.Tables[j]
		if a.SheetIndex != b.SheetIndex {
			return a.SheetIndex < b.SheetIndex
		}
		return a.RegionIndex < b.RegionIndex
	})

	return dto
}

// filterReport returns a copy of report with every SheetReport.Notes entry
// that looks like an instruction attempt dropped. report itself (and its
// Sheets slice) is never mutated.
func filterReport(report *ParseReport) *ParseReport {
	if report == nil {
		return nil
	}
	out := *report
	out.Sheets = make([]SheetReport, len(report.Sheets))
	for i, s := range report.Sheets {
		out.Sheets[i] = s
		out.Sheets[i].Notes = filterInstructionStrings(s.Notes, 0)
	}
	return &out
}

func buildTableDTO(e CatalogEntry) TableDTO {
	statsByName := make(map[string]ColumnStat, len(e.ColumnStats))
	for _, s := range e.ColumnStats {
		statsByName[s.Name] = s
	}

	cols := make([]ColumnDTO, 0, len(e.Columns))
	for _, c := range e.Columns {
		col := ColumnDTO{
			Original:    c.Original,
			Name:        c.Name,
			Type:        string(c.Type),
			Role:        c.Role,
			Description: filterInstructionString(c.Description),
			ShadowOf:    c.ShadowOf,
		}
		if stat, ok := statsByName[c.Name]; ok {
			col.ShadowColumn = stat.ShadowColumn
			col.NullCount = stat.NullCount
			col.DistinctCount = stat.DistinctCount
			col.CoercionFailed = stat.CoercionFailed
			col.Samples = filterInstructionStrings(stat.Samples, maxDTOSamples)
			col.ValueSet = filterInstructionStrings(stat.ValueSet, maxDTOValueSet)
		}
		cols = append(cols, col)
	}

	return TableDTO{
		SheetIndex:  e.SheetIndex,
		RegionIndex: e.RegionIndex,
		SheetName:   e.SheetName,
		TableName:   e.TableName,
		SheetKind:   e.SheetKind,
		Hidden:      e.Hidden,
		HeaderRow:   e.HeaderRow,
		RowCount:    e.RowCount,
		Columns:     cols,
	}
}

// filterInstructionString drops s (returns "") when it looks like an
// instruction rather than data.
func filterInstructionString(s string) string {
	if s == "" || promptsafety.LooksLikeInstruction(s) {
		return ""
	}
	return s
}

// filterInstructionStrings drops every instruction-like entry from items and
// caps the result at max (max <= 0 means no cap). Returns nil (never an
// empty non-nil slice) when nothing survives, so the field's `omitempty`
// json tag omits it.
func filterInstructionStrings(items []string, max int) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, s := range items {
		if promptsafety.LooksLikeInstruction(s) {
			continue
		}
		out = append(out, s)
		if max > 0 && len(out) >= max {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
