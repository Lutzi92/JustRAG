package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/prompts"
)

// TabularSQLRequest is one call's input to the tabular router's
// SQL-generation step (spec §5.1 step 4): the resolved schema text for the
// KB's tabular.* tables, any stored values the router already matched to a
// column (e.g. "Liegenschaft = 'Goethestraße 55' (Gebäudedaten › Sheet1, 3
// rows)"), and the user's question. On a repair round, PreviousSQL and
// Failure additionally carry the failed attempt and its verbatim failure
// text ("0 rows", a DB error message capped to 500 chars, or "all
// aggregates NULL").
type TabularSQLRequest struct {
	Lang        string
	TodayISO    string
	SchemaText  string
	Matched     []string
	Question    string
	PreviousSQL string
	Failure     string
}

// TabularSQLProposal is the LLM's structured response: either one SELECT
// statement plus a rationale and confidence, or a null SQL (with confidence
// 0) when the listed tabular.* tables cannot answer the question.
type TabularSQLProposal struct {
	SQL        *string `json:"sql"`
	Rationale  string  `json:"rationale"`
	Confidence float64 `json:"confidence"`
}

// tabularSQLSpec is the Structured-Outputs contract for GenerateTabularSQL,
// mirroring TabularSQLProposal's JSON shape.
var tabularSQLSpec = &StructuredSpec{
	Name: "tabular_sql",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"sql": {"type": ["string", "null"]},
			"rationale": {"type": "string"},
			"confidence": {"type": "number", "minimum": 0, "maximum": 1}
		},
		"required": ["sql", "rationale", "confidence"],
		"additionalProperties": false
	}`),
}

// tabularSQLCallTimeout bounds one SQL-generation LLM call. The router
// dispatches this once per question, and again per repair round, so a
// slow or stuck backend must not stall the whole chat turn.
const tabularSQLCallTimeout = 10 * time.Second

// GenerateTabularSQL asks the fast-tier model to write one Postgres SELECT
// against the KB's tabular.* schema (or to decline with sql:null). When
// req.PreviousSQL is non-empty, the user prompt additionally carries the
// failed attempt and its failure text (prompts.TabularSQLRepairPrompt),
// selecting the repair path instead of a first attempt.
//
// A malformed or empty completion is a real error here — not a
// silently-empty proposal — so a caller can tell "the model declined
// (SQL == nil)" apart from "the call or parse broke" and retry/abstain
// accordingly. Metrics recording is Task 7's responsibility, not this
// call's.
func GenerateTabularSQL(ctx context.Context, resolver *ConfigResolver, req TabularSQLRequest, kbID, modelOverride string) (TabularSQLProposal, error) {
	ctx, cancel := context.WithTimeout(ctx, tabularSQLCallTimeout)
	defer cancel()

	sys := prompts.TabularSQLSystemPrompt(req.Lang, req.TodayISO)
	user := prompts.TabularSQLUserPrompt(req.Lang, req.SchemaText, req.Matched, req.Question)
	if req.PreviousSQL != "" {
		user += "\n\n" + prompts.TabularSQLRepairPrompt(req.Lang, req.PreviousSQL, req.Failure)
	}

	res, err := resolver.structuredCompletionFn(ctx, user, sys, kbID, modelOverride, tabularSQLSpec)
	if err != nil {
		return TabularSQLProposal{}, fmt.Errorf("tabular_sql: completion: %w", err)
	}
	if res == nil || strings.TrimSpace(res.Content) == "" {
		return TabularSQLProposal{}, fmt.Errorf("tabular_sql: empty completion")
	}

	out, perr := parseTabularSQLJSON(res.Content)
	if perr != nil {
		return TabularSQLProposal{}, fmt.Errorf("tabular_sql: parse: %w", perr)
	}

	if out.Confidence < 0 {
		out.Confidence = 0
	} else if out.Confidence > 1 {
		out.Confidence = 1
	}

	return out, nil
}

// parseTabularSQLJSON tolerantly parses the completion body into a
// TabularSQLProposal: a direct unmarshal first, then — if the model
// wrapped the JSON in markdown fences or trailing prose — a fallback that
// slices from the first '{' to the last '}'. Same two-step strategy as
// parseSheetProfileJSON / parseKGJSON / parsePlanQueriesJSON in this
// package.
//
// {"sql": ""} is normalised to SQL == nil (normalizeEmptySQL): an
// empty-but-non-nil string is not a usable SQL statement, and treating it
// as distinct from null would push that special case onto every caller
// that only wants to branch on "did the model propose SQL or not".
func parseTabularSQLJSON(text string) (TabularSQLProposal, error) {
	var out TabularSQLProposal
	trimmed := strings.TrimSpace(text)
	if err := json.Unmarshal([]byte(trimmed), &out); err == nil {
		return normalizeEmptySQL(out), nil
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(trimmed[start:end+1]), &out); err == nil {
			return normalizeEmptySQL(out), nil
		}
	}
	return TabularSQLProposal{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}

// normalizeEmptySQL collapses an empty-string SQL to nil, see
// parseTabularSQLJSON's doc comment.
func normalizeEmptySQL(p TabularSQLProposal) TabularSQLProposal {
	if p.SQL != nil && strings.TrimSpace(*p.SQL) == "" {
		p.SQL = nil
	}
	return p
}
