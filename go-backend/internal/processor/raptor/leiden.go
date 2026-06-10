package raptor

import (
	"fmt"
	"math/rand/v2"
	"sort"
)

// LeidenConfig tunes the modularity-based clustering. Zero values
// fall through to documented defaults via Sanitize.
//
// We implement a single-level Louvain-style modularity optimisation
// over a k-NN similarity graph: each point connects to its
// KNeighbors most-similar neighbours, nodes iteratively migrate to
// the neighbour community that maximises modularity, and the
// resulting partition is returned. Single-level rather than the
// full multi-resolution Leiden hierarchy because RAPTOR's inputs
// are 25-1000 leaves per file — small enough that the aggregation
// pass adds latency without measurable quality lift, and the
// recursive RAPTOR tree already provides the multi-level structure
// the full Leiden algorithm aims for.
//
// Resolution is the γ in modularity Q = (1/2m) Σ [A_ij - γ·k_i·k_j/2m]·δ.
// γ > 1 produces more, smaller communities; γ < 1 produces fewer,
// larger ones. γ = 1 is the classical Newman-Girvan modularity and
// matches the 20 % accuracy lift reported in the 2025 Frontiers
// adaptive-RAPTOR paper.
type LeidenConfig struct {
	KNeighbors int     // k for the k-NN graph; default 10
	Resolution float64 // γ for modularity; default 1.0
	MaxIter    int     // hard cap on outer iterations; default 20
	Seed       int64   // deterministic node-traversal order
}

// Sanitize fills missing / out-of-range fields with defaults.
func (c LeidenConfig) Sanitize() LeidenConfig {
	if c.KNeighbors < 2 || c.KNeighbors > 200 {
		c.KNeighbors = 10
	}
	if c.Resolution <= 0 || c.Resolution > 10 {
		c.Resolution = 1.0
	}
	if c.MaxIter < 1 || c.MaxIter > 200 {
		c.MaxIter = 20
	}
	return c
}

