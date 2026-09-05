package vector

import (
	"bytes"
	"testing"
)

// TestEffectiveRawQuery covers effectiveRawQuery's guard: empty input,
// whitespace-only input, and raw == final modulo trimming all yield "" so
// the raw lane in Search() is skipped; anything else passes through
// trimmed. Mutation: dropping the equality check (raw == final) collapses
// the "identical modulo whitespace" case into "different" — that case
// below must fail if the guard is removed.
func TestEffectiveRawQuery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		raw, final string
		want       string
	}{
		{"empty raw", "", "Wer leitet das PPM-Team?", ""},
		{"whitespace-only raw", "   ", "Wer leitet das PPM-Team?", ""},
		{"identical modulo whitespace", "  Wer leitet das PPM-Team? ", "Wer leitet das PPM-Team?", ""},
		{"different", "und wann war der?", "Wann war der Workshop des Projekts Neue Wege mit KI?", "und wann war der?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveRawQuery(c.raw, c.final); got != c.want {
				t.Errorf("effectiveRawQuery(%q, %q) = %q, want %q", c.raw, c.final, got, c.want)
			}
		})
	}
}

// TestQueryCacheShape_RawQueryAffectsHash guards the raw-lane cache-key
// entanglement: a cached result for the condensed query alone must not be
// served when the raw lane added extra RRF lists, so presence of
// RawQuery must flip the shape hash regardless of its content.
func TestQueryCacheShape_RawQueryAffectsHash(t *testing.T) {
	t.Parallel()
	a := SearchOptions{Enhance: "rewrite", QueryType: QueryTypeComplexReasoning, FileIDs: []string{"a"}}
	b := SearchOptions{Enhance: "rewrite", QueryType: QueryTypeComplexReasoning, FileIDs: []string{"a"}, RawQuery: "und wann?"}

	ha := shapeHash(a, 15, "")
	hb := shapeHash(b, 15, "")

	if bytes.Equal(ha, hb) {
		t.Errorf("expected distinct hashes for empty vs non-empty RawQuery")
	}
}

// TestQueryCacheShape_RawQueryContentDoesNotAffectHash: only PRESENCE of
// RawQuery is hashed, not its content — the raw text itself belongs to the
// cache key's query-embedding side (each distinct raw string embeds
// differently and therefore already selects a different cache row via the
// embedding vector), not the request "shape". Two different non-empty raw
// strings must hash identically here.
func TestQueryCacheShape_RawQueryContentDoesNotAffectHash(t *testing.T) {
	t.Parallel()
	a := SearchOptions{RawQuery: "und wann?"}
	b := SearchOptions{RawQuery: "wer war das nochmal?"}

	if !bytes.Equal(shapeHash(a, 10, ""), shapeHash(b, 10, "")) {
		t.Errorf("two different non-empty RawQuery values should hash identically (presence-only)")
	}
}
