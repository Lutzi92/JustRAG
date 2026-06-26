// Package canonicalize collapses near-duplicate KG entities (same type,
// high embedding similarity, LLM-confirmed) into one canonical node.
package canonicalize

import (
	"math"
	"sort"
)

// CanonEntity is one entity considered for canonicalization.
type CanonEntity struct {
	ID      int64
	Name    string
	Type    string
	Aliases []string
	Degree  int
}

// Pair is a candidate duplicate pair, by index into the entity/embedding slices.
type Pair struct {
	I, J  int
	Score float64
}

// cosine is the cosine similarity of two equal-length vectors. Zero-norm
// inputs return 0 (no signal).
func cosine(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// candidatePairs returns same-type entity pairs whose embedding cosine is >=
// threshold, sorted by score desc and capped at maxPairs (the strongest kept).
// embs is index-aligned to ents; an entity with a missing/empty embedding is
// skipped.
func candidatePairs(ents []CanonEntity, embs [][]float64, threshold float64, maxPairs int) []Pair {
	out := []Pair{}
	for i := 0; i < len(ents); i++ {
		if i >= len(embs) || len(embs[i]) == 0 {
			continue
		}
		for j := i + 1; j < len(ents); j++ {
			if j >= len(embs) || len(embs[j]) == 0 {
				continue
			}
			if ents[i].Type != ents[j].Type {
				continue
			}
			s := cosine(embs[i], embs[j])
			if s >= threshold {
				out = append(out, Pair{I: i, J: j, Score: s})
			}
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		if out[a].I != out[b].I {
			return out[a].I < out[b].I
		}
		return out[a].J < out[b].J
	})
	if maxPairs > 0 && len(out) > maxPairs {
		out = out[:maxPairs]
	}
	return out
}

// cluster groups indices connected by pairs (union-find). Only nodes that
// appear in at least one pair are returned; singletons are omitted. n is the
// total node count (entity slice length).
func cluster(pairs []Pair, n int) [][]int {
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) { parent[find(a)] = find(b) }
	inPair := make(map[int]bool)
	for _, p := range pairs {
		union(p.I, p.J)
		inPair[p.I] = true
		inPair[p.J] = true
	}
	byRoot := map[int][]int{}
	for node := range inPair {
		r := find(node)
		byRoot[r] = append(byRoot[r], node)
	}
	groups := make([][]int, 0, len(byRoot))
	for _, g := range byRoot {
		sort.Ints(g)
		groups = append(groups, g)
	}
	sort.Slice(groups, func(a, b int) bool { return groups[a][0] < groups[b][0] })
	return groups
}

// pickSurvivor chooses the canonical entity of a merge group (highest Degree,
// then longest Name, then smallest ID) and returns the survivor id, the ids to
// fold into it, and the deduped union of every member's Name + Aliases.
func pickSurvivor(group []CanonEntity) (int64, []int64, []string) {
	best := 0
	for i := 1; i < len(group); i++ {
		if better(group[i], group[best]) {
			best = i
		}
	}
	survivorID := group[best].ID
	merged := make([]int64, 0, len(group)-1)
	seen := map[string]bool{}
	aliases := []string{}
	addAlias := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		aliases = append(aliases, s)
	}
	for i, e := range group {
		if i != best {
			merged = append(merged, e.ID)
		}
		addAlias(e.Name)
		for _, a := range e.Aliases {
			addAlias(a)
		}
	}
	sort.Strings(aliases)
	sort.Slice(merged, func(a, b int) bool { return merged[a] < merged[b] })
	return survivorID, merged, aliases
}

// better reports whether candidate c should beat the current best b.
func better(c, b CanonEntity) bool {
	if c.Degree != b.Degree {
		return c.Degree > b.Degree
	}
	if len(c.Name) != len(b.Name) {
		return len(c.Name) > len(b.Name)
	}
	return c.ID < b.ID
}
