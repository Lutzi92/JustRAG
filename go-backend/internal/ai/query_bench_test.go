package ai

import (
	"strings"
	"testing"
)

// BenchmarkTruncateToTokens measures the per-chunk truncation cost during
// contextual enrichment. The byte-boundary binary search must not allocate
// per probe; if a regression reintroduces []rune materialization, the alloc
// count will jump from a small constant to O(probes).
func BenchmarkTruncateToTokens(b *testing.B) {
	// Two body sizes that bracket real contextual-enrichment inputs:
	// short (~5 KB) and long (~100 KB). Production budget is small enough
	// to force the binary search.
	short := strings.Repeat("Some example text with several words. ", 130)
	long := strings.Repeat("Some example text with several words. ", 2600)

	for _, tc := range []struct {
		name string
		s    string
	}{
		{"short_5kb", short},
		{"long_100kb", long},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = truncateToTokens(tc.s, 100)
			}
		})
	}
}
