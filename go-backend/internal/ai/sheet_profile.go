package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
)

// SheetProfileRequest is one call's input: a sampled grid excerpt from one
// table region of a sheet, plus the heuristic profiler's own proposal for
// the model to confirm or correct. Grid is capped by the caller (≤ 30 rows
// × the region's kept columns per the spec; profile.BuildLLMRequest does
// that capping) — this type carries whatever the caller already trimmed.
type SheetProfileRequest struct {
	FileName, SheetName string
	Grid                [][]string // ≤ 30 rows × kept columns, row 0.. as in the sheet; header-candidate rows included
	RowOffset           int        // absolute index of Grid[0]
	Heuristic           SheetProfileProposal
}

// SheetProfileProposal is the LLM's structured response: a possibly
// corrected sheet kind/header-rows plus a per-column description and role
// proposal. internal/tabular/profile.ApplyLLM decides how much of this to
// actually accept (see that function's doc comment for the gating rules).
type SheetProfileProposal struct {
	Kind       string               `json:"kind"`        // table|form|prose
	HeaderRows []int                `json:"header_rows"` // absolute
	Columns    []SheetProfileColumn `json:"columns"`
	Confidence float64              `json:"confidence"`
}

// SheetProfileColumn is one column's proposal within a SheetProfileProposal.
type SheetProfileColumn struct {
	Index       int    `json:"index"`
	Name        string `json:"name"`
	Role        string `json:"role"` // id|measure|date|category|bool|text
	Description string `json:"description"`
}

// sheetProfileSpec is the Structured-Outputs contract for
// ProfileTableRegion, mirroring SheetProfileProposal's JSON shape.
var sheetProfileSpec = &StructuredSpec{
	Name: "sheet_profile",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"kind": {"type": "string", "enum": ["table", "form", "prose"]},
			"header_rows": {"type": "array", "items": {"type": "integer"}},
			"confidence": {"type": "number"},
			"columns": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"index": {"type": "integer"},
						"name": {"type": "string"},
						"role": {"type": "string", "enum": ["id", "measure", "date", "category", "bool", "text"]},
						"description": {"type": "string"}
					},
					"required": ["index", "name", "role", "description"],
					"additionalProperties": false
				}
			}
		},
		"required": ["kind", "header_rows", "confidence", "columns"],
		"additionalProperties": false
	}`),
}

// sheetProfileCallTimeout bounds one profiler LLM call. Ingestion runs one
// of these per table region (not per file), so a slow/stuck backend must
// not stall the pipeline indefinitely.
const sheetProfileCallTimeout = 10 * time.Second

// ProfileTableRegion asks the fast-tier model to confirm or correct the
// heuristic profiler's read of one table region: sheet kind, header rows,
// and a per-column role + one-sentence description. Best-effort — on any
// failure (call error, empty completion, parse failure) it returns the
// zero SheetProfileProposal and a non-nil error; the caller (Phase 2's
// ingestion wiring) is expected to keep the heuristic profile unchanged
// and proceed, the same way ExtractKG's callers treat KG-extraction
// failures as non-fatal.
func ProfileTableRegion(ctx context.Context, resolver *ConfigResolver, req SheetProfileRequest, kbID, lang, modelOverride string) (SheetProfileProposal, error) {
	ctx, cancel := context.WithTimeout(ctx, sheetProfileCallTimeout)
	defer cancel()

	var grid strings.Builder
	for i, row := range req.Grid {
		cells := make([]string, len(row))
		for j, c := range row {
			cells[j] = "'" + strings.ReplaceAll(firstNChars(c, 60), "\n", " ") + "'"
		}
		fmt.Fprintf(&grid, "%d: %s\n", req.RowOffset+i, strings.Join(cells, " | "))
	}

	heuristicJSON, err := json.Marshal(req.Heuristic)
	if err != nil {
		// Marshaling our own struct cannot realistically fail, but guard
		// anyway rather than sending a broken prompt.
		return SheetProfileProposal{}, fmt.Errorf("sheet_profile: marshal heuristic: %w", err)
	}

	sys := prompts.SheetProfileSystemPrompt(lang)
	user := prompts.SheetProfileUserPrompt(req.FileName, req.SheetName, grid.String(), string(heuristicJSON))

	res, err := resolver.structuredCompletionFn(ctx, user, sys, kbID, modelOverride, sheetProfileSpec)
	if err != nil {
		observability.RecordTabularProfileLLM("error")
		return SheetProfileProposal{}, fmt.Errorf("sheet_profile: completion: %w", err)
	}
	if res == nil || strings.TrimSpace(res.Content) == "" {
		observability.RecordTabularProfileLLM("error")
		return SheetProfileProposal{}, fmt.Errorf("sheet_profile: empty completion")
	}

	out, perr := parseSheetProfileJSON(res.Content)
	if perr != nil {
		observability.RecordTabularProfileLLM("parse_error")
		return SheetProfileProposal{}, fmt.Errorf("sheet_profile: parse: %w", perr)
	}

	observability.RecordTabularProfileLLM("ok")
	return out, nil
}

// parseSheetProfileJSON tolerantly parses the completion body into a
// SheetProfileProposal: a direct unmarshal first, then — if the model
// wrapped the JSON in markdown fences or trailing prose — a fallback that
// slices from the first '{' to the last '}'. Same two-step strategy as
// parseKGJSON / parsePlanQueriesJSON in this package.
func parseSheetProfileJSON(text string) (SheetProfileProposal, error) {
	var out SheetProfileProposal
	trimmed := strings.TrimSpace(text)
	if err := json.Unmarshal([]byte(trimmed), &out); err == nil {
		return out, nil
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(trimmed[start:end+1]), &out); err == nil {
			return out, nil
		}
	}
	return SheetProfileProposal{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}
