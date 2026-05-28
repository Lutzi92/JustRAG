package vector

import (
	"cmp"
	"encoding/json"
	"math"
	"slices"
	"sync"
)

// rrfMergeMapPool recycles the merged-doc map used by FuseRRF. Maps grown
// past the typical search size (≤ a few hundred unique docs) stay sized for
// the pooled reuse: clear() drops entries but keeps the bucket array, so
// subsequent calls don't pay the rehash + allocation cost of the typical
// merge. The map is keyed by chunk ID and holds RankedDoc by value (not
// pointer) — pointer entries would force every unique doc onto the heap via
// `&copy` escape; value entries keep accumulation alloc-free.
var rrfMergeMapPool = sync.Pool{
	New: func() any { m := make(map[string]RankedDoc, 256); return &m },
}

// RankedDoc represents a document with scoring information from one or more retrieval passes.
type RankedDoc struct {
	ID               string
	Content          string
	ContextualPrefix string // LLM-generated 1-sentence context prefix (empty when enrichment was off at ingestion)
	// Metadata is the raw JSON bytes scanned from the chunk row's metadata
	// column. json.RawMessage (rather than string) so the per-chunk
	// json.Unmarshal in the final assembly loop can read the bytes
	// directly instead of paying a []byte(string) copy per result.
	Metadata    json.RawMessage
	FileID      string
	Score       float64 // final score (RRF or blended)
	VectorScore float64
	KeywordRank float64
	RerankScore float64
	// ParentChunkID is the FK to document_chunk_parents.id when this row
	// is a Phase 3 §D parent-child child chunk. Nil for legacy flat
	// chunks. The retrieval pipeline uses it to swap child Content +
	// ContextualPrefix with the parent's after the final ranking is
	// settled (LookupParentContent + swapToParents in search.go).
	ParentChunkID *string
	// NodeKind is "leaf" for ordinary chunks and "summary" for Phase F
	// RAPTOR-built summary nodes. Empty string is normalised to "leaf"
	// by toRankedDocs (covers pre-0046 reads).
	NodeKind string
	// TreeLevel is 0 for leaves, 1..N for RAPTOR summaries.
	TreeLevel int
}

// FuseRRF merges multiple ranked lists using Reciprocal Rank Fusion.
// For each document across all lists: score += 1 / (k + rank + 1) where rank is 0-based.
// Documents appearing in multiple lists accumulate score.
// The highest VectorScore and KeywordRank seen across lists are preserved.
// The result is sorted by Score descending.
func FuseRRF(k int, lists ...[]RankedDoc) []RankedDoc {
	return FuseRRFWeighted(k, nil, lists...)
}

// FuseRRFWeighted is FuseRRF with a per-list multiplicative weight applied
// to each list's RRF contribution. weights[i] is the multiplier for lists[i];
// if weights is shorter than lists, missing entries default to 1.0 so the
// behavior matches FuseRRF on unweighted call sites. Negative weights are
// clamped to 0 (a zero-weight list contributes nothing). Tunable via
// site_configs keys "rrf_weight_vector" and "rrf_weight_bm25".
func FuseRRFWeighted(k int, weights []float64, lists ...[]RankedDoc) []RankedDoc {
	mergedPtr := rrfMergeMapPool.Get().(*map[string]RankedDoc)
	merged := *mergedPtr
	defer func() {
		clear(merged)
		rrfMergeMapPool.Put(mergedPtr)
	}()

	for listIdx, list := range lists {
		w := 1.0
		if listIdx < len(weights) {
			w = weights[listIdx]
			if w < 0 {
				w = 0
			}
		}
		if w == 0 {
			continue
		}
		for rank, doc := range list {
			existing, ok := merged[doc.ID]
			if !ok {
				existing = doc
				existing.Score = 0
			}
			existing.Score += w / float64(k+rank+1)
			if doc.VectorScore > existing.VectorScore {
				existing.VectorScore = doc.VectorScore
			}
			if doc.KeywordRank > existing.KeywordRank {
				existing.KeywordRank = doc.KeywordRank
			}
			merged[doc.ID] = existing
		}
	}
	result := make([]RankedDoc, 0, len(merged))
	for _, doc := range merged {
		result = append(result, doc)
	}
	slices.SortFunc(result, func(a, b RankedDoc) int { return cmp.Compare(b.Score, a.Score) })
	return result
}

