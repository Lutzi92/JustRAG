// Package community detects GraphRAG-style communities over the KG relationship
// graph (topology Louvain) and builds per-community summary inputs.
package community

import "sort"

// Edge is one weighted relationship (undirected; both directions contribute).
type Edge struct {
	Src, Dst int64
	Weight   float64
}

// Config tunes detection. Resolution>1 → more, smaller communities. MinSize
// drops communities below the threshold (singletons/pairs aren't useful).
type Config struct {
	Resolution float64
	MinSize    int
}

// Community is a set of entity ids.
type Community struct {
	Members []int64
}

// Detect runs single-level Louvain modularity optimisation over the undirected
// weighted graph and returns communities with >= MinSize members. Deterministic
// (nodes processed in ascending id order; stable community ordering).
func Detect(edges []Edge, cfg Config) []Community {
	if cfg.Resolution <= 0 {
		cfg.Resolution = 1.0
	}
	adj := map[int64]map[int64]float64{}
	deg := map[int64]float64{}
	var m2 float64
	addNode := func(n int64) {
		if adj[n] == nil {
			adj[n] = map[int64]float64{}
		}
	}
	for _, e := range edges {
		if e.Src == e.Dst {
			continue
		}
		w := e.Weight
		if w <= 0 {
			w = 1
		}
		addNode(e.Src)
		addNode(e.Dst)
		adj[e.Src][e.Dst] += w
		adj[e.Dst][e.Src] += w
		deg[e.Src] += w
		deg[e.Dst] += w
		m2 += 2 * w
	}
	if m2 == 0 {
		return nil
	}
	nodes := make([]int64, 0, len(adj))
	for n := range adj {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i] < nodes[j] })

	comm := map[int64]int64{}
	sigmaTot := map[int64]float64{}
	for _, n := range nodes {
		comm[n] = n
		sigmaTot[n] = deg[n]
	}

	improved := true
	for improved {
		improved = false
		for _, n := range nodes {
			cur := comm[n]
			sigmaTot[cur] -= deg[n]
			toComm := map[int64]float64{}
			for nb, w := range adj[n] {
				toComm[comm[nb]] += w
			}
			bestC, bestGain := cur, 0.0
			for c, kIn := range toComm {
				gain := kIn - cfg.Resolution*deg[n]*sigmaTot[c]/m2
				if gain > bestGain {
					bestGain, bestC = gain, c
				}
			}
			if bestC != cur {
				comm[n] = bestC
				improved = true
			}
			sigmaTot[comm[n]] += deg[n]
		}
	}

	byComm := map[int64][]int64{}
	for _, n := range nodes {
		byComm[comm[n]] = append(byComm[comm[n]], n)
	}
	out := make([]Community, 0, len(byComm))
	for _, members := range byComm {
		if len(members) < cfg.MinSize {
			continue
		}
		sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
		out = append(out, Community{Members: members})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Members[0] < out[j].Members[0] })
	return out
}