// ClusterByLeiden groups points into communities using
// modularity-based community detection over a k-NN cosine-similarity
// graph. Unlike ClusterByKMeans which takes a fixed k, the number
// of clusters is determined by the graph topology and the
// resolution parameter — heterogeneous documents naturally yield
// more, tighter clusters; homogeneous ones yield fewer, larger.
//
// Returns one Cluster per discovered community, each with its
// centroid (member-mean embedding) populated. Empty points or a
// degenerate KNeighbors return an error. Single-point input
// returns one cluster (consistent with ClusterByKMeans).
func ClusterByLeiden(points []ClusterInput, cfg LeidenConfig) ([]Cluster, error) {
	if len(points) == 0 {
		return nil, fmt.Errorf("leiden: empty input")
	}
	cfg = cfg.Sanitize()
	if len(points) == 1 {
		return []Cluster{{
			Centroid: append([]float64(nil), points[0].Embedding...),
			Members:  []ClusterInput{points[0]},
		}}, nil
	}

	// Build k-NN similarity graph. Each row of `adj` is the list of
	// (neighbour, weight) pairs for node i. weight = cosine
	// similarity, clamped to (0, 2] so a perfectly-opposing pair
	// (extremely rare on production-embedded text) doesn't crater
	// modularity arithmetic. Unit-norm vectors → cosine =
	// 1 - 0.5·sqEuclidean.
	n := len(points)
	k := cfg.KNeighbors
	if k >= n {
		k = n - 1
	}
	adj := make([][]edgeTo, n)
	type pair struct {
		idx   int
		simSq float64
	}
	for i := 0; i < n; i++ {
		// Compute squared distance to every other point; pick the
		// k smallest. The k-NN graph is the foundation Leiden
		// operates on, so this is the dominant cost (O(n² · dim)).
		// Acceptable at n ≤ 1000; RAPTOR's MinChunks already
		// gates this.
		dists := make([]pair, 0, n-1)
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			dists = append(dists, pair{j, sqEuclidean(points[i].Embedding, points[j].Embedding)})
		}
		sort.Slice(dists, func(a, b int) bool { return dists[a].simSq < dists[b].simSq })
		if len(dists) > k {
			dists = dists[:k]
		}
		row := make([]edgeTo, 0, len(dists))
		for _, d := range dists {
			// Cosine similarity on unit-norm vectors. Clamp to
			// (0, 2] so a degenerate pair doesn't poison
			// modularity. Values <= 0 happen when distance ≥ 2
			// (vectors point opposite directions); we floor at a
			// tiny positive so the edge still counts but barely.
			cos := 1.0 - 0.5*d.simSq
			if cos < 1e-6 {
				cos = 1e-6
			}
			row = append(row, edgeTo{nb: d.idx, w: cos})
		}
		adj[i] = row
	}

	// Symmetrise: if i lists j as a neighbour but not vice-versa,
	// add the back-edge with the same weight. Modularity is
	// defined on undirected graphs; without symmetrisation a
	// directed-only k-NN graph would produce asymmetric modularity
	// gain calculations that flap between iterations.
	adj = symmetrise(adj)

	// Initialise: each node in its own community.
	community := make([]int, n)
	for i := range community {
		community[i] = i
	}

	// Per-node strength (sum of incident edge weights) and per-
	// community total strength. The modularity gain formula reads
	// these on every move, so caching them avoids an O(degree)
	// recompute per candidate move.
	nodeStrength := make([]float64, n)
	var totalEdgeWeight float64
	for i, row := range adj {
		for _, e := range row {
			nodeStrength[i] += e.w
		}
		totalEdgeWeight += nodeStrength[i]
	}
	// Edge weights counted both directions → halve.
	m := totalEdgeWeight / 2.0
	if m == 0 {
		// No edges (e.g. KNeighbors=0 after clamping) — return
		// every point as its own cluster.
		return singletonClusters(points), nil
	}
	commStrength := make(map[int]float64, n)
	for i, s := range nodeStrength {
		commStrength[community[i]] += s
	}

	rng := rand.New(rand.NewPCG(uint64(cfg.Seed), uint64(cfg.Seed>>32)))
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}

	for iter := 0; iter < cfg.MaxIter; iter++ {
		// Shuffle traversal order each iteration. Without the
		// shuffle Louvain converges to a deterministic local
		// optimum that depends on node-id order — usually fine,
		// but a shuffled order tends to find better optima on
		// non-trivial graphs.
		rng.Shuffle(n, func(a, b int) {
			order[a], order[b] = order[b], order[a]
		})

		moved := false
		for _, i := range order {
			currentComm := community[i]
			// Compute the weight of i's edges into each adjacent
			// community (including its own). Maps because the set
			// of touched communities is bounded by i's degree, not
			// total comm count.
			weightIntoComm := make(map[int]float64, len(adj[i]))
			for _, e := range adj[i] {
				weightIntoComm[community[e.nb]] += e.w
			}

			// Best candidate: the community that maximises
			// modularity gain when i moves there from its
			// current community. The empty-gain case is i's own
			// community (the no-op move).
			bestComm := currentComm
			bestGain := 0.0

			// Strength of i's edges into its own community
			// (excluding self-edges, of which there are none in
			// our k-NN graph).
			kI := nodeStrength[i]
			// Community-strength MINUS kI for the
			// "what would current be without i" baseline.
			sigmaCurrentMinusI := commStrength[currentComm] - kI

			for c, kIinC := range weightIntoComm {
				if c == currentComm {
					continue
				}
				sigmaC := commStrength[c]
				// Modularity-gain formula (Blondel et al. 2008,
				// adapted for resolution γ):
				//   ΔQ = (kIinC - kIinCurrent) / m
				//        - γ · kI · (sigmaC - sigmaCurrentMinusI) / (2m²)
				// We expand kIinCurrent inside the loop because
				// it's constant per i across candidate c's.
				kIinCurrent := weightIntoComm[currentComm] // 0 if no edges back
				deltaA := (kIinC - kIinCurrent) / m
				deltaExpect := cfg.Resolution * kI * (sigmaC - sigmaCurrentMinusI) / (2 * m * m)
				gain := deltaA - deltaExpect
				if gain > bestGain {
					bestGain = gain
					bestComm = c
				}
			}

			if bestComm != currentComm {
				community[i] = bestComm
				commStrength[currentComm] -= kI
				if commStrength[currentComm] <= 0 {
					delete(commStrength, currentComm)
				}
				commStrength[bestComm] += kI
				moved = true
			}
		}
		if !moved {
			break
		}
	}

	return materialiseClusters(points, community), nil
}

