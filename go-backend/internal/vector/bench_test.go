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
	for b.Loop() {
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
	for b.Loop() {
		formatEmbedding(emb)
	}
}

func BenchmarkSanitizeUTF8Clean(b *testing.B) {
	text := "This is perfectly valid UTF-8 text with no issues at all."
	b.ReportAllocs()
	for b.Loop() {
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
	for b.Loop() {
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
			for b.Loop() {
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
			for b.Loop() {
				FuseRRF(60, listA, listB)
			}
		})

		listC := make([]RankedDoc, len(docs))
		copy(listC, docs)
		r.Shuffle(len(listC), func(i, j int) { listC[i], listC[j] = listC[j], listC[i] })
		b.Run(fmt.Sprintf("three_lists_n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
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
		for b.Loop() {
			_ = svc.loadSiteConfigCached(ctx)
		}
	})

	b.Run("ParallelHit", func(b *testing.B) {
		b.ReportAllocs()
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
			for b.Loop() {
				// Copy docs each iter so we measure the function on its
				// "fresh" input shape, not on already-blended scores.
				copy(working, docs)
				BlendRerankScores(working, rerank, 0.5)
			}
		})
	}
}

// BenchmarkBuildRerankDocs guards the rerank-input assembly on the search hot
// path (invoked whenever a reranker is active). Half the docs carry a
// ContextualPrefix to exercise the pooled-buffer prefix+content concat path;
// the other half pass through with no allocation. A regression in the string
// building (e.g. losing the buffer pool) surfaces here at n=200, the realistic
// candidate-pool size after RRF fusion.
func BenchmarkBuildRerankDocs(b *testing.B) {
	for _, n := range []int{20, 50, 100, 200} {
		docs := makeBenchDocs(n)
		// Give every other doc a contextual prefix so both branches run.
		for i := range docs {
			if i%2 == 0 {
				docs[i].ContextualPrefix = fmt.Sprintf("Section %d of the source document.", i)
			}
		}
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = buildRerankDocs(docs)
			}
		})
	}
}

// BenchmarkDeduplicate guards the near-duplicate filter on the retrieval hot
// path. It is O(kept^2) in the worst case (every kept doc compared against
// each survivor), with one sortedUniqueWords tokenization per accepted doc,
// so a regression in either the merge-join or the tokenizer surfaces here.
func BenchmarkDeduplicate(b *testing.B) {
	for _, n := range []int{20, 50, 100, 200} {
		docs := makeBenchDocs(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				Deduplicate(docs, 0.9)
			}
		})
	}
}

// BenchmarkSortedUniqueWords isolates the per-doc tokenize+sort+dedup cost
// that both Deduplicate and ApplyMMR pay once per candidate. The 30-word
// body mirrors makeBenchDocs so the number is comparable across the suite.
func BenchmarkSortedUniqueWords(b *testing.B) {
	text := makeBenchDocs(1)[0].Content
	b.ReportAllocs()
	for b.Loop() {
		sortedUniqueWords(text)
	}
}

// BenchmarkJaccardSorted measures the zero-allocation merge-join similarity
// over two pre-tokenized slices — the inner comparison both Deduplicate and
// ApplyMMR call O(k) times per candidate. Tokenization is hoisted out of the
// timed loop so this captures the merge-join alone.
func BenchmarkJaccardSorted(b *testing.B) {
	docs := makeBenchDocs(2)
	a := sortedUniqueWords(docs[0].Content)
	c := sortedUniqueWords(docs[1].Content)
	b.ReportAllocs()
	for b.Loop() {
		jaccardSorted(a, c)
	}
}

// BenchmarkSearchPipeline composes the post-retrieval pipeline in production
// stage order (search.go steps 9–13b): RRF fusion → rerank-score blend →
// dedup → snapshot → MMR → trim → BM25 floor. The per-stage benchmarks above
// guard each component; this one guards the composition and is the
// regression baseline for pipeline-level changes. Network stages (embedding,
// pgvector query, reranker call) are excluded — rerank scores are
// synthesized deterministically per fused index. The snapshot and score maps
// are hoisted and clear()ed per iteration to mirror the pooled scratch maps
// the production path borrows.
func BenchmarkSearchPipeline(b *testing.B) {
	const limit = 10
	for _, n := range []int{50, 200} {
		b.Run(fmt.Sprintf("docs_per_list=%d", n), func(b *testing.B) {
			docs := makeBenchDocs(n)
			vec := make([]RankedDoc, len(docs))
			copy(vec, docs)
			// Keyword arm: same corpus in a different rank order, as BM25
			// and vector search typically agree on membership more than on
			// order. Index 0 = best BM25 hit, which ApplyBM25Floor assumes.
			kw := make([]RankedDoc, len(docs))
			copy(kw, docs)
			r := rand.New(rand.NewSource(3))
			r.Shuffle(len(kw), func(i, j int) { kw[i], kw[j] = kw[j], kw[i] })
			for i := range kw {
				kw[i].Score = float64(len(kw) - i)
			}
			weights := []float64{1.0, 1.0}
			scoreMap := make(map[int]float64, 2*n)
			snapshot := make(map[string]RankedDoc, 2*n)

			b.ReportAllocs()
			for b.Loop() {
				fused := FuseRRFWeighted(60, weights, vec, kw)
				clear(scoreMap)
				for i := range fused {
					// Deterministic stand-in for cross-encoder output,
					// decorrelated from RRF order so the blend pass does
					// real re-sorting work.
					scoreMap[i] = float64((i*7919)%101) / 100.0
				}
				fused = BlendRerankScores(fused, scoreMap, 0.7)
				fused = Deduplicate(fused, 0.85)
				clear(snapshot)
				for _, d := range fused {
					snapshot[d.ID] = d
				}
				if len(fused) > limit {
					pool := limit * 2
					if pool > len(fused) {
						pool = len(fused)
					}
					fused = ApplyMMR(fused[:pool], 0.7, limit)
				}
				if len(fused) > limit {
					fused = fused[:limit]
				}
				fused, _ = ApplyBM25Floor(fused, kw, snapshot, limit, BM25FloorMaxFilesFor(limit))
				_ = fused
			}
		})
	}
}
