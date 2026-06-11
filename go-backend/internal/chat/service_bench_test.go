package chat

import (
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

func BenchmarkTruncateChunksToFit_AllFit(b *testing.B) {
	chunks := makeBenchChunks(30, 200)
	b.ReportAllocs()
	for b.Loop() {
		TruncateChunksToFit(chunks, 100_000)
	}
}

func BenchmarkTruncateChunksToFit_NeedsTruncation(b *testing.B) {
	chunks := makeBenchChunks(30, 200)
	b.ReportAllocs()
	for b.Loop() {
		TruncateChunksToFit(chunks, 2000)
	}
}

func BenchmarkTruncateChunksToFit_LargeContext(b *testing.B) {
	chunks := makeBenchChunks(100, 500)
	b.ReportAllocs()
	for b.Loop() {
		TruncateChunksToFit(chunks, 10_000)
	}
}

func makeBenchChunks(n, wordsPerChunk int) []vector.SearchChunk {
	body := strings.Repeat("word ", wordsPerChunk)
	chunks := make([]vector.SearchChunk, n)
	for i := range chunks {
		chunks[i] = vector.SearchChunk{
			Content: body,
			Score:   float64(n-i) / float64(n),
		}
	}
	return chunks
}
