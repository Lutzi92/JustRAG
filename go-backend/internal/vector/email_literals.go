package vector

import "regexp"

// emailLiteralRE matches email-shaped substrings. Must follow Postgres's
// default text-search parser definition closely so that `phraseto_tsquery`
// downstream produces the same token shape as the document-side `to_tsvector`.
//
// Local-part:  one or more of [A-Za-z0-9._+-]
// `@`
// Domain:      one or more labels of [A-Za-z0-9-]+ separated by dots, with at
//
//	least one dot (so "user@host" alone is not enough — we want a
//	real TLD-bearing literal worth promoting to a phrase).
//
// Bounds: anchored on word boundaries via lookaround-equivalent — we instead
// require the surrounding chars to be non-alnum or absent. RE2 has no
// lookaround so we use \b which in Go's regexp is a word-boundary on
// [A-Za-z0-9_].
var emailLiteralRE = regexp.MustCompile(`\b[A-Za-z0-9._+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+\b`)

// extractEmailLiterals returns email-shaped substrings of `s` along with `s`
// with those substrings removed (replaced by the empty string). Downstream
// tokenisation in runKeywordSearch collapses any resulting whitespace runs
// via strings.FieldsFunc, so the empty-replacement choice is observably
// identical at the BM25 SQL boundary to a single-space replacement.
func extractEmailLiterals(s string) (emails []string, rest string) {
	matches := emailLiteralRE.FindAllString(s, -1)
	if len(matches) == 0 {
		return nil, s
	}
	rest = emailLiteralRE.ReplaceAllString(s, "")
	return matches, rest
}
