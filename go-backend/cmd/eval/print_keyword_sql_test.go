package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

// TestValidateKeywordSQLFlags_RequiresKBID guards the one operator mistake
// the diagnostic mode can make silently: asking for the keyword SQL without
// naming a KB. Without the guard the render call resolves nothing and the
// tool would print an error from three layers down (or, worse, a statement
// against whichever table the probe happened to find).
//
// Mutation this test catches: dropping the `kbID == ""` branch from
// validateKeywordSQLFlags (the empty-kb case would then return nil).
func TestValidateKeywordSQLFlags_RequiresKBID(t *testing.T) {
	if err := validateKeywordSQLFlags(""); err == nil {
		t.Fatal("want an error when --kb-id is missing, got nil")
	}
	if err := validateKeywordSQLFlags("5ca1e000-0000-4000-8000-000000000001"); err != nil {
		t.Fatalf("want no error for a complete invocation, got %v", err)
	}
}

// TestWriteKeywordSQLJSON_PrintsBothBuilders pins the tool's output
// contract: ONE machine-readable JSON document on stdout carrying BOTH
// builders' statements, each with a placeholder-free executable form. The
// scale-check script (eval/fixtures/bm25-scale/time-keyword-sql.sh) parses
// exactly this shape and feeds executable_sql to EXPLAIN.
//
// Mutation this test catches: emitting a human-readable dump instead of JSON,
// or writing only the configured mode (the array would then hold one entry).
func TestWriteKeywordSQLJSON_PrintsBothBuilders(t *testing.T) {
	diag := vector.KeywordSQLDiagnostics{
		KBID:      "5ca1e000-0000-4000-8000-000000000001",
		Query:     "Zugriffsrechte",
		TableName: "document_chunks_768",
		Dim:       768,
		PgConfig:  "german",
		Limit:     50,
		Modes: []vector.RenderedKeywordSQL{
			{Mode: vector.KeywordScoringTsRank, OK: true, SQL: "SELECT ts_rank($1)", Args: []string{"a"}, ExecutableSQL: "SELECT ts_rank('a')"},
			{Mode: vector.KeywordScoringBM25, OK: true, SQL: "SELECT bm25 $1", Args: []string{"a"}, ExecutableSQL: "SELECT bm25 'a'"},
		},
	}

	var buf bytes.Buffer
	if err := writeKeywordSQLJSON(&buf, diag); err != nil {
		t.Fatalf("writeKeywordSQLJSON: %v", err)
	}

	var got vector.KeywordSQLDiagnostics
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not a single JSON document (%v):\n%s", err, buf.String())
	}
	if len(got.Modes) != 2 {
		t.Fatalf("want both builders in the output, got %d mode(s)", len(got.Modes))
	}
	if got.Modes[0].Mode != vector.KeywordScoringTsRank || got.Modes[1].Mode != vector.KeywordScoringBM25 {
		t.Errorf("want [ts_rank bm25], got [%s %s]", got.Modes[0].Mode, got.Modes[1].Mode)
	}
	for _, m := range got.Modes {
		if strings.Contains(m.ExecutableSQL, "$") {
			t.Errorf("mode %s: executable_sql still carries a placeholder: %q", m.Mode, m.ExecutableSQL)
		}
	}
	if got.TableName != "document_chunks_768" || got.Dim != 768 {
		t.Errorf("resolved table/dim not carried through: %q/%d", got.TableName, got.Dim)
	}
}
