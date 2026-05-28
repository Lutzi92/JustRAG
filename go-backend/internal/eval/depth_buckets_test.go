package eval

import (
	"testing"
)

// TestBucketDepth_Boundaries pins the bucket cutoffs. Each bucket
// is a half-open interval except the last which is closed:
//   - 0-25:   [0.00, 0.25)
//   - 25-50:  [0.25, 0.50)
//   - 50-75:  [0.50, 0.75)
//   - 75-100: [0.75, 1.00]
func TestBucketDepth_Boundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		chunkIndex  int
		totalChunks int
		want        string
	}{
		// 4-chunk file — exact quartile boundaries.
		{0, 4, "0-25"},
		{1, 4, "25-50"},
		{2, 4, "50-75"},
		{3, 4, "75-100"},
		// 8-chunk file — interior of each bucket.
		{0, 8, "0-25"},
		{1, 8, "0-25"},
		{2, 8, "25-50"},
		{3, 8, "25-50"},
		{4, 8, "50-75"},
		{5, 8, "50-75"},
		{6, 8, "75-100"},
		{7, 8, "75-100"},
		// 100-chunk file — boundary samples (1% granularity).
		{0, 100, "0-25"},
		{24, 100, "0-25"},
		{25, 100, "25-50"},
		{49, 100, "25-50"},
		{50, 100, "50-75"},
		{74, 100, "50-75"},
		{75, 100, "75-100"},
		{99, 100, "75-100"},
	}
	for _, c := range cases {
		got := BucketDepth(c.chunkIndex, c.totalChunks)
		if got != c.want {
			t.Errorf("BucketDepth(%d, %d) = %q, want %q", c.chunkIndex, c.totalChunks, got, c.want)
		}
	}
}

// TestBucketDepth_InvalidInputs: zero / negative / out-of-range
// inputs all yield "" (unknown bucket). Callers skip such chunks
// when aggregating so a missing-metadata chunk doesn't pollute
// any bucket.
func TestBucketDepth_InvalidInputs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		chunkIndex, totalChunks int
	}{
		{0, 0},  // total=0
		{0, -1}, // negative total
		{-1, 4}, // negative index
		{4, 4},  // index == total (out of range; total is the count, max valid index is total-1)
		{5, 4},  // index > total
		{10, 0}, // both bad
	}
	for _, c := range cases {
		if got := BucketDepth(c.chunkIndex, c.totalChunks); got != "" {
			t.Errorf("BucketDepth(%d, %d) = %q, want empty (invalid input)", c.chunkIndex, c.totalChunks, got)
		}
	}
}

// TestBucketDepth_SingleChunkFile: a 1-chunk file's only chunk
// has chunkIndex=0 → depth=0.0 → "0-25". The aggregator can apply
// a min-totalChunks filter to suppress noise from these; the
// bucket function itself reports honestly.
func TestBucketDepth_SingleChunkFile(t *testing.T) {
	t.Parallel()
	if got := BucketDepth(0, 1); got != "0-25" {
		t.Errorf("BucketDepth(0, 1) = %q, want 0-25", got)
	}
}

// ---------------------------------------------------------------------------
// AggregateDepthBuckets — population stats per bucket
// ---------------------------------------------------------------------------

