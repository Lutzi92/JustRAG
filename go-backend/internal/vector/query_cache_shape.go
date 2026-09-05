package vector

import (
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"strings"
)

// queryCacheSchemaVersion is incremented when the SearchResult serialization
// format changes OR when an operator wants to manually invalidate the cache
// without a table truncate. Bumping this value cause-fragments every shape
// hash, effectively invalidating all entries.
//
// Bumped to 3 for Wave-2 Task 6 (bm25_scoring_mode): the keyword arm's
// scoring formula (ts_rank vs. BM25) is a deployment-wide site_config value
// and, per the global constraints doc, is deliberately NOT hashed into the
// shape below — a cached entry produced under one mode must not be served
// once the operator flips the mode, so the version bump does that
// invalidation once, here, instead. Operators must bump this again by hand
// whenever they flip bm25_scoring_mode in production thereafter.
const queryCacheSchemaVersion byte = 3

// shapeHash returns a deterministic identifier for a request's "shape" —
// the set of inputs (besides the query text + embedding) that affect the
// returned SearchResult. Inputs are STRICTLY request-scoped fields on
// SearchOptions plus the resolved topN; deployment-wide site_config values
// (alpha, MMR λ, RRF weights) are intentionally excluded so an admin tweak
// doesn't silently fragment the cache. Operators bump
// queryCacheSchemaVersion to force-invalidate.
//
// Covered request-scoped fields:
//   - Enhance (string): query rewrite/expand/spell mode
//   - QueryType (string): lookup / enumeration / complex_reasoning routing
//   - HyDE / MultiQuery / StepBack / LongContextMode / ForceBM25SimpleArm
//     (bitmask byte): query-transform and retrieval-shape flags
//   - Grade (bitmask byte): CRAG-style relevance grading attaches per-chunk
//     verdicts to the SearchResult, so two requests with different Grade
//     values must not share a cache slot
//   - GraderModel (string): the LLM used by GradeRelevance — different
//     graders produce different verdicts on the same chunks
//   - ModelOverride (string): the LLM used by HyDE / MultiQuery / StepBack
//     auxiliary calls — different overrides produce different generated
//     queries which select different candidate pools
//   - embeddingModel (string): the embedder that produced query_embedding.
//     Same-dimension embedder swaps keep the same cache table, and cosine
//     similarity between vectors from different embedders is meaningless —
//     without this field a swap would silently serve stale-semantics hits
//   - topN (uint64 big-endian): final result-set size cap
//   - FileIDs (sorted): scope filter
//   - SubQueries count (uint16 big-endian): length-only — see inline comment
//   - GraphChunkIDs count (uint16 big-endian): length-only, same compromise
//     as SubQueries — KG re-ingest changes the count → invalidates entry
//   - RawQuery presence (1-byte flag): whether the raw last-turn lane fired
//     — see inline comment for why presence, not content, is hashed
func shapeHash(opts SearchOptions, topN int, embeddingModel string) []byte {
	h := sha256.New()
	h.Write([]byte{queryCacheSchemaVersion})
	h.Write([]byte(opts.Enhance))
	h.Write([]byte{0})
	h.Write([]byte(opts.QueryType))
	h.Write([]byte{0})

	flags := byte(0)
	if opts.HyDE {
		flags |= 0x1
	}
	if opts.MultiQuery {
		flags |= 0x2
	}
	if opts.StepBack {
		flags |= 0x4
	}
	if opts.Grade {
		flags |= 0x8
	}
	// T2-1 long-context mode produces a structurally different
	// SearchResult (wider top-k, no MMR / score filtering) so its
	// cache entries must NOT collide with normal-mode entries.
	if opts.LongContextMode {
		flags |= 0x10
	}
	// The forced simple keyword arm selects a different BM25 candidate
	// pool, so its results must not collide with normal-mode entries.
	if opts.ForceBM25SimpleArm {
		flags |= 0x20
	}
	h.Write([]byte{flags})

	h.Write([]byte(opts.GraderModel))
	h.Write([]byte{0})
	h.Write([]byte(opts.ModelOverride))
	h.Write([]byte{0})
	h.Write([]byte(embeddingModel))
	h.Write([]byte{0})

	var topNBuf [8]byte
	binary.BigEndian.PutUint64(topNBuf[:], uint64(topN))
	h.Write(topNBuf[:])

	// FileIDs sorted to make hash order-independent.
	if len(opts.FileIDs) > 0 {
		sorted := make([]string, len(opts.FileIDs))
		copy(sorted, opts.FileIDs)
		slices.Sort(sorted)
		h.Write([]byte(strings.Join(sorted, "\x00")))
	}

	// SubQueries: caller-driven decomposition. We hash the COUNT only, not
	// the contents, because two callers with different decompositions of
	// the same user question must produce different cache entries (the
	// fusion stage sees different evidence) but exact contents would make
	// the cache effectively per-LLM-call (bad hit rate). Length-only is
	// the deliberate compromise.
	var subQueriesBuf [2]byte
	binary.BigEndian.PutUint16(subQueriesBuf[:], uint16(len(opts.SubQueries)))
	h.Write(subQueriesBuf[:])

	// GraphChunkIDs: AP-C4 graph-router chunk injection. Same length-only
	// compromise as SubQueries. Distinct buffer placement so a (n,0) and
	// (0,n) split between the two extra-list contributors hash distinctly.
	var graphChunksBuf [2]byte
	binary.BigEndian.PutUint16(graphChunksBuf[:], uint16(len(opts.GraphChunkIDs)))
	h.Write(graphChunksBuf[:])

	// RawQuery: rewrite ⊕ raw retrieval lane (Wave 1 Task 7). We hash
	// PRESENCE only (a 1-byte flag), not the raw text itself — the raw
	// string is part of the cache key's query-embedding side (each
	// distinct raw utterance already routes to a different cache row via
	// the query embedding), not the request "shape". What the shape must
	// capture is that the lane fired at all: a cached result produced
	// with the condensed query alone (RawQuery empty/identical, see
	// effectiveRawQuery) must not be served for a request where the raw
	// lane added extra RRF lists, and vice versa. Distinct buffer
	// position (its own byte, after GraphChunkIDs) so this bit can never
	// be confused with any other field's encoding.
	rawQueryFlag := byte(0)
	if opts.RawQuery != "" {
		rawQueryFlag = 1
	}
	h.Write([]byte{rawQueryFlag})

	return h.Sum(nil)
}
