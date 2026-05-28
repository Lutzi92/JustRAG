package kg

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/justrag/go-backend/internal/observability"
)

// EdgeRow is the in-memory projection of one kg_edges row used by
// the graph-traversal algorithms (PPR + path finding). Rel and
// Evidence are dropped because neither algorithm reasons about
// labels — they walk topology and weights. ChunkID is kept so the
// callers can collect citation-ready chunk_ids along the traversal.
type EdgeRow struct {
	Src     int64
	Dst     int64
	Weight  float64
	ChunkID string
}

// PPRConfig tunes the iterative Personalized PageRank. Defaults
// match the HippoRAG 2 paper: damping 0.85, max 20 iterations,
// convergence threshold 1e-4 on the L1 norm of the score delta.
// Callers that want different behaviour pass overrides; zero
// fields fall through to the defaults via Sanitize.
type PPRConfig struct {
	Damping     float64 // teleport probability complement; 0.85 = standard
	MaxIter     int     // hard cap on iterations even without convergence
	Convergence float64 // L1-norm delta below which we declare convergence
}

// Sanitize fills zero / out-of-range fields with the documented
// defaults. Returns a copy so the caller's config struct is not
// mutated.
func (c PPRConfig) Sanitize() PPRConfig {
	if c.Damping <= 0 || c.Damping >= 1 {
		c.Damping = 0.85
	}
	if c.MaxIter < 1 || c.MaxIter > 200 {
		c.MaxIter = 20
	}
	if c.Convergence <= 0 || c.Convergence >= 1 {
		c.Convergence = 1e-4
	}
	return c
}

// PersonalizedPageRank runs the iterative PPR algorithm over an
// in-memory edge list. The graph is treated as UNDIRECTED — for
// every kg_edge (u, v) we contribute mass both u→v and v→u — which
// matches LookupSubgraph's own treatment ("the direction of a
// relation in extracted text is rarely canonical").
//
// Returns a score map (entity_id → score). Higher score = more
// structurally important to the seed set, in the graph-theoretic
// sense that random walks from the seeds frequently land here.
// Seeds with no outgoing edges still score (1-d)/|S| via the
// teleport term, so a totally isolated seed entity still surfaces
// itself as the top-1 result.
//
// Empty seeds → empty result; empty edges → seeds with uniform
// teleport probability (so a graph with no edges yields exactly
// the seed set scored equally). Both are valid inputs; the caller
// decides whether they're useful.
//
// Complexity: O(MaxIter × (|edges| + |entities|)). For a KB with
// ~10k entities and ~50k edges, MaxIter=20, this is ~1M flops
// per call — sub-millisecond on commodity hardware.
func PersonalizedPageRank(seeds []int64, edges []EdgeRow, cfg PPRConfig) map[int64]float64 {
	cfg = cfg.Sanitize()
	if len(seeds) == 0 {
		return nil
	}

	// Build adjacency: for each node, the list of (neighbour,
	// weight) it pushes mass to. Undirected: each edge contributes
	// to both ends.
	adj := make(map[int64][]struct {
		dst    int64
		weight float64
	})
	outWeight := make(map[int64]float64)
	for _, e := range edges {
		w := e.Weight
		if w <= 0 {
			// A non-positive weight would zero the contribution and
			// could be a data-quality bug; clamp to 1.0 (the schema
			// default) so PPR stays well-defined.
			w = 1.0
		}
		adj[e.Src] = append(adj[e.Src], struct {
			dst    int64
			weight float64
		}{e.Dst, w})
		outWeight[e.Src] += w
		adj[e.Dst] = append(adj[e.Dst], struct {
			dst    int64
			weight float64
		}{e.Src, w})
		outWeight[e.Dst] += w
	}

	// Personalisation vector: uniform over the seed set. Seeds with
	// duplicate IDs collapse (rare — caller usually passes deduped
	// list, but defensive).
	personalisation := make(map[int64]float64, len(seeds))
	seedSet := make(map[int64]bool, len(seeds))
	for _, s := range seeds {
		seedSet[s] = true
	}
	pMass := 1.0 / float64(len(seedSet))
	for s := range seedSet {
		personalisation[s] = pMass
	}

	// Score map. Initialise from the personalisation so the first
	// iteration's push has well-defined source mass. Entities not in
	// the seed set start at 0.
	scores := make(map[int64]float64, len(adj))
	for s, m := range personalisation {
		scores[s] = m
	}

	// All entities touched by edges OR seeds — the iteration
	// universe. Without this the score map only ever holds the seed
	// rows + their direct neighbours and we'd undercount transitive
	// reach.
	allNodes := make(map[int64]bool, len(adj))
	for n := range adj {
		allNodes[n] = true
	}
	for s := range seedSet {
		allNodes[s] = true
	}

	teleport := 1.0 - cfg.Damping
	for iter := 0; iter < cfg.MaxIter; iter++ {
		next := make(map[int64]float64, len(allNodes))
		// Teleport step: every node gets (1-d) × personalisation_mass.
		// Nodes outside the seed set receive 0 from teleport — that's
		// what makes the variant "personalised" vs. global PageRank.
		for n := range allNodes {
			next[n] = teleport * personalisation[n]
		}
		// Push step: each node sends d × (score / weighted_out) to
		// each neighbour, weighted by edge weight. Nodes with no
		// outgoing edges (outWeight==0) keep their mass within the
		// teleport pool — we don't dangle-distribute back to seeds
		// because the seed set already absorbs all teleport mass.
		for n, score := range scores {
			out := outWeight[n]
			if out == 0 {
				// Isolated node — fold its mass into the teleport
				// pool for the seed set so total probability stays at
				// 1.0. Without this drift the seed mass would slowly
				// leak into the void on graphs with sinks.
				for s, m := range personalisation {
					next[s] += cfg.Damping * score * m
				}
				continue
			}
			factor := cfg.Damping * score / out
			for _, nb := range adj[n] {
				next[nb.dst] += factor * nb.weight
			}
		}

		// L1 convergence check. Compute over the union so a brand-new
		// node (first iteration after seeding) counts even when its
		// previous score was zero.
		var l1 float64
		for n := range allNodes {
			l1 += math.Abs(next[n] - scores[n])
		}
		scores = next
		if l1 < cfg.Convergence {
			observability.RecordKGPPRConverged(iter + 1)
			break
		}
	}

	return scores
}