// BlendRerankScores combines RRF scores with cross-encoder rerank scores.
// alpha controls how strongly the rerank score dominates the final ranking:
//   - 1.0 → pure rerank scores (no normalization, raw reranker output sorted)
//   - 0.5 → genuine 50/50 blend of normalized reranker and normalized RRF
//   - 0.0 → rerank scores are ignored (RRF order preserved, but normalized)
//
// When 0 < alpha < 1, BOTH the RRF scores and the rerank scores are
// independently min-max-normalized to [0, 1] before blending. Without
// normalization on the reranker side the blend is not actually alpha/1-alpha:
// different rerankers emit raw scores in wildly different ranges (sigmoid
// [0,1], logit [-∞,+∞], Cohere-style [0,1], BGE-style raw logits, …), so a
// "50/50 blend" can be dominated by the side with the larger dynamic range.
// Normalizing both sides makes alpha honest and portable across reranker
// models — ZeroEntropy's 2026 reranker guide calls this out as a key
// production concern.
//
// rerankScores maps original index in docs to the raw rerank score for that
// document. The RerankScore field on each doc is populated with the RAW
// reranker score (for telemetry / debugging); the final Score is the
// normalized-and-blended value.
func BlendRerankScores(docs []RankedDoc, rerankScores map[int]float64, alpha float64) []RankedDoc {
	if len(docs) == 0 {
		return docs
	}

	if alpha >= 1.0 {
		// Pure rerank: raw reranker output is the final order. No blend,
		// so no normalization needed.
		for i := range docs {
			rerankScore := rerankScores[i]
			docs[i].RerankScore = rerankScore
			docs[i].Score = rerankScore
		}
		slices.SortFunc(docs, func(a, b RankedDoc) int { return cmp.Compare(b.Score, a.Score) })
		return docs
	}

	// Min/max for both signals, in one pass.
	minRRF, maxRRF := docs[0].Score, docs[0].Score
	minRerank, maxRerank := rerankScores[0], rerankScores[0]
	for i := 1; i < len(docs); i++ {
		if docs[i].Score < minRRF {
			minRRF = docs[i].Score
		}
		if docs[i].Score > maxRRF {
			maxRRF = docs[i].Score
		}
		rs := rerankScores[i]
		if rs < minRerank {
			minRerank = rs
		}
		if rs > maxRerank {
			maxRerank = rs
		}
	}
	rrfRange := maxRRF - minRRF
	rerankRange := maxRerank - minRerank

	for i := range docs {
		var normalizedRRF, normalizedRerank float64
		if rrfRange == 0 {
			normalizedRRF = 1.0
		} else {
			normalizedRRF = (docs[i].Score - minRRF) / rrfRange
		}
		rawRerank := rerankScores[i]
		if rerankRange == 0 {
			normalizedRerank = 1.0
		} else {
			normalizedRerank = (rawRerank - minRerank) / rerankRange
		}
		docs[i].RerankScore = rawRerank // keep raw for telemetry
		docs[i].Score = alpha*normalizedRerank + (1-alpha)*normalizedRRF
	}

	slices.SortFunc(docs, func(a, b RankedDoc) int { return cmp.Compare(b.Score, a.Score) })
	return docs
}

// ApplyScoreDrop performs elbow detection on a sorted (descending) slice of docs.
// Starting from index 1, if any doc's score drops below threshold * docs[0].Score,
// the slice is cut at that index and the prefix is returned.
func ApplyScoreDrop(docs []RankedDoc, threshold float64) []RankedDoc {
	if len(docs) == 0 {
		return docs
	}
	cutoff := len(docs)
	topScore := docs[0].Score
	for i := 1; i < len(docs); i++ {
		if docs[i].Score < threshold*topScore {
			cutoff = i
			break
		}
	}
	return docs[:cutoff]
}

// floorCandidate pairs a top-BM25 file with its single best (highest-ranked)
// chunk ID. Top-level so bm25FloorScratch can pool the candidates slice.
type floorCandidate struct{ fileID, chunkID string }

// bm25FloorScratch bundles the structures ApplyBM25Floor reused to allocate
// on every search. Pooling drops the per-call hot-path allocations: `Search`
// invokes the floor at the tail of every request, and the maps in particular
// were rehashing into fresh buckets each time.
//
// bm25FileSet, keep, and next are only touched on the rarer displacement
// branch (len(final)+len(missing) > limit). They live in the same scratch
// struct so the displacement path also runs allocation-free once the pool
// has warmed up.
type bm25FloorScratch struct {
	seenFile     map[string]bool
	candidates   []floorCandidate
	finalIdxByID map[string]int
	missing      []RankedDoc
	bm25FileSet  map[string]bool
	keep         []bool
	next         []RankedDoc
}

