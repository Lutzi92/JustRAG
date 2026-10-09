package globalsearch

import (
	"context"
	"fmt"
	"strconv"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// Text matching shared by the name-like columns: topic names, file names and
// chat titles all use it unchanged. Keep it free of anything group-specific.
//
// A column matches the query when it contains q as a literal substring
// (escaped ILIKE) OR is fuzzily similar to it (pg_trgm word similarity). Each
// hit is labelled with the best tier it reached, and the tiers rank the
// results: prefix > substring > fuzzy, fuzzy by similarity descending.
// Migration 0084 provides the pg_trgm extension and the GIN trigram indexes
// that let the planner answer both arms from an index.

// FuzzyThreshold is the minimum pg_trgm word_similarity(q, column) for a
// fuzzy hit. Applied per transaction via pg_trgm.word_similarity_threshold,
// because only the operator form (q <% column) can use the GIN index; the
// function form word_similarity(q, column) >= x cannot.
//
// 0.4, below pg_trgm's own default word_similarity_threshold of 0.6, because
// the default misses the most common typo, a swap of two adjacent letters.
// Measured on Postgres 18.6 / pg_trgm 1.6: "Statsitik" -> "Statistik" scores
// 0.429, so 0.6 would not find it; 0.4 does, with a small margin. The other
// typos in TestTypoOracle score well above it ("Physk"/"Physik" 0.667,
// "protkoll.pdf"/"Protokoll.pdf" 0.667, "Rechtwissenschaft"/
// "Rechtswissenschaft" 0.762), and its negatives — unrelated words and words
// sharing only a prefix ("Stadtplanung" for "Statsitik") — stay below it.
// Lower would admit more noise; fuzzy hits rank after every literal hit, so
// the cost of an occasional loose match is one extra row at the bottom.
const FuzzyThreshold = 0.4

// Match tiers, as reported in TopicHit.Match / SourceHit.Match.
const (
	MatchPrefix    = "prefix"
	MatchSubstring = "substring"
	MatchFuzzy     = "fuzzy"
)

// textMatch renders the SQL fragments that match one text column against the
// query. It holds only placeholder names, never values: the three arguments
// come from matchArgs and are bound like any other parameter.
type textMatch struct {
	containsParam string // escaped '%q%' pattern
	prefixParam   string // escaped 'q%' pattern
	rawParam      string // the query text itself, for the trigram operators
}

// searchMatch is the placeholder layout every search query uses. $1..$3 are
// visibleKBsCTE's, $6 is the group limit.
var searchMatch = textMatch{containsParam: "$4", prefixParam: "$5", rawParam: "$7"}

// hasPrefix: the column starts with q (case-insensitive, literal).
func (m textMatch) hasPrefix(col string) string {
	return col + ` ILIKE ` + m.prefixParam + ` ESCAPE '\'`
}

// contains: the column contains q (case-insensitive, literal). NULL columns
// yield NULL, which OR and CASE treat as false — deliberately no COALESCE,
// which would hide the column from its trigram index.
func (m textMatch) contains(col string) string {
	return col + ` ILIKE ` + m.containsParam + ` ESCAPE '\'`
}

// fuzzy: q is word-similar to some extent of the column, at the transaction's
// pg_trgm.word_similarity_threshold (see setFuzzyThreshold). Index-supported
// by gin_trgm_ops.
func (m textMatch) fuzzy(col string) string {
	return m.rawParam + ` <% ` + col
}

// similarity is the ranking score for fuzzy hits.
func (m textMatch) similarity(col string) string {
	return `word_similarity(` + m.rawParam + `, ` + col + `)`
}

// matches is the WHERE arm for a column that is matched both ways.
func (m textMatch) matches(col string) string {
	return `(` + m.contains(col) + ` OR ` + m.fuzzy(col) + `)`
}

// matchArgs derives the pattern arguments from the validated query text, in
// the order of textMatch's fields.
func matchArgs(text string) (contains, prefix, raw string) {
	escaped := pgxutil.EscapeLike(text)
	return "%" + escaped + "%", escaped + "%", text
}

// setFuzzyThreshold sets pg_trgm.word_similarity_threshold for the current
// TRANSACTION only (set_config's is_local = true), so the value never leaks
// into a pooled connection's next user. Must run inside the transaction that
// executes the search queries.
func setFuzzyThreshold(ctx context.Context, q pgxutil.Querier) error {
	_, err := q.Exec(ctx, `SELECT set_config('pg_trgm.word_similarity_threshold', $1, true)`,
		strconv.FormatFloat(FuzzyThreshold, 'f', -1, 64))
	if err != nil {
		return fmt.Errorf("set fuzzy threshold: %w", err)
	}
	return nil
}