// TestAggregateDepthBuckets_HappyPath builds two questions, each
// retrieving chunks from across the depth range, and verifies the
// aggregator splits relevant vs non-relevant hits per bucket.
func TestAggregateDepthBuckets_HappyPath(t *testing.T) {
	t.Parallel()
	questions := []QuestionReport{
		{
			Question: Question{ID: "q1", MustCiteFileIDs: []string{"file-A"}},
			Retrieved: []RetrievedChunk{
				{FileID: "file-A", ChunkIndex: 0, TotalChunks: 8}, // 0-25, relevant
				{FileID: "file-A", ChunkIndex: 4, TotalChunks: 8}, // 50-75, relevant
				{FileID: "file-B", ChunkIndex: 1, TotalChunks: 8}, // 0-25, non-relevant
				{FileID: "file-C", ChunkIndex: 7, TotalChunks: 8}, // 75-100, non-relevant
			},
		},
		{
			Question: Question{ID: "q2", MustCiteFileIDs: []string{"file-B"}},
			Retrieved: []RetrievedChunk{
				{FileID: "file-B", ChunkIndex: 6, TotalChunks: 8}, // 75-100, relevant
				{FileID: "file-A", ChunkIndex: 2, TotalChunks: 8}, // 25-50, non-relevant
			},
		},
	}

	rep := AggregateDepthBuckets(questions, 10, 4)
	if rep.EligibleQuestions != 2 {
		t.Errorf("EligibleQuestions = %d, want 2", rep.EligibleQuestions)
	}
	if rep.MinTotalChunks != 4 {
		t.Errorf("MinTotalChunks = %d, want 4", rep.MinTotalChunks)
	}
	if rep.K != 10 {
		t.Errorf("K = %d, want 10", rep.K)
	}
	if len(rep.Buckets) != 4 {
		t.Fatalf("expected 4 buckets, got %d", len(rep.Buckets))
	}

	// Verify bucket counts. Order matches AllDepthBuckets.
	want := map[string]struct{ rel, nonRel int }{
		"0-25":   {rel: 1, nonRel: 1},
		"25-50":  {rel: 0, nonRel: 1},
		"50-75":  {rel: 1, nonRel: 0},
		"75-100": {rel: 1, nonRel: 1},
	}
	for _, b := range rep.Buckets {
		w := want[b.Bucket]
		if b.RelevantHits != w.rel {
			t.Errorf("bucket %s: RelevantHits = %d, want %d", b.Bucket, b.RelevantHits, w.rel)
		}
		if b.NonRelevantHits != w.nonRel {
			t.Errorf("bucket %s: NonRelevantHits = %d, want %d", b.Bucket, b.NonRelevantHits, w.nonRel)
		}
		if b.Total != b.RelevantHits+b.NonRelevantHits {
			t.Errorf("bucket %s: Total = %d, want %d", b.Bucket, b.Total, b.RelevantHits+b.NonRelevantHits)
		}
	}
}

// TestAggregateDepthBuckets_MinTotalChunksFilter: chunks from
// short files (totalChunks below minTotalChunks) are skipped so
// 1-2 chunk files don't pollute the 0-25 bucket. The filter is
// per-chunk, not per-question.
func TestAggregateDepthBuckets_MinTotalChunksFilter(t *testing.T) {
	t.Parallel()
	questions := []QuestionReport{
		{
			Question: Question{ID: "q1", MustCiteFileIDs: []string{"file-A"}},
			Retrieved: []RetrievedChunk{
				{FileID: "file-A", ChunkIndex: 0, TotalChunks: 2}, // SKIPPED (total < 4)
				{FileID: "file-A", ChunkIndex: 0, TotalChunks: 8}, // 0-25, relevant
			},
		},
	}

	rep := AggregateDepthBuckets(questions, 10, 4)
	totalHits := 0
	for _, b := range rep.Buckets {
		totalHits += b.Total
	}
	if totalHits != 1 {
		t.Errorf("total bucket hits = %d, want 1 (one chunk filtered by minTotalChunks)", totalHits)
	}
	for _, b := range rep.Buckets {
		if b.Bucket == "0-25" && b.RelevantHits != 1 {
			t.Errorf("0-25 RelevantHits = %d, want 1", b.RelevantHits)
		}
	}
}

// TestAggregateDepthBuckets_NoEligibleChunks: when every chunk is
// invalid or filtered, EligibleQuestions is 0 and all bucket
// totals are 0 (but the bucket list is still populated for
// stable JSON output).
func TestAggregateDepthBuckets_NoEligibleChunks(t *testing.T) {
	t.Parallel()
	questions := []QuestionReport{
		{
			Question: Question{ID: "q1", MustCiteFileIDs: []string{"file-A"}},
			Retrieved: []RetrievedChunk{
				{FileID: "file-A", ChunkIndex: 0, TotalChunks: 0}, // missing metadata
				{FileID: "file-A", ChunkIndex: 0, TotalChunks: 2}, // filtered (< 4)
			},
		},
	}
	rep := AggregateDepthBuckets(questions, 10, 4)
	if rep.EligibleQuestions != 0 {
		t.Errorf("EligibleQuestions = %d, want 0", rep.EligibleQuestions)
	}
	if len(rep.Buckets) != 4 {
		t.Errorf("Buckets length = %d, want 4 (stable output regardless of data)", len(rep.Buckets))
	}
	for _, b := range rep.Buckets {
		if b.Total != 0 {
			t.Errorf("bucket %s Total = %d, want 0", b.Bucket, b.Total)
		}
	}
}

