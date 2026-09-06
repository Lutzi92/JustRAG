package vector

import (
	"strings"
	"testing"
)

// TestRenderKeywordArmSQLModes_RendersBothBuilders pins the diagnostic
// renderer's core contract: ONE call yields BOTH scoring modes' SQL for the
// same input, and the two share the byte-identical candidate WHERE clause
// (the property the ts_rank-vs-bm25 A/B rests on — "the candidate set never
// differs between modes, only the ranking does").
//
// Mutation this test catches: rendering only the configured mode (dropping
// either entry from the returned slice), or rebuilding the candidate clause
// per mode instead of once (which would let the two WHERE texts drift).
func TestRenderKeywordArmSQLModes_RendersBothBuilders(t *testing.T) {
	in := keywordSQLInput{
		TableName: "document_chunks_768",
		Query:     `Zugriffsrechte "Stud.IP" Portal`,
		KbID:      "5ca1e000-0000-4000-8000-000000000001",
		PgConfig:  "german",
		Limit:     50,
		SimpleArm: true,
		Dim:       768,
		K1:        1.2,
		B:         0.75,
	}

	modes := renderKeywordArmSQLModes(in)
	if len(modes) != 2 {
		t.Fatalf("want 2 rendered modes, got %d", len(modes))
	}
	if modes[0].Mode != KeywordScoringTsRank || modes[1].Mode != KeywordScoringBM25 {
		t.Fatalf("want [ts_rank bm25], got [%s %s]", modes[0].Mode, modes[1].Mode)
	}
	for _, m := range modes {
		if !m.OK {
			t.Fatalf("mode %s: want ok=true for a non-empty query", m.Mode)
		}
		if strings.TrimSpace(m.SQL) == "" {
			t.Fatalf("mode %s: empty SQL", m.Mode)
		}
	}

	if !strings.Contains(modes[0].SQL, "ts_rank(vector_index,") {
		t.Errorf("ts_rank mode must score with ts_rank(); got:\n%s", modes[0].SQL)
	}
	if !strings.Contains(modes[1].SQL, `"bm25_kb_stats_768"`) || !strings.Contains(modes[1].SQL, `"bm25_term_stats_768"`) {
		t.Errorf("bm25 mode must read the dim-keyed stats tables; got:\n%s", modes[1].SQL)
	}

	// Both builders embed cc.whereClause verbatim; extracting it from each
	// rendered statement and comparing is the closest a unit test gets to
	// "the candidate set is identical".
	wantWhere := extractWhereText(t, modes[0].SQL)
	gotWhere := extractWhereText(t, modes[1].SQL)
	if wantWhere != gotWhere {
		t.Errorf("candidate WHERE clauses differ between modes:\nts_rank: %q\nbm25:    %q", wantWhere, gotWhere)
	}
}

// extractWhereText returns the text between the first "WHERE " and the next
// newline-terminated clause keyword, normalised on whitespace.
func extractWhereText(t *testing.T, sqlText string) string {
	t.Helper()
	i := strings.Index(sqlText, "WHERE ")
	if i < 0 {
		t.Fatalf("no WHERE in:\n%s", sqlText)
	}
	rest := sqlText[i+len("WHERE "):]
	for _, stop := range []string{"\n\t\t\tORDER BY", "\n\t\t)", "\nORDER BY"} {
		if j := strings.Index(rest, stop); j >= 0 {
			rest = rest[:j]
		}
	}
	return strings.Join(strings.Fields(rest), " ")
}

// TestRenderKeywordArmSQLModes_EmptyQueryIsNotOK pins the "nothing to search
// for" path: an empty query produces no SQL in either mode rather than a
// half-built statement.
//
// Mutation this test catches: dropping the `ok` result of buildKeywordSQL and
// reporting an empty string as a valid statement.
func TestRenderKeywordArmSQLModes_EmptyQueryIsNotOK(t *testing.T) {
	modes := renderKeywordArmSQLModes(keywordSQLInput{
		TableName: "document_chunks_768",
		Query:     "   ",
		KbID:      "5ca1e000-0000-4000-8000-000000000001",
		PgConfig:  "german",
		Limit:     50,
		Dim:       768,
	})
	if len(modes) != 2 {
		t.Fatalf("want 2 rendered modes, got %d", len(modes))
	}
	for _, m := range modes {
		if m.OK || m.SQL != "" || m.ExecutableSQL != "" {
			t.Errorf("mode %s: want ok=false and empty SQL for a query with no remainder and no phrases, got ok=%v sql=%q", m.Mode, m.OK, m.SQL)
		}
	}
}

