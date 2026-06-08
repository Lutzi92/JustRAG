package kg

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/justrag/go-backend/internal/observability"
)

// RelationalPath is one ordered chain of edges connecting a src
// entity to a dst entity. The path's chunk_ids (in order, deduped)
// are surfaced separately so the chat-layer caller can fold them
// into the RRF pool without re-walking the edge list.
//
// Length is the number of edges traversed (= len(Edges)); a direct
// edge between src and dst has Length 1. Score is the path's
// quality score — currently the inverse of length so shorter paths
// rank higher (PathRAG's "S(P) = exp(-len(P))" up to a constant).
// Callers compare scores within one FindRelationalPaths result;
// scores across different (src, dst) pairs are not directly
// comparable because the path-length distribution shifts.
type RelationalPath struct {
	Length   int
	Score    float64
	Edges    []EdgeRow
	ChunkIDs []string
}

// PathConfig tunes FindRelationalPaths. Zero / out-of-range values
// fall back to defaults via Sanitize.
type PathConfig struct {
	MaxLen   int // hard cap on path length (number of edges)
	MaxPaths int // hard cap on returned paths
}

// Sanitize returns a copy with defaults filled in. MaxLen defaults
// to 3 (PathRAG paper's sweet spot — multi-hop reasoning rarely
// benefits past 3 hops on KG-scale graphs and the path count
// explodes combinatorially). MaxPaths defaults to 5 — enough to
// surface alternative paths through different intermediaries
// without swamping the chunk pool.
func (c PathConfig) Sanitize() PathConfig {
	if c.MaxLen < 1 || c.MaxLen > 6 {
		c.MaxLen = 3
	}
	if c.MaxPaths < 1 || c.MaxPaths > 50 {
		c.MaxPaths = 5
	}
	return c
}

// FindRelationalPaths returns up to MaxPaths paths of length ≤
// MaxLen connecting srcID to dstID, walking the KB's edge set
// undirectionally (matching LookupSubgraph's bidirectional treatment).
// Paths are ordered by length ascending (shortest first), ties
// broken by aggregate edge weight (heavier-weight evidence first).
//
// Returns empty + nil error when src == dst or when no path of the
// allowed length exists — these are both valid "no result"
// outcomes the chat layer should fall back from to plain
// neighbours mode.
//
// Algorithm: bounded BFS that records ALL paths reaching dst, not
// just the shortest. The MaxPaths cap stops enumeration early once
// we have enough. Memory bound is O(MaxPaths × MaxLen × edge_size)
// which stays comfortable even at the configured ceilings.
func (s *PgStore) FindRelationalPaths(ctx context.Context, kbID string, srcID, dstID int64, cfg PathConfig) ([]RelationalPath, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("kg: no DB pool configured")
	}
	cfg = cfg.Sanitize()
	if srcID == dstID {
		return nil, nil
	}

	start := time.Now()
	defer func() {
		observability.ObserveKGPathsSeconds(time.Since(start).Seconds())
	}()

	edges, err := s.loadEdges(ctx, kbID)
	if err != nil {
		return nil, err
	}
	if len(edges) == 0 {
		observability.RecordKGPathsFound(0)
		return nil, nil
	}

	// Build undirected adjacency: each node → list of (neighbour, edge).
	// Storing the edge (not just the neighbour) lets us reconstruct
	// the path with its chunk_ids without a second lookup.
	type adjEntry struct {
		nb   int64
		edge EdgeRow
	}
	adj := make(map[int64][]adjEntry, len(edges))
	for _, e := range edges {
		adj[e.Src] = append(adj[e.Src], adjEntry{nb: e.Dst, edge: e})
		// Reversed view of the same edge — keep ChunkID identical so
		// dedup along a path catches the row regardless of direction.
		rev := EdgeRow{Src: e.Dst, Dst: e.Src, Weight: e.Weight, ChunkID: e.ChunkID}
		adj[e.Dst] = append(adj[e.Dst], adjEntry{nb: e.Src, edge: rev})
	}

	// BFS that tracks the path back to src for each frontier entry.
	// We do NOT use a visited-set the usual way: BFS over paths
	// (not nodes), with each path tracking its own visited node
	// set to avoid cycles. This is what lets us enumerate multiple
	// paths rather than just one shortest.
	type frontierEntry struct {
		node    int64
		visited map[int64]bool
		edges   []EdgeRow
	}
	initVisited := map[int64]bool{srcID: true}
	frontier := []frontierEntry{{node: srcID, visited: initVisited, edges: nil}}

	var found []RelationalPath
	for hop := 0; hop < cfg.MaxLen && len(frontier) > 0; hop++ {
		var next []frontierEntry
		for _, fe := range frontier {
			for _, ae := range adj[fe.node] {
				if fe.visited[ae.nb] {
					continue // skip cycles
				}
				newEdges := make([]EdgeRow, len(fe.edges)+1)
				copy(newEdges, fe.edges)
				newEdges[len(fe.edges)] = ae.edge
				if ae.nb == dstID {
					found = append(found, buildPath(newEdges))
					if len(found) >= cfg.MaxPaths*4 {
						// Over-fetch by 4× the configured cap so the
						// final ordering pass can keep the best
						// paths (e.g. heavier weight at the same
						// length) instead of just "the first N we
						// found." We trim in the sort step below.
						goto enumeration_done
					}
					// Don't extend further from dst — a path that
					// passes through dst and continues isn't useful.
					continue
				}
				newVisited := make(map[int64]bool, len(fe.visited)+1)
				for k, v := range fe.visited {
					newVisited[k] = v
				}
				newVisited[ae.nb] = true
				next = append(next, frontierEntry{
					node:    ae.nb,
					visited: newVisited,
					edges:   newEdges,
				})
			}
		}
		frontier = next
	}
