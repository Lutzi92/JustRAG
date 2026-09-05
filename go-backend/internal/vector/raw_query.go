package vector

import "strings"

// effectiveRawQuery returns the trimmed raw utterance to search as the
// extra RRF lane in Search(), or "" when the lane should be skipped: raw
// is empty/whitespace-only, or raw equals final after trimming both sides
// (a single-turn question, or a follow-up CondenseFollowUp left
// unchanged) — in either case the extra list would just duplicate the
// condensed-query search at the cost of one more embedding call and one
// more BM25 query.
func effectiveRawQuery(raw, final string) string {
	r := strings.TrimSpace(raw)
	if r == "" || r == strings.TrimSpace(final) {
		return ""
	}
	return r
}
