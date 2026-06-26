package kg

import (
	"strings"
	"unicode"
)

// TokenizeForGraph splits a query into distinct, order-preserving tokens for
// alias matching: Unicode word-split (so "PPM-Team" → ["PPM","Team"]), tokens
// shorter than 3 runes dropped, first-seen dedup. Shared by the chat router
// and the scoped-graph read so both match aliases identically.
func TokenizeForGraph(query string) []string {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	out := make([]string, 0, 8)
	seen := make(map[string]bool, 8)
	current := strings.Builder{}
	flush := func() {
		t := current.String()
		current.Reset()
		if len(t) < 3 {
			return
		}
		if seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}