enumeration_done:

	if len(found) == 0 {
		observability.RecordKGPathsFound(0)
		return nil, nil
	}

	// Order: shortest first; ties broken by aggregate weight
	// descending (heavier evidence wins). Final trim to MaxPaths.
	slices.SortFunc(found, func(a, b RelationalPath) int {
		// Length ascending (shortest first); ties broken by aggregate edge
		// weight descending so heavier evidence wins.
		return cmp.Or(
			cmp.Compare(a.Length, b.Length),
			cmp.Compare(totalWeight(b.Edges), totalWeight(a.Edges)),
		)
	})
	if len(found) > cfg.MaxPaths {
		found = found[:cfg.MaxPaths]
	}
	observability.RecordKGPathsFound(len(found))
	return found, nil
}

// buildPath wraps a sequence of edges into the RelationalPath shape
// with derived fields (Length, Score, ChunkIDs).
func buildPath(edges []EdgeRow) RelationalPath {
	p := RelationalPath{
		Length: len(edges),
		Edges:  edges,
	}
	// Score: pure inverse-length (PathRAG: shorter == stronger). We
	// don't fold weight in here — weight is a tiebreaker in the
	// outer ordering, not part of the score the caller sees, so the
	// score is comparable across (src, dst) calls with identical
	// MaxLen.
	switch p.Length {
	case 0:
		p.Score = 0
	default:
		p.Score = 1.0 / float64(p.Length)
	}
	// Dedup chunk IDs along the path while preserving first-seen
	// order. A path that revisits the same chunk_id (extracted from
	// the same chunk for two adjacent relations) still gets a single
	// citation, which is what the chat-layer caller wants when
	// folding into the RRF pool.
	seen := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		if e.ChunkID == "" {
			continue
		}
		if _, dup := seen[e.ChunkID]; dup {
			continue
		}
		seen[e.ChunkID] = struct{}{}
		p.ChunkIDs = append(p.ChunkIDs, e.ChunkID)
	}
	return p
}

// totalWeight sums the weights along a path. Used only for the
// secondary sort key when two paths share a length.
func totalWeight(edges []EdgeRow) float64 {
	var w float64
	for _, e := range edges {
		w += e.Weight
	}
	return w
}

// PathChunks runs FindRelationalPaths between two entities and
// returns the deduped chunk_ids across all returned paths, capped
// at maxChunks. Chunks that appear in multiple short paths get
// listed earlier (a chunk on three different paths between src and
// dst is stronger evidence than one on a single path).
//
// Returns an empty slice on disconnected (src, dst) pairs so the
// chat-layer caller falls back to plain neighbours mode.
func (s *PgStore) PathChunks(ctx context.Context, kbID string, srcID, dstID int64, maxChunks int, cfg PathConfig) ([]string, error) {
	if maxChunks < 1 {
		return nil, nil
	}
	paths, err := s.FindRelationalPaths(ctx, kbID, srcID, dstID, cfg)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}

	// Aggregate chunk frequencies × path-score: a chunk's value is
	// the sum of the scores of paths it appears on. Then order by
	// that score, ties by chunk_id for determinism.
	type chunkScored struct {
		id    string
		score float64
	}
	scoreByID := make(map[string]float64)
	for _, p := range paths {
		for _, cid := range p.ChunkIDs {
			scoreByID[cid] += p.Score
		}
	}
	ranked := make([]chunkScored, 0, len(scoreByID))
	for id, sc := range scoreByID {
		ranked = append(ranked, chunkScored{id, sc})
	}
	slices.SortFunc(ranked, func(a, b chunkScored) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.id, b.id))
	})
	if len(ranked) > maxChunks {
		ranked = ranked[:maxChunks]
	}
	out := make([]string, len(ranked))
	for i, r := range ranked {
		out[i] = r.id
	}
	return out, nil
}

