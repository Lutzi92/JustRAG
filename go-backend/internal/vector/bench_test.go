package vector

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync/atomic"
	"testing"
)

// makeBenchDocs builds n RankedDocs with realistic content variety. Document
// scores decrease linearly so the input is already in descending order, which
// is what ApplyMMR / RRF / BlendRerankScores expect from upstream.
func makeBenchDocs(n int) []RankedDoc {
	r := rand.New(rand.NewSource(1)) // deterministic across runs
	vocab := []string{
		"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta",
		"iota", "kappa", "lambda", "mu", "nu", "xi", "omicron", "pi", "rho",
		"sigma", "tau", "upsilon", "phi", "chi", "psi", "omega",
	}
	docs := make([]RankedDoc, n)
	for i := range docs {
		// 30 words per doc, sampled from vocab — gives non-trivial Jaccard
		// overlap between any two docs (the worst case for MMR).
		words := make([]string, 30)
		for j := range words {
			words[j] = vocab[r.Intn(len(vocab))]
		}
		docs[i] = RankedDoc{
			ID:      fmt.Sprintf("doc-%d", i),
			Content: strings.Join(words, " "),
			FileID:  fmt.Sprintf("file-%d", i%10),
			Score:   1.0 - float64(i)/float64(n),
		}
	}
	return docs
}

func BenchmarkFormatEmbedding1536(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	emb := make([]float64, 1536)
	for i := range emb {
		emb[i] = r.Float64()*2 - 1
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		formatEmbedding(emb)
	}
}

func BenchmarkFormatEmbedding4096(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	emb := make([]float64, 4096)
	for i := range emb {
		emb[i] = r.Float64()*2 - 1
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		formatEmbedding(emb)
	}
}

func BenchmarkSanitizeUTF8Clean(b *testing.B) {
	text := "This is perfectly valid UTF-8 text with no issues at all."
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sanitizeUTF8(text)
	}
}

func BenchmarkSanitizeUTF8Dirty(b *testing.B) {
	// Text with invalid bytes scattered throughout.
	dirty := make([]byte, 1000)
	for i := range dirty {
		if i%50 == 0 {
			dirty[i] = 0xa0 // invalid Latin-1 non-breaking space
		} else {
			dirty[i] = 'a'
		}
	}
	text := string(dirty)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sanitizeUTF8(text)
	}
}

// BenchmarkApplyMMR exercises the inner candidate × selected loop, which is
// O(k * (n - k)) on top of an O(k) similarity scan per candidate, so total
// O(k^2 * n) when k ≪ n. Sweep across realistic candidate-pool sizes.
func BenchmarkApplyMMR(b *testing.B) {
	for _, n := range []int{20, 50, 100, 200} {
		docs := makeBenchDocs(n)
		k := 10
		if n < k {
			k = n
		}
		b.Run(fmt.Sprintf("n=%d_k=%d", n, k), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ApplyMMR(docs, 0.7, k)
			}
		})
	}
}

// BenchmarkFuseRRF measures the merge cost over multiple ranked lists with
// realistic overlap. Vector + BM25 fusion is the typical 2-list shape; the
// 3-list case mirrors hybrid + multi-query fan-out.
func BenchmarkFuseRRF(b *testing.B) {
	for _, n := range []int{50, 200} {
		docs := makeBenchDocs(n)
		// Build two overlapping rank lists by shuffling the same set with
		// different seeds.
		listA := make([]RankedDoc, len(docs))
		copy(listA, docs)
		r := rand.New(rand.NewSource(2))
		listB := make([]RankedDoc, len(docs))
		copy(listB, docs)
		r.Shuffle(len(listB), func(i, j int) { listB[i], listB[j] = listB[j], listB[i] })

		b.Run(fmt.Sprintf("two_lists_n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				FuseRRF(60, listA, listB)
			}
		})

		listC := make([]RankedDoc, len(docs))
		copy(listC, docs)
		r.Shuffle(len(listC), func(i, j int) { listC[i], listC[j] = listC[j], listC[i] })
		b.Run(fmt.Sprintf("three_lists_n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				FuseRRF(60, listA, listB, listC)
			}
		})
	}
}

// BenchmarkLoadSiteConfigCached measures the hot-path read on the cached
// branch (the case Search hits every call): an atomic pointer load + a time
// comparison, no DB round-trip, no singleflight contention. Establishes a
// baseline so a regression — e.g. lock contention sneaking into the read
// path — surfaces in CI rather than under production load.
//
// Sub-benchmark "ParallelHit" exercises concurrent readers to confirm the
// atomic.Pointer load stays wait-free under contention (a regression to
// RWMutex would show up here as a much larger ns/op under -cpu=N).
func BenchmarkLoadSiteConfigCached(b *testing.B) {
	svc := NewSearchService(nil, nil, nil, WithSiteConfigReader(&benchSiteConfig{}))
	ctx := context.Background()

	// Warm the cache so every benchmark iteration takes the fast path.
	_ = svc.loadSiteConfigCached(ctx)

	b.Run("CacheHit", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = svc.loadSiteConfigCached(ctx)
		}
	})

	b.Run("ParallelHit", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = svc.loadSiteConfigCached(ctx)
			}
		})
	})
}

// benchSiteConfig is an allocation-free SiteConfigReader stub for the
// cached-path benchmark. Returns nil for every key so loadSiteConfig
// (called only on the cold path or once during warm-up) yields
// DefaultConfig().
type benchSiteConfig struct {
	calls atomic.Int64
}

func (s *benchSiteConfig) GetSiteConfigValue(_ context.Context, _ string) (*string, error) {
	s.calls.Add(1)
	return nil, nil
}

// BenchmarkBlendRerankScores measures the min-max + blend pass. Linear in
// docs but called on every search — worth a baseline.
func BenchmarkBlendRerankScores(b *testing.B) {
	for _, n := range []int{20, 50, 100} {
		docs := makeBenchDocs(n)
		rerank := make(map[int]float64, n)
		r := rand.New(rand.NewSource(3))
		for i := 0; i < n; i++ {
			rerank[i] = r.Float64()
		}
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			// Pre-allocate the working slice outside the timed loop so the
			// per-iteration setup (the copy) doesn't get charged to
			// BlendRerankScores' own allocation count.
			working := make([]RankedDoc, len(docs))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Copy docs each iter so we measure the function on its
				// "fresh" input shape, not on already-blended scores.
				copy(working, docs)
				BlendRerankScores(working, rerank, 0.5)
			}
		})
	}
}