// PPREntities runs PPR over the KB's edge set and returns the
// top-K entities by score (descending). When seeds is empty or the
// store has no edges for the KB, an empty slice + nil error are
// returned so callers can degrade gracefully.
//
// topK is clamped to [1, 200]. Larger pools produce diminishing
// returns — the chat-layer chunk-injection cap is much smaller
// (typically 15) so anything above ~50 entities only matters for
// trajectory eval.
func (s *PgStore) PPREntities(ctx context.Context, kbID string, seedIDs []int64, topK int, cfg PPRConfig) ([]Entity, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("kg: no DB pool configured")
	}
	if len(seedIDs) == 0 {
		return nil, nil
	}
	if topK < 1 {
		topK = 1
	}
	if topK > 200 {
		topK = 200
	}

	start := time.Now()
	defer func() {
		observability.ObserveKGPPRSeconds(time.Since(start).Seconds())
	}()

	edges, err := s.loadEdges(ctx, kbID)
	if err != nil {
		return nil, err
	}

	scores := PersonalizedPageRank(seedIDs, edges, cfg)
	if len(scores) == 0 {
		return nil, nil
	}

	type scored struct {
		id    int64
		score float64
	}
	ranked := make([]scored, 0, len(scores))
	for id, sc := range scores {
		// Drop seeds from the candidate pool — they're already in
		// the result set the caller has (otherwise PPR wouldn't have
		// fired) and folding them back as "neighbours of themselves"
		// would steal slots from genuinely new findings. The chat-
		// layer caller's own chunk fetch already includes the seeds.
		if isSeed(id, seedIDs) {
			continue
		}
		ranked = append(ranked, scored{id, sc})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		// Stable secondary sort by id so identical-score ties don't
		// flap between runs (tests expect deterministic ordering).
		return ranked[i].id < ranked[j].id
	})
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}
	if len(ranked) == 0 {
		return nil, nil
	}

	// Bulk-load the entity rows. One round-trip via ANY(int[]) is
	// cheaper than N point lookups even at topK=200.
	ids := make([]int64, len(ranked))
	for i, r := range ranked {
		ids[i] = r.id
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, canonical_name, type, aliases
		  FROM kg_entities
		 WHERE kb_id = $1::uuid AND id = ANY($2::bigint[])
	`, kbID, ids)
	if err != nil {
		return nil, fmt.Errorf("kg: ppr fetch entities: %w", err)
	}
	defer rows.Close()

	// Re-key by id so we can preserve the PPR-score ordering.
	byID := make(map[int64]Entity, len(ranked))
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.CanonicalName, &e.Type, &e.Aliases); err != nil {
			return nil, fmt.Errorf("kg: ppr scan entity: %w", err)
		}
		byID[e.ID] = e
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kg: ppr rows: %w", err)
	}

	out := make([]Entity, 0, len(ranked))
	for _, r := range ranked {
		if e, ok := byID[r.id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// PPRChunks runs PPR, picks the top-K most relevant entities, and
// returns the deduped chunk_ids of edges incident to those
// entities (capped to maxChunks). The two-stage shape mirrors
// ResolveGraphChunks's neighbours mode: pick the relevant entities
// first, then collect their evidence chunks.
//
// Chunks are ordered by aggregate PPR score of their incident
// entities — a chunk whose two endpoints are both highly relevant
// outranks a chunk whose only relevant endpoint is the seed. This
// matters when maxChunks << candidate pool size: the cap then
// preserves the strongest-evidence chunks rather than dropping by
// arbitrary id order.
func (s *PgStore) PPRChunks(ctx context.Context, kbID string, seedIDs []int64, topEntities, maxChunks int, cfg PPRConfig) ([]string, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("kg: no DB pool configured")
	}
	if maxChunks < 1 || len(seedIDs) == 0 {
		return nil, nil
	}

	// Reuse the same edge load for scoring + chunk projection so we
	// only hit Postgres once.
	edges, err := s.loadEdges(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if len(edges) == 0 {
		return nil, nil
	}

	scores := PersonalizedPageRank(seedIDs, edges, cfg)
	if len(scores) == 0 {
		return nil, nil
	}

	// Pick the top-K entities by PPR score, EXCLUDING the seeds.
	// Same reasoning as PPREntities — seeds already feed the chat
	// layer's primary path; PPR's job is to add the
	// structurally-relevant neighbours.
	type scored struct {
		id    int64
		score float64
	}
	ranked := make([]scored, 0, len(scores))
	for id, sc := range scores {
		if isSeed(id, seedIDs) {
			continue
		}
		ranked = append(ranked, scored{id, sc})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].id < ranked[j].id
	})
	if topEntities < 1 {
		topEntities = 1
	}
	if len(ranked) > topEntities {
		ranked = ranked[:topEntities]
	}

	pool := make(map[int64]float64, len(ranked))
	for _, r := range ranked {
		pool[r.id] = r.score
	}
	// Seeds count as "in the pool" for chunk projection so their
	// direct evidence still surfaces even though they don't appear
	// in `ranked` (we excluded them above).
	for _, s := range seedIDs {
		// Use a small ceiling for seeds so they never outweigh a
		// genuinely high-PPR neighbour — but stay positive so any
		// seed-incident chunk still scores above seedless ones.
		if _, ok := pool[s]; !ok {
			pool[s] = 1.0
		}
	}

	// Aggregate chunk scores by walking edges. A chunk gets the
	// sum of its endpoints' pool scores (zero if neither endpoint
	// is in the pool — that chunk is irrelevant). Edges without a
	// chunk_id contribute nothing.
	chunkScores := make(map[string]float64)
	for _, e := range edges {
		if e.ChunkID == "" {
			continue
		}
		s1, in1 := pool[e.Src]
		s2, in2 := pool[e.Dst]
		if !in1 && !in2 {
			continue
		}
		chunkScores[e.ChunkID] += s1 + s2
	}
	if len(chunkScores) == 0 {
		return nil, nil
	}

	type chunkScored struct {
		id    string
		score float64
	}
	chunkRanked := make([]chunkScored, 0, len(chunkScores))
	for id, sc := range chunkScores {
		chunkRanked = append(chunkRanked, chunkScored{id, sc})
	}
	sort.Slice(chunkRanked, func(i, j int) bool {
		if chunkRanked[i].score != chunkRanked[j].score {
			return chunkRanked[i].score > chunkRanked[j].score
		}
		return chunkRanked[i].id < chunkRanked[j].id
	})
	if len(chunkRanked) > maxChunks {
		chunkRanked = chunkRanked[:maxChunks]
	}
	out := make([]string, len(chunkRanked))
	for i, c := range chunkRanked {
		out[i] = c.id
	}
	return out, nil
}

// loadEdges pulls every (src, dst, weight, chunk_id) row for the KB.
// Edges without a chunk_id are still useful for PPR (topology
// matters) but PPRChunks filters them out at projection time.
func (s *PgStore) loadEdges(ctx context.Context, kbID string) ([]EdgeRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT src_entity_id, dst_entity_id, weight, COALESCE(chunk_id::text, '')
		  FROM kg_edges
		 WHERE kb_id = $1::uuid
	`, kbID)
	if err != nil {
		return nil, fmt.Errorf("kg: load edges: %w", err)
	}
	defer rows.Close()
	out := make([]EdgeRow, 0, 64)
	for rows.Next() {
		var e EdgeRow
		if err := rows.Scan(&e.Src, &e.Dst, &e.Weight, &e.ChunkID); err != nil {
			return nil, fmt.Errorf("kg: scan edge row: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kg: edge rows: %w", err)
	}
	return out, nil
}

// isSeed reports whether id appears in seedIDs. Linear scan — fine
// because seedIDs is typically 1-3 entities from the alias router.
func isSeed(id int64, seedIDs []int64) bool {
	for _, s := range seedIDs {
		if s == id {
			return true
		}
	}
	return false
}
