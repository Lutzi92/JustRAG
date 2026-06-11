package ai

import (
	"math/rand"
	"strings"
	"testing"
)

// BenchmarkEmbCacheKey measures Redis-key derivation for an embedding cache
// lookup. Runs on every Get/Set, so heap allocations here directly translate
// into search-path GC pressure.
func BenchmarkEmbCacheKey(b *testing.B) {
	const model = "text-embedding-qwen3-4b"
	// 200 chars is on the high end of typical query length; document chunks
	// can be longer but Get/Set is dominated by query-side calls.
	text := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 5)
	b.ReportAllocs()
	for b.Loop() {
		_ = embCacheKey(model, text)
	}
}

// BenchmarkMarshalEmbeddingInto measures the in-place packed-LE encoding
// path. Should report 0 allocs once the destination slice has enough cap.
func BenchmarkMarshalEmbeddingInto1536(b *testing.B) {
	emb := make([]float64, 1536)
	r := rand.New(rand.NewSource(1))
	for i := range emb {
		emb[i] = r.Float64()*2 - 1
	}
	dst := make([]byte, 0, len(emb)*8)
	b.ReportAllocs()
	for b.Loop() {
		dst = marshalEmbeddingInto(dst[:0], emb)
	}
	_ = dst
}

func BenchmarkMarshalEmbeddingInto4096(b *testing.B) {
	emb := make([]float64, 4096)
	r := rand.New(rand.NewSource(1))
	for i := range emb {
		emb[i] = r.Float64()*2 - 1
	}
	dst := make([]byte, 0, len(emb)*8)
	b.ReportAllocs()
	for b.Loop() {
		dst = marshalEmbeddingInto(dst[:0], emb)
	}
	_ = dst
}
