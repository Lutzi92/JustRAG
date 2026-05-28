package vector_test

import (
	"fmt"

	"github.com/justrag/go-backend/internal/vector"
)

// ExampleFuseRRF demonstrates Reciprocal Rank Fusion of two retrieval
// passes. A document that appears in both lists outranks documents that
// appear in only one — that's the whole point of RRF as a hybrid-search
// fusion, and the example pins it in executable docs so `go doc -all
// github.com/justrag/go-backend/internal/vector` shows the behaviour.
func ExampleFuseRRF() {
	// Vector-pass results.
	vec := []vector.RankedDoc{
		{ID: "a", VectorScore: 0.92},
		{ID: "b", VectorScore: 0.75},
		{ID: "c", VectorScore: 0.41},
	}
	// BM25-pass results. Doc "a" lands top in both.
	bm25 := []vector.RankedDoc{
		{ID: "a", KeywordRank: 1},
		{ID: "d", KeywordRank: 2},
		{ID: "b", KeywordRank: 3},
	}

	fused := vector.FuseRRF(60, vec, bm25)
	fmt.Println(fused[0].ID) // shared between both lists → highest fused score
	// Output: a
}

// ExampleApplyMMR demonstrates MMR diversification. With a low λ the
// reranker trades relevance for diversity: when two near-duplicate
// chunks sit at the top of the score-sorted input, MMR keeps the
// higher-scored one and skips the duplicate in favour of a less-related
// but more informative chunk further down.
func ExampleApplyMMR() {
	// Input is pre-sorted by Score descending — ApplyMMR's contract.
	docs := []vector.RankedDoc{
		{ID: "top", Content: "paris is the capital of france", FileID: "f1", Score: 0.95},
		{ID: "dup", Content: "paris is the capital of france", FileID: "f1", Score: 0.93}, // identical, same file → Jaccard ≈ 1.0
		{ID: "fresh", Content: "lyon and marseille are major french cities", FileID: "f2", Score: 0.70},
	}
	// λ=0.3 weighs diversity more heavily than relevance — the
	// near-duplicate's similarity penalty outweighs its score
	// advantage, so MMR skips it and picks the unrelated chunk.
	out := vector.ApplyMMR(docs, 0.3, 2)
	fmt.Println(out[0].ID)
	fmt.Println(out[1].ID)
	// Output:
	// top
	// fresh
}
