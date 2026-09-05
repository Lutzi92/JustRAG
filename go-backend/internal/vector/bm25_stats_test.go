package vector

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGetBM25KBStatsTableName(t *testing.T) {
	cases := map[int]string{
		1536: "bm25_kb_stats",
		2560: "bm25_kb_stats_2560",
		4096: "bm25_kb_stats_4096",
		768:  "bm25_kb_stats_768",
	}
	for dim, want := range cases {
		if got := GetBM25KBStatsTableName(dim); got != want {
			t.Errorf("GetBM25KBStatsTableName(%d) = %q, want %q", dim, got, want)
		}
	}
}

func TestGetBM25TermStatsTableName(t *testing.T) {
	cases := map[int]string{
		1536: "bm25_term_stats",
		2560: "bm25_term_stats_2560",
		4096: "bm25_term_stats_4096",
		768:  "bm25_term_stats_768",
	}
	for dim, want := range cases {
		if got := GetBM25TermStatsTableName(dim); got != want {
			t.Errorf("GetBM25TermStatsTableName(%d) = %q, want %q", dim, got, want)
		}
	}
}

// TestValidVectorTable_BM25StatsTables locks in the allowlist extension: the
// new bm25_kb_stats[_<dim>] / bm25_term_stats[_<dim>] names produced above
// must be accepted, and any attempted SQL-injection suffix must still be
// rejected. Mutation: dropping the new alternation branch from the regex
// makes the accept assertions fail.
func TestValidVectorTable_BM25StatsTables(t *testing.T) {
	accept := []string{
		"bm25_kb_stats", "bm25_kb_stats_4096", "bm25_kb_stats_768",
		"bm25_term_stats", "bm25_term_stats_4096", "bm25_term_stats_768",
	}
	for _, name := range accept {
		if !validVectorTable.MatchString(name) {
			t.Errorf("validVectorTable should accept %q", name)
		}
	}
	reject := []string{
		`bm25_kb_stats_4096; DROP TABLE users`,
		"bm25_kb_stats_",
		"bm25_kb_statsx_4096",
		"bm25_term_statsx",
		"bm25_kb_stats_4096x",
	}
	for _, name := range reject {
		if validVectorTable.MatchString(name) {
			t.Errorf("validVectorTable should reject %q", name)
		}
	}
}

// TestBuildTermStatsRefreshSQL pins the ts_stat() literal built for each
// arm. Mutation (named per the brief): swap the column used for the
// "simple" arm (e.g. use vector_index instead of vector_index_simple) ->
// this test fails.
func TestBuildTermStatsRefreshSQL(t *testing.T) {
	kbID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	langSQL := buildTermStatsRefreshSQL(kbID, 768, "lang")
	wantLang := `ts_stat($$SELECT vector_index FROM "document_chunks_768" WHERE kb_id = '11111111-1111-1111-1111-111111111111'$$)`
	if !strings.Contains(langSQL, wantLang) {
		t.Errorf("lang arm SQL = %q, want to contain %q", langSQL, wantLang)
	}
	if strings.Contains(langSQL, "vector_index_simple") {
		t.Errorf("lang arm SQL must reference vector_index, not vector_index_simple: %q", langSQL)
	}

	simpleSQL := buildTermStatsRefreshSQL(kbID, 768, "simple")
	wantSimple := `ts_stat($$SELECT vector_index_simple FROM "document_chunks_768" WHERE kb_id = '11111111-1111-1111-1111-111111111111'$$)`
	if !strings.Contains(simpleSQL, wantSimple) {
		t.Errorf("simple arm SQL = %q, want to contain %q", simpleSQL, wantSimple)
	}

	// dim=1536 uses the bare document_chunks table name, mirroring
	// GetVectorTableName.
	bareSQL := buildTermStatsRefreshSQL(kbID, 1536, "lang")
	if !strings.Contains(bareSQL, `FROM "document_chunks" WHERE`) {
		t.Errorf("1536-dim SQL should use the bare table name, got %q", bareSQL)
	}
}