var bm25FloorScratchPool = sync.Pool{
	New: func() any {
		// 64 is a comfortable upper bound for typical maxFiles + limit
		// (limit ≤ 50 in production, maxFiles is limit/2 with floor 4).
		// Maps grown past that survive the clear(), so the bucket array
		// is reused on subsequent calls of the same approximate size.
		return &bm25FloorScratch{
			seenFile:     make(map[string]bool, 64),
			candidates:   make([]floorCandidate, 0, 64),
			finalIdxByID: make(map[string]int, 64),
			missing:      make([]RankedDoc, 0, 16),
			bm25FileSet:  make(map[string]bool, 64),
			keep:         make([]bool, 0, 64),
			next:         make([]RankedDoc, 0, 64),
		}
	},
}

// ApplyMinThreshold removes any docs whose Score is strictly below minScore.
func ApplyMinThreshold(docs []RankedDoc, minScore float64) []RankedDoc {
	result := docs[:0]
	for _, doc := range docs {
		if doc.Score >= minScore {
			result = append(result, doc)
		}
	}
	return result
}

// bm25FloorMinFiles is the minimum number of BM25-top files the floor will
// protect regardless of `limit`. BM25FloorMaxFilesFor computes the effective
// cap adaptively (half of `limit`, but never below this floor) so a person
// in 8 projects isn't truncated to 4, and typical single-answer queries
// still leave ample room for the reranker's picks.
const bm25FloorMinFiles = 4

// BM25FloorMaxFilesFor returns the adaptive cap for ApplyBM25Floor given
// the caller's result-set limit. Goal: never let the floor grab more than
// half the result (reranker stays relevant) while also not truncating
// strong keyword evidence for queries that legitimately have many hits
// (e.g. "all projects with Person X" where X is on many teams).
func BM25FloorMaxFilesFor(limit int) int {
	half := limit / 2
	if half < bm25FloorMinFiles {
		return bm25FloorMinFiles
	}
	return half
}