// edgeTo is one outbound edge from the implicit current node.
type edgeTo struct {
	nb int
	w  float64
}

// symmetrise ensures that for every (i→j, w) there is a matching
// (j→i, w). When both directions already exist the weight is left
// unchanged — k-NN graphs from cosine similarity are symmetric on
// the weight axis even if not on the membership axis. Duplicate
// detection uses a small map keyed by (i, j) to avoid quadratic
// nested loops over adj[i] for every back-edge.
func symmetrise(adj [][]edgeTo) [][]edgeTo {
	type key struct{ a, b int }
	seen := make(map[key]struct{}, 4*len(adj))
	for i, row := range adj {
		for _, e := range row {
			seen[key{i, e.nb}] = struct{}{}
		}
	}
	for i, row := range adj {
		for _, e := range row {
			if _, ok := seen[key{e.nb, i}]; ok {
				continue
			}
			adj[e.nb] = append(adj[e.nb], edgeTo{nb: i, w: e.w})
			seen[key{e.nb, i}] = struct{}{}
		}
	}
	return adj
}

// singletonClusters returns one cluster per input point. Used as
// the degenerate-graph fallback when modularity has no signal.
func singletonClusters(points []ClusterInput) []Cluster {
	out := make([]Cluster, len(points))
	for i, p := range points {
		out[i] = Cluster{
			Centroid: append([]float64(nil), p.Embedding...),
			Members:  []ClusterInput{p},
		}
	}
	return out
}

// materialiseClusters turns the per-node community label vector
// into the []Cluster output: gathers members and computes per-
// community centroids as the member-mean of the embeddings.
//
// Communities are renumbered to a dense range so the output is
// stable across runs that happen to land on different internal
// labels. Order is by ascending member count first (smallest
// community first → larger clusters later), then by minimum
// member ID for ties. Deterministic ordering makes test fixtures
// easy.
func materialiseClusters(points []ClusterInput, community []int) []Cluster {
	type group struct {
		members []ClusterInput
		minID   string
	}
	bag := make(map[int]*group, len(points))
	for i, p := range points {
		g, ok := bag[community[i]]
		if !ok {
			g = &group{minID: p.ID}
			bag[community[i]] = g
		} else if p.ID < g.minID {
			g.minID = p.ID
		}
		g.members = append(g.members, p)
	}

	// Stable ordering: smaller community first, ties by minID asc.
	keys := make([]int, 0, len(bag))
	for k := range bag {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		gi, gj := bag[keys[i]], bag[keys[j]]
		if len(gi.members) != len(gj.members) {
			return len(gi.members) < len(gj.members)
		}
		return gi.minID < gj.minID
	})

	out := make([]Cluster, 0, len(keys))
	for _, k := range keys {
		g := bag[k]
		if len(g.members) == 0 {
			continue
		}
		dim := len(g.members[0].Embedding)
		centroid := make([]float64, dim)
		for _, m := range g.members {
			for d := 0; d < dim; d++ {
				centroid[d] += m.Embedding[d]
			}
		}
		inv := 1.0 / float64(len(g.members))
		for d := 0; d < dim; d++ {
			centroid[d] *= inv
		}
		out = append(out, Cluster{
			Centroid: centroid,
			Members:  g.members,
		})
	}
	return out
}
