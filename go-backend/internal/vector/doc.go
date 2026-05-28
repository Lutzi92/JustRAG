// Package vector is the retrieval layer: it owns the hybrid search pipeline
// that turns a user query into a ranked list of chunks ready for the LLM.
//
// The end-to-end pipeline (most of it lives in search.go) is:
//
//  1. Query-side embedding (with optional asymmetric instruction prefix,
//     HyDE, multi-query expansion, and step-back broader rephrasing).
//  2. Optional semantic query cache lookup (exact (kb_id, shape_hash) hit
//     plus pgvector ANN paraphrase hit).
//  3. Parallel vector + BM25 keyword searches (the latter using
//     phraseto_tsquery for quoted terms, websearch_to_tsquery elsewhere).
//  4. RRF fusion across all candidate lists (vector, BM25, multi-query
//     alternates, step-back).
//  5. Optional cross-encoder reranker pass with a configurable
//     score-blend (alpha * rerank + (1-alpha) * normalized_RRF).
//  6. MMR diversification on Jaccard word overlap.
//  7. BM25 floor reinsertion: top-BM25 chunks dropped along the way are
//     reinserted at the boundaries of the final set so sandwich layout
//     places named-entity matches at attention boundaries.
//
// Configuration (alpha, MMR lambda, top-N, route-specific overrides) lives
// in the admin site_configs row; this package reads it through SiteConfigReader
// rather than the *config.Config startup struct.
package vector
