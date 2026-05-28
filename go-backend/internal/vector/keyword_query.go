package vector

import (
	"strings"
	"unicode"
)

// buildOrTokensExpr converts a free-text query remainder (the part left over
// after extractQuotedPhrases pulled out "..."-quoted phrases) into a string
// suitable for Postgres `to_tsquery`, where tokens are joined with " | ".
//
// Tokenization rule: a token is a maximal run of Unicode letters, digits,
// hyphens, or underscores. Everything else is a token boundary. This keeps
// hyphenated and underscore-bearing identifiers intact (e.g. "Support-Chatbot",
// "snake_case_id") while stripping punctuation, quotes, and `to_tsquery`
// operator characters (& | ! ( ) : * <).
//
// Postgres `to_tsquery` itself handles hyphens inside a token by emitting both
// the compound and its parts (e.g. "support-chatbot" expands to
// `'support-chatbot' <-> 'support' <-> 'chatbot'`), which mirrors how
// `to_tsvector('german')` indexed the document side, so hyphenated tokens
// match document-side compounds. Verified against pgvector 0.8.2 / Postgres 18.
//
// Returns ("", false) if no usable tokens remain after splitting.
func buildOrTokensExpr(remainder string) (tokens string, ok bool) {
	parts := strings.FieldsFunc(remainder, isTokenBoundary)
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, " | "), true
}

func isTokenBoundary(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return false
	}
	if r == '-' || r == '_' {
		return false
	}
	return true
}
