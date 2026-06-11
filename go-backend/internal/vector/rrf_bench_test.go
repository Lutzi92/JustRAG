package vector

import (
	"fmt"
	"testing"
)

// BenchmarkFuseRRFWeighted exercises the merge-map pool on a representative
// search-time shape: 5 input lists (vector + BM25 + 3 alt-query lists) of 50
// docs each, with ~20% overlap. The pooled merge map should keep allocs
// dominated by the result slice and per-doc copies.
func BenchmarkFuseRRFWeighted(b *testing.B) {
	for _, n := range []int{50, 100, 200} {
		b.Run(fmt.Sprintf("docs_per_list=%d", n), func(b *testing.B) {
			lists := make([][]RankedDoc, 5)
			// Overlapping IDs: first 20% of each list shares with list 0.
			overlap := n / 5
			for li := range lists {
				docs := make([]RankedDoc, n)
				for i := range docs {
					var id string
					if i < overlap && li > 0 {
						id = fmt.Sprintf("doc-shared-%d", i)
					} else {
						id = fmt.Sprintf("doc-%d-%d", li, i)
					}
					docs[i] = RankedDoc{
						ID:          id,
						FileID:      fmt.Sprintf("f%d", i%20),
						VectorScore: float64(n-i) / float64(n),
					}
				}
				lists[li] = docs
			}
			weights := []float64{1.0, 1.0, 0.5, 0.5, 0.5}

			b.ReportAllocs()
			for b.Loop() {
				_ = FuseRRFWeighted(60, weights, lists...)
			}
		})
	}
}

// BenchmarkApplyBM25Floor exercises the pooled scratch struct on a
// representative shape: 25 final chunks, 20 BM25 chunks with ~half already
// in final. Should report zero allocs per op on the common path.
func BenchmarkApplyBM25Floor(b *testing.B) {
	final := make([]RankedDoc, 25)
	snapshot := make(map[string]RankedDoc, 50)
	for i := range final {
		id := fmt.Sprintf("c%d", i)
		final[i] = RankedDoc{ID: id, FileID: fmt.Sprintf("F%d", i), Score: float64(25-i) / 25.0}
		snapshot[id] = final[i]
	}
	// 20 BM25 docs: 10 already in final (different chunks of same files), 10 new.
	keywordDocs := make([]RankedDoc, 20)
	for i := range keywordDocs {
		var id, file string
		if i < 10 {
			// Same file as final, different chunk: forces in-place boost path.
			id = fmt.Sprintf("c%d", i)
			file = fmt.Sprintf("F%d", i)
		} else {
			id = fmt.Sprintf("kw%d", i)
			file = fmt.Sprintf("KF%d", i)
		}
		keywordDocs[i] = RankedDoc{ID: id, FileID: file, Score: float64(20 - i)}
		snapshot[id] = keywordDocs[i]
	}

	b.ReportAllocs()
	for b.Loop() {
		// Copy final each iteration: ApplyBM25Floor mutates the input slice
		// (in-place score boost) and we want each iteration to start from
		// the same state. Sort allocations from final[i].Score writes are
		// accounted to the work the function does, not the benchmark setup.
		f := make([]RankedDoc, len(final))
		copy(f, final)
		_, _ = ApplyBM25Floor(f, keywordDocs, snapshot, 25, 12)
	}
}
