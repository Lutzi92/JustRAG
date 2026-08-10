// Package raptor implements RAPTOR (Recursive Abstractive Processing
// for Tree-Organized Retrieval) on top of the existing ingest
// pipeline.
//
// The builder grows a per-file summary tree at ingest: cluster leaves
// with K-means, summarise each cluster with a fast-tier LLM, embed
// the summary as a regular chunk row tagged node_kind='summary',
// repeat until one root. Leaves and summaries sit in the same
// dim-keyed chunk table so the unchanged BM25 + vector + reranker
// pipeline scores them identically ("collapsed-tree retrieval").
//
// See docs/retrieval.md → "RAPTOR hierarchical indexing".
package raptor

import (
	"fmt"
	"math/rand/v2"
)

// ClusterInput is one point fed to K-means: an id (so the caller can
// match results back to the original row) and its embedding.
type ClusterInput struct {
	ID        string
	Embedding []float64
}

// Cluster is one K-means partition: the centroid and the members
// assigned to it.
type Cluster struct {
	Centroid []float64
	Members  []ClusterInput
}

// ClusterByKMeans groups `points` into `k` clusters with Lloyd's
// algorithm seeded by k-means++. `seed` makes initialisation
// reproducible across runs (the builder hashes kb_id + file_id +
// level so re-ingest produces the same tree). Returns an error on
// empty input or non-positive k.
//
// Distance is squared-Euclidean. The production embedder emits
// unit-normalised vectors, so squared-Euclidean ranks points
// identically to cosine distance under unit-norm — and avoids the
// per-iteration sqrt + the cosine numerator/denominator dance.
//
// k >= len(points) returns one cluster per point; the empty-cluster
// path re-seeds with a random point so the result always has k or
// fewer non-empty clusters. Convergence is capped at 30 iterations
// (typical files converge in 4-8; the cap defends against
// pathologically flat embedding clouds).
func ClusterByKMeans(points []ClusterInput, k int, seed int64) ([]Cluster, error) {
	if len(points) == 0 {
		return nil, fmt.Errorf("kmeans: empty input")
	}
	if k <= 0 {
		return nil, fmt.Errorf("kmeans: k must be > 0")
	}
	if k >= len(points) {
		out := make([]Cluster, len(points))
		for i, p := range points {
			out[i] = Cluster{
				Centroid: append([]float64(nil), p.Embedding...),
				Members:  []ClusterInput{p},
			}
		}
		return out, nil
	}

	dim := len(points[0].Embedding)
	rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed>>32)))

	centroids := initKMeansPlusPlus(points, k, rng)

	assignments := make([]int, len(points))
	// Hoisted out of the Lloyd loop below: at k×dim float64s these are the
	// largest allocations in the routine (a 4096-dim embedding set with k=8
	// is ~256 KiB per iteration), and up to 30 iterations run. Zeroed per
	// iteration instead — same cost as a fresh make, no GC pressure.
	sums := make([][]float64, k)
	for i := range sums {
		sums[i] = make([]float64, dim)
	}
	counts := make([]int, k)

	const maxIter = 30
	for iter := 0; iter < maxIter; iter++ {
		changed := false
		for i, p := range points {
			best, _ := nearestCentroid(p.Embedding, centroids)
			if assignments[i] != best {
				changed = true
				assignments[i] = best
			}
		}

		clear(counts)
		for i := range sums {
			clear(sums[i])
		}
		for i, p := range points {
			a := assignments[i]
			for d := 0; d < dim; d++ {
				sums[a][d] += p.Embedding[d]
			}
			counts[a]++
		}
		for i := range centroids {
			if counts[i] == 0 {
				// Re-seed empty clusters with a random point so the
				// next iteration has a fighting chance. Without this
				// a cluster can vanish forever once it loses every
				// member to a neighbour.
				centroids[i] = append([]float64(nil), points[rng.IntN(len(points))].Embedding...)
				continue
			}
			inv := 1.0 / float64(counts[i])
			for d := 0; d < dim; d++ {
				centroids[i][d] = sums[i][d] * inv
			}
		}
		if !changed {
			break
		}
	}

	out := make([]Cluster, k)
	for i := range out {
		out[i].Centroid = centroids[i]
	}
	for i, p := range points {
		a := assignments[i]
		out[a].Members = append(out[a].Members, p)
	}

	// Drop empty clusters in the unlikely event one survived re-seeding.
	pruned := out[:0]
	for _, c := range out {
		if len(c.Members) > 0 {
			pruned = append(pruned, c)
		}
	}
	return pruned, nil
}

// initKMeansPlusPlus picks k initial centroids using the k-means++
// scheme: first centroid uniform-at-random, each subsequent centroid
// picked with probability proportional to its squared distance from
// the nearest already-chosen centroid. Materially better convergence
// than uniform initialisation on real-world data.
func initKMeansPlusPlus(points []ClusterInput, k int, rng *rand.Rand) [][]float64 {
	centroids := make([][]float64, 0, k)
	centroids = append(centroids, append([]float64(nil), points[rng.IntN(len(points))].Embedding...))
	for len(centroids) < k {
		dists := make([]float64, len(points))
		var total float64
		for j, p := range points {
			d := nearestDistSq(p.Embedding, centroids)
			dists[j] = d
			total += d
		}
		if total == 0 {
			// Degenerate case — every remaining point sits on top of
			// an existing centroid. Pick any point as a tie-break.
			centroids = append(centroids, append([]float64(nil), points[rng.IntN(len(points))].Embedding...))
			continue
		}
		target := rng.Float64() * total
		var acc float64
		pick := len(points) - 1
		for j, d := range dists {
			acc += d
			if acc >= target {
				pick = j
				break
			}
		}
		centroids = append(centroids, append([]float64(nil), points[pick].Embedding...))
	}
	return centroids
}

func nearestDistSq(p []float64, centroids [][]float64) float64 {
	best := -1.0
	for _, c := range centroids {
		d := sqEuclidean(p, c)
		if best < 0 || d < best {
			best = d
		}
	}
	if best < 0 {
		return 0
	}
	return best
}

func nearestCentroid(p []float64, centroids [][]float64) (int, float64) {
	best := 0
	bestD := sqEuclidean(p, centroids[0])
	for i := 1; i < len(centroids); i++ {
		d := sqEuclidean(p, centroids[i])
		if d < bestD {
			bestD = d
			best = i
		}
	}
	return best, bestD
}

func sqEuclidean(a, b []float64) float64 {
	var sum float64
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}