// TestInlineKeywordSQLArgs_HandlesMultiDigitPlaceholders pins the two
// properties EXPLAIN-ability depends on: every placeholder is replaced (a
// leftover $N is not runnable in psql), and $1 must not eat the "$1" prefix
// of "$11".
//
// Mutation this test catches: substituting placeholders with a loop of
// strings.ReplaceAll in ascending index order (which turns "$11" into
// "'kb'1"), or skipping the quote-escaping so a literal containing an
// apostrophe silently truncates the statement.
func TestInlineKeywordSQLArgs_HandlesMultiDigitPlaceholders(t *testing.T) {
	sqlText := "SELECT $1::uuid, $11, $2, $10"
	args := make([]any, 11)
	for i := range args {
		args[i] = "a"
	}
	args[0] = "kb"
	args[1] = "german"
	args[9] = "ten"
	args[10] = "O'Brien"

	got := inlineKeywordSQLArgs(sqlText, args)
	want := "SELECT 'kb'::uuid, 'O''Brien', 'german', 'ten'"
	if got != want {
		t.Errorf("inlineKeywordSQLArgs:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "$") {
		t.Errorf("executable SQL still carries a placeholder: %q", got)
	}
}

// TestInlineKeywordSQLArgs_DoesNotRescanSubstitutedLiterals pins the
// single-pass property: an argument whose own text looks like a placeholder
// must survive verbatim. A user question really can contain "$3" (a price, a
// shell snippet), and it is bound as query text into the keyword SQL.
//
// Mutation this test catches: implementing the substitution as a loop of
// strings.ReplaceAll over the whole statement — the "$3" inside argument 1's
// literal would then be replaced when placeholder 3's turn came round,
// producing a statement that is plausible and wrong.
func TestInlineKeywordSQLArgs_DoesNotRescanSubstitutedLiterals(t *testing.T) {
	got := inlineKeywordSQLArgs("SELECT $1, $3", []any{"kostet $3 pro Monat", "unused", "third"})
	want := "SELECT 'kostet $3 pro Monat', 'third'"
	if got != want {
		t.Errorf("inlineKeywordSQLArgs:\n got %q\nwant %q", got, want)
	}
}

// TestInlineKeywordSQLArgs_LeavesOutOfRangePlaceholder pins the fail-visible
// choice for a placeholder with no argument.
//
// Mutation this test catches: substituting an empty SQL literal for an
// out-of-range index, which yields a runnable but silently wrong statement.
func TestInlineKeywordSQLArgs_LeavesOutOfRangePlaceholder(t *testing.T) {
	got := inlineKeywordSQLArgs("SELECT $1, $9", []any{"a"})
	want := "SELECT 'a', $9"
	if got != want {
		t.Errorf("inlineKeywordSQLArgs:\n got %q\nwant %q", got, want)
	}
}

// TestInlineKeywordSQLArgs_RendersStringSliceAsArrayLiteral covers the one
// non-string argument shape the keyword builder binds: FileIDs, bound as a
// []string for `file_id = ANY($N::uuid[])`.
//
// Mutation this test catches: formatting a []string with %v ("[a b]"), which
// is not valid SQL and would make a file-scoped diagnostic un-runnable.
func TestInlineKeywordSQLArgs_RendersStringSliceAsArrayLiteral(t *testing.T) {
	got := inlineKeywordSQLArgs("WHERE file_id = ANY($1::uuid[])", []any{[]string{"a", "b"}})
	want := "WHERE file_id = ANY(ARRAY['a','b']::uuid[])"
	if got != want {
		t.Errorf("inlineKeywordSQLArgs:\n got %q\nwant %q", got, want)
	}
}