// ApplyBM25Floor guarantees that the top BM25-ranked chunks (one per file,
// up to maxFiles budget) surface in the final result — even when the
// reranker, dedup, MMR or the final trim otherwise dropped that specific
// chunk. Reinserted chunks carry their post-dedup blended score from the
// snapshot so downstream token-budget truncation and sandwich ordering
// treat them fairly.
//
// Chunk-level (not file-level) presence is what matters: a project file
// might have several chunks, and the reranker can happily keep the
// project-description chunk while discarding the team-list chunk that
// actually contains the named entity. File-level recall would say
// "file is present, done"; the answer would still miss the mention.
//
// Why this exists: cross-encoder rerankers trained on generic Q/A pairs
// systematically misrank structural chunks (team lists in project
// profiles, attribute tables in product sheets) that are nonetheless the
// exact evidence a named-entity query needs. BM25 scores those chunks
// correctly via exact keyword match; the floor preserves that signal
// regardless of rerank_blend_alpha.
//
// Parameters:
//   - final: current final chunks after MMR + trim.
//   - keywordDocs: BM25-rank-ordered result list (index 0 = best BM25).
//   - snapshot: ID → post-dedup RankedDoc (carries blended Score).
//   - limit: hard cap on output length.
//   - maxFiles: cap on distinct files the floor may reinsert from.
//
// Returns the possibly-augmented final slice and the count of chunks
// reinserted. If keywordDocs is empty or every candidate chunk is
// already present in final, this is a no-op and final is returned
// unchanged.
func ApplyBM25Floor(
	final []RankedDoc,
	keywordDocs []RankedDoc,
	snapshot map[string]RankedDoc,
	limit int,
	maxFiles int,
) ([]RankedDoc, int) {
	if len(keywordDocs) == 0 || maxFiles <= 0 || limit <= 0 {
		return final, 0
	}

	// Borrow scratch space from the pool. clear() drops map entries but
	// keeps the bucket arrays sized for the typical call. The slices are
	// reset to length 0 against their existing backing arrays. If a slice
	// grows past its pooled cap (rare — bounded by maxFiles + reinserts),
	// the deferred write-back persists the new backing array.
	scratch := bm25FloorScratchPool.Get().(*bm25FloorScratch)
	seenFile := scratch.seenFile
	candidates := scratch.candidates[:0]
	finalIdxByID := scratch.finalIdxByID
	missing := scratch.missing[:0]
	clear(seenFile)
	clear(finalIdxByID)
	// Reset displacement-branch scratch up front: most calls never enter
	// that branch, but the cleanup is cheap and the defer below has to
	// write the (possibly grown) backing arrays back regardless.
	bm25FileSet := scratch.bm25FileSet
	keep := scratch.keep[:0]
	next := scratch.next[:0]
	clear(bm25FileSet)
	defer func() {
		scratch.candidates = candidates[:0]
		scratch.missing = missing[:0]
		scratch.keep = keep[:0]
		scratch.next = next[:0]
		bm25FloorScratchPool.Put(scratch)
	}()

	// Map each of the top-N distinct files to its best (highest-BM25-ranked)
	// chunk ID. That chunk ID is what we'll protect — not just the file.
	for _, kd := range keywordDocs {
		if kd.FileID == "" || seenFile[kd.FileID] {
			continue
		}
		seenFile[kd.FileID] = true
		candidates = append(candidates, floorCandidate{fileID: kd.FileID, chunkID: kd.ID})
		if len(candidates) >= maxFiles {
			break
		}
	}

	// Index chunks already present in final by ID (chunk-level, not
	// file-level — see function docs).
	for i, d := range final {
		finalIdxByID[d.ID] = i
	}

	// Compute the top score across the current final set. Reinserted and
	// in-place-boosted chunks get their score clamped up to this value
	// so the subsequent score-sort + SandwichOrder place them at the
	// array boundaries (LLM attention is strongest at the front/back of
	// the context; the middle is lost-in-the-middle territory). Without
	// the boost, chunks the reranker demoted keep their demoted score
	// and land in the middle despite the prompt correctly labeling them.
	topScore := 0.0
	if len(final) > 0 {
		topScore = final[0].Score
		for _, d := range final[1:] {
			if d.Score > topScore {
				topScore = d.Score
			}
		}
	}

	// Walk candidates. If present in final, boost the existing entry's
	// score in-place. If missing, stage a reinsertion with the boosted
	// score. We boost BOTH cases because the BM25 floor's semantic is
	// "these chunks are critical, regardless of reranker judgement";
	// that applies equally to a chunk already in final with a low score
	// and to a chunk that fell out entirely.
	touched := false
	for _, c := range candidates {
		if idx, present := finalIdxByID[c.chunkID]; present {
			if final[idx].Score < topScore {
				final[idx].Score = topScore
				touched = true
			}
			continue
		}
		if snap, ok := snapshot[c.chunkID]; ok {
			snap.Score = topScore
			missing = append(missing, snap)
		}
		// Chunk missing from snapshot (didn't survive dedup) → skip; a
		// same-content chunk from another file is already carrying that
		// evidence.
	}
	if len(missing) == 0 {
		if touched {
			slices.SortStableFunc(final, func(a, b RankedDoc) int { return cmp.Compare(b.Score, a.Score) })
		}
		return final, 0
	}

	// Splice with displacement-awareness. Naive tail-replacement
	// regularly dropped the SOLE representative of a DIFFERENT top-BM25
	// file that happened to sit at MMR's tail, so net effect was "we
	// reinserted F3 but lost F7" — observed in production as
	// pre_seeded_candidates=8 when keyword_files=9. To prevent that,
	// preferentially drop entries whose FileID is NOT in the top-BM25
	// set. Only if the non-BM25 budget is exhausted do we fall back to
	// dropping BM25 entries from the tail.
	if len(final)+len(missing) <= limit {
		final = append(final, missing...)
	} else {
		toDrop := len(final) + len(missing) - limit
		for _, c := range candidates {
			bm25FileSet[c.fileID] = true
		}
		// Grow keep in-place to len(final) with all-true entries. Reusing
		// the pooled backing array means the typical-size case avoids the
		// allocation entirely; growth only happens when final exceeds the
		// pool's last high-water mark.
		if cap(keep) < len(final) {
			keep = make([]bool, len(final))
		} else {
			keep = keep[:len(final)]
		}
		for i := range keep {
			keep[i] = true
		}
		// Pass 1: drop non-BM25-file entries from the tail.
		for i := len(final) - 1; i >= 0 && toDrop > 0; i-- {
			if !bm25FileSet[final[i].FileID] {
				keep[i] = false
				toDrop--
			}
		}
		// Pass 2: if still need to drop, go from the tail regardless.
		for i := len(final) - 1; i >= 0 && toDrop > 0; i-- {
			if keep[i] {
				keep[i] = false
				toDrop--
			}
		}
		// Reset next to a 0-len slice with capacity ≥ limit; the existing
		// backing array is reused unless limit exceeds the pool's high-
		// water mark.
		if cap(next) < limit {
			next = make([]RankedDoc, 0, limit)
		}
		for i, d := range final {
			if keep[i] {
				next = append(next, d)
			}
		}
		next = append(next, missing...)
		final = next
	}

	// Re-sort so reinserted chunks land at their blended-score position
	// rather than always at the tail. Downstream truncation keeps the
	// highest-scored chunks first.
	slices.SortStableFunc(final, func(a, b RankedDoc) int { return cmp.Compare(b.Score, a.Score) })
	return final, len(missing)
}