// TestAggregateDepthBuckets_SkipsErroredQuestions: questions with
// a non-empty Error field don't contribute to the aggregate
// (matches the convention used by Aggregate / AggregateByRoute).
func TestAggregateDepthBuckets_SkipsErroredQuestions(t *testing.T) {
	t.Parallel()
	questions := []QuestionReport{
		{
			Question: Question{ID: "q1", MustCiteFileIDs: []string{"file-A"}},
			Error:    "search failed",
			Retrieved: []RetrievedChunk{
				{FileID: "file-A", ChunkIndex: 0, TotalChunks: 8},
			},
		},
		{
			Question: Question{ID: "q2", MustCiteFileIDs: []string{"file-A"}},
			Retrieved: []RetrievedChunk{
				{FileID: "file-A", ChunkIndex: 0, TotalChunks: 8},
			},
		},
	}
	rep := AggregateDepthBuckets(questions, 10, 4)
	if rep.EligibleQuestions != 1 {
		t.Errorf("EligibleQuestions = %d, want 1 (errored question skipped)", rep.EligibleQuestions)
	}
	totalHits := 0
	for _, b := range rep.Buckets {
		totalHits += b.Total
	}
	if totalHits != 1 {
		t.Errorf("total hits = %d, want 1", totalHits)
	}
}

// TestChunkPositionFromMetadata_HappyPath: the processor writes
// chunkIndex + totalChunks as numbers in the chunk's metadata
// JSONB. After Postgres returns the JSONB and pgx unmarshals into
// map[string]any, numbers come back as float64. The extractor
// must accept that representation and round-trip cleanly.
func TestChunkPositionFromMetadata_HappyPath(t *testing.T) {
	t.Parallel()
	meta := map[string]any{
		"chunkIndex":  float64(3),
		"totalChunks": float64(8),
		"page":        float64(2),
	}
	idx, total := ChunkPositionFromMetadata(meta)
	if idx != 3 || total != 8 {
		t.Errorf("got (%d, %d), want (3, 8)", idx, total)
	}
}

// TestChunkPositionFromMetadata_AcceptsIntKind: defensive — some
// codepaths build metadata maps directly with int values rather
// than the JSON-decoded float64. The extractor handles both so
// future ingestion changes don't silently lose position data.
func TestChunkPositionFromMetadata_AcceptsIntKind(t *testing.T) {
	t.Parallel()
	meta := map[string]any{"chunkIndex": 3, "totalChunks": 8}
	idx, total := ChunkPositionFromMetadata(meta)
	if idx != 3 || total != 8 {
		t.Errorf("got (%d, %d), want (3, 8)", idx, total)
	}
}

// TestChunkPositionFromMetadata_MissingFields: missing or
// malformed fields yield (0, 0). The aggregator skips chunks
// with TotalChunks == 0 so this is the safe "ignore me" signal.
func TestChunkPositionFromMetadata_MissingFields(t *testing.T) {
	t.Parallel()
	cases := []map[string]any{
		nil,                         // nil map
		{},                          // empty map
		{"page": float64(2)},        // unrelated field
		{"chunkIndex": float64(3)},  // missing totalChunks
		{"totalChunks": float64(8)}, // missing chunkIndex
		{"chunkIndex": "three", "totalChunks": 8}, // wrong type
		{"chunkIndex": float64(3), "totalChunks": "eight"},
	}
	for i, m := range cases {
		idx, total := ChunkPositionFromMetadata(m)
		if idx != 0 || total != 0 {
			t.Errorf("case %d: got (%d, %d), want (0, 0)", i, idx, total)
		}
	}
}

// TestAggregateDepthBuckets_TruncatesToK: the aggregator only
// considers the first k retrieved chunks per question, matching
// the existing Recall/Precision @ k convention.
func TestAggregateDepthBuckets_TruncatesToK(t *testing.T) {
	t.Parallel()
	questions := []QuestionReport{
		{
			Question: Question{ID: "q1", MustCiteFileIDs: []string{"file-A"}},
			Retrieved: []RetrievedChunk{
				{FileID: "file-A", ChunkIndex: 0, TotalChunks: 8},
				{FileID: "file-A", ChunkIndex: 1, TotalChunks: 8},
				{FileID: "file-A", ChunkIndex: 2, TotalChunks: 8}, // beyond k=2
				{FileID: "file-A", ChunkIndex: 7, TotalChunks: 8}, // beyond k=2
			},
		},
	}
	rep := AggregateDepthBuckets(questions, 2, 4)
	totalHits := 0
	for _, b := range rep.Buckets {
		totalHits += b.Total
	}
	if totalHits != 2 {
		t.Errorf("total hits at k=2 = %d, want 2", totalHits)
	}
}
