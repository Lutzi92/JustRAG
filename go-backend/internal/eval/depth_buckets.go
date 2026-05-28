package eval

// Depth-bucket identifiers. The set is closed and stable (these
// strings appear in JSON reports and in the markdown export);
// extending it is a breaking change for downstream consumers.
const (
	DepthBucket0to25   = "0-25"
	DepthBucket25to50  = "25-50"
	DepthBucket50to75  = "50-75"
	DepthBucket75to100 = "75-100"
)

// AllDepthBuckets is the canonical iteration order — useful for
// the markdown report and JSON pretty-printers that want a stable
// row ordering.
var AllDepthBuckets = []string{
	DepthBucket0to25,
	DepthBucket25to50,
	DepthBucket50to75,
	DepthBucket75to100,
}

// ChunkPositionFromMetadata extracts (chunkIndex, totalChunks)
// from a chunk's metadata map as written by the processor. Returns
// (0, 0) on any failure: missing fields, wrong types, or non-int
// numeric values. The (0, 0) sentinel is what the bucket aggregator
// treats as "skip this chunk" — TotalChunks == 0 is invalid input
// to BucketDepth.
//
// Accepts both float64 (the JSON-decoded representation that Postgres
// returns via pgx) and int (when callers build metadata directly).
func ChunkPositionFromMetadata(meta map[string]any) (int, int) {
	if meta == nil {
		return 0, 0
	}
	idx, idxOK := positionFieldAsInt(meta["chunkIndex"])
	total, totalOK := positionFieldAsInt(meta["totalChunks"])
	if !idxOK || !totalOK {
		return 0, 0
	}
	return idx, total
}

// positionFieldAsInt accepts the two representations the
// processor's metadata can carry for the position fields:
// float64 (after JSON round-trip) or int (when built directly).
// Other types — including strings, bools, nils — are rejected.
func positionFieldAsInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
}

// DepthBucketStats reports retrieval counts in one depth bucket.
// Relevant means the chunk's file_id is in the question's
// MustCiteFileIDs set. Total = RelevantHits + NonRelevantHits.
type DepthBucketStats struct {
	Bucket          string `json:"bucket"`
	RelevantHits    int    `json:"relevant_hits"`
	NonRelevantHits int    `json:"non_relevant_hits"`
	Total           int    `json:"total"`
}

// DepthBucketReport summarises retrieval-by-position across an
// entire eval run. EligibleQuestions is the number of non-errored
// questions whose retrieved set contained at least one chunk with
// valid bucket metadata (i.e. TotalChunks >= MinTotalChunks).
// Buckets is always populated in AllDepthBuckets order even when
// every count is zero, so JSON output shape is stable.
type DepthBucketReport struct {
	K                 int                `json:"k"`
	MinTotalChunks    int                `json:"min_total_chunks"`
	EligibleQuestions int                `json:"eligible_questions"`
	Buckets           []DepthBucketStats `json:"buckets"`
}

// AggregateDepthBuckets walks the per-question reports and counts
// retrieved chunks per quartile of source-file depth.
//
// Inputs:
//   - questions: per-question reports as produced by RunEval. Reports
//     with non-empty Error are skipped (same convention as Aggregate).
//   - k: cap on retrieved chunks per question (matches the @k
//     convention; chunks beyond position k-1 are ignored).
//   - minTotalChunks: ignore chunks whose source file has fewer
//     total chunks than this threshold. Suppresses noise from
//     1-3 chunk files where bucketing has no useful signal.
//     Pass 1 to disable filtering.
//
// Output: a populated DepthBucketReport. EligibleQuestions counts
// the questions that contributed at least one bucketed chunk
// (errored / empty-retrieval / all-chunks-filtered questions are
// excluded).
func AggregateDepthBuckets(questions []QuestionReport, k, minTotalChunks int) DepthBucketReport {
	rep := DepthBucketReport{
		K:              k,
		MinTotalChunks: minTotalChunks,
		Buckets:        make([]DepthBucketStats, 0, len(AllDepthBuckets)),
	}
	counts := make(map[string]*DepthBucketStats, len(AllDepthBuckets))
	for _, name := range AllDepthBuckets {
		s := DepthBucketStats{Bucket: name}
		counts[name] = &s
	}

	for _, q := range questions {
		if q.Error != "" {
			continue
		}
		truth := toSet(q.Question.MustCiteFileIDs)
		retrieved := q.Retrieved
		if k > 0 && k < len(retrieved) {
			retrieved = retrieved[:k]
		}
		contributed := false
		for _, c := range retrieved {
			if c.TotalChunks < minTotalChunks {
				continue
			}
			bucket := BucketDepth(c.ChunkIndex, c.TotalChunks)
			if bucket == "" {
				continue
			}
			st := counts[bucket]
			if _, ok := truth[c.FileID]; ok {
				st.RelevantHits++
			} else {
				st.NonRelevantHits++
			}
			st.Total++
			contributed = true
		}
		if contributed {
			rep.EligibleQuestions++
		}
	}

	for _, name := range AllDepthBuckets {
		rep.Buckets = append(rep.Buckets, *counts[name])
	}
	return rep
}

// BucketDepth maps a chunk's position within its source file to
// one of four quartile buckets. The depth is computed as
// chunkIndex / totalChunks (a value in [0.0, 1.0)) and assigned
// using half-open intervals except the last bucket which is closed:
//
//   - [0.00, 0.25) → "0-25"
//   - [0.25, 0.50) → "25-50"
//   - [0.50, 0.75) → "50-75"
//   - [0.75, 1.00] → "75-100"
//
// Returns "" for invalid inputs (totalChunks <= 0, chunkIndex
// negative, or chunkIndex >= totalChunks). Callers skip "" buckets
// when aggregating so a chunk with missing metadata never inflates
// any bucket's count.
//
// Note on small files: a 1-chunk file's only chunk falls into
// "0-25" trivially. The aggregator applies a minTotalChunks filter
// to suppress noise from these.
func BucketDepth(chunkIndex, totalChunks int) string {
	if totalChunks <= 0 || chunkIndex < 0 || chunkIndex >= totalChunks {
		return ""
	}
	// Use integer comparisons against scaled cutoffs to avoid
	// floating-point boundary drift (chunkIndex*4 vs totalChunks*N).
	// Equivalent to chunkIndex/totalChunks < 0.25, < 0.50, < 0.75.
	switch {
	case chunkIndex*4 < totalChunks:
		return DepthBucket0to25
	case chunkIndex*4 < totalChunks*2:
		return DepthBucket25to50
	case chunkIndex*4 < totalChunks*3:
		return DepthBucket50to75
	default:
		return DepthBucket75to100
	}
}