// EffectiveRerankAlpha selects the rerank-blend alpha for a single
// search call based on the query-type classification. Per-type overrides
// in cfg (RerankBlendAlphaLookup, RerankBlendAlphaEnumeration,
// RerankBlendAlphaComplexReasoning) are sentinel-encoded: a negative
// value means "inherit cfg.RerankBlendAlpha". Empty / unknown query
// types always inherit the global. Pure function — no observability
// dependency, fully unit-testable.
func EffectiveRerankAlpha(cfg KBVectorConfig, queryType string) float64 {
	var override float64
	switch queryType {
	case QueryTypeLookup:
		override = cfg.RerankBlendAlphaLookup
	case QueryTypeEnumeration:
		override = cfg.RerankBlendAlphaEnumeration
	case QueryTypeComplexReasoning:
		override = cfg.RerankBlendAlphaComplexReasoning
	default:
		return cfg.RerankBlendAlpha
	}
	if !(override >= 0 && override <= 1) {
		return cfg.RerankBlendAlpha
	}
	return override
}

// EffectiveRerankAlphaForQuery extends EffectiveRerankAlpha with an
// entity-asking-query override. When isEntityQuery is true AND
// queryType == QueryTypeLookup AND cfg.RerankBlendAlphaEntity is a
// valid [0, 1] value, that alpha wins regardless of the per-route
// override. Rationale: entity-asking queries hit a reranker-saturation
// failure mode (many lookalike chunks matching the role/function
// semantics get max scores; the chunks that actually name the entity
// in their prefix lose). A lower α gives BM25 phrase evidence more
// weight to surface those chunks. Sentinel-encoded the same way as
// the per-route overrides.
//
// The override is GATED to lookup-route queries because
// `extractProperNounPhrases` over-extracts on German complex_reasoning
// queries — multi-word capitalized phrases like "Best Practices" and
// "Künstliche Intelligenz" trigger the entity branch even though
// they're common nouns, not entities. Empirically the un-gated entity
// alpha cost complex_reasoning MRR −2.7pp in AB_both_v2
// (2026-05-11) without lifting Q085. Restricting the override to
// `lookup` preserves the +0.9pp lookup MRR while leaving
// complex_reasoning at its reranker-dominant default.
func EffectiveRerankAlphaForQuery(cfg KBVectorConfig, queryType string, isEntityQuery bool) float64 {
	if isEntityQuery && queryType == QueryTypeLookup {
		ent := cfg.RerankBlendAlphaEntity
		if ent >= 0 && ent <= 1 {
			return ent
		}
	}
	return EffectiveRerankAlpha(cfg, queryType)
}

// ComputeRerankScoreStddev returns the population standard deviation of the
// values in scores. Returns 0 for n<2 (stddev is undefined). Used to detect
// reranker calibration collapse: a healthy cross-encoder produces scores
// spread across the [0, 1] band on a heterogeneous candidate set, while a
// miscalibrated one (the Qwen3-Reranker-via-vLLM-pooling pathology
// documented in CLAUDE.md) collapses every score into a 0.01-wide band.
// Pure function: no observability deps so this stays in the scoring layer.
func ComputeRerankScoreStddev(scores map[int]float64) float64 {
	n := len(scores)
	if n < 2 {
		return 0
	}
	var sum float64
	for _, s := range scores {
		sum += s
	}
	mean := sum / float64(n)
	var sqSum float64
	for _, s := range scores {
		d := s - mean
		sqSum += d * d
	}
	return math.Sqrt(sqSum / float64(n))
}
