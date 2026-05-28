package raptor

import (
	"testing"
)

// TestClusterByLeiden_EmptyInputErrors: same contract as
// ClusterByKMeans — caller must distinguish "no points" from
// "no signal". Empty input is the former.
func TestClusterByLeiden_EmptyInputErrors(t *testing.T) {
	t.Parallel()
	if _, err := ClusterByLeiden(nil, LeidenConfig{}); err == nil {
		t.Error("empty input: expected error, got nil")
	}
}

// TestClusterByLeiden_SinglePointReturnsOneCluster: trivial input
// must work, mirroring the K-means contract. Without this the
// RAPTOR builder would crash on a single-leaf file at the
// degenerate top level.
func TestClusterByLeiden_SinglePointReturnsOneCluster(t *testing.T) {
	t.Parallel()
	points := []ClusterInput{{ID: "a", Embedding: []float64{1, 0, 0}}}
	clusters, err := ClusterByLeiden(points, LeidenConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusters) != 1 || len(clusters[0].Members) != 1 {
		t.Errorf("single point: want 1 cluster of 1 member, got %+v", clusters)
	}
}

// TestClusterByLeiden_TwoFarApartClustersSplit: a clear cluster
// structure (two tight groups separated by a clear gap) must
// produce two communities, regardless of the BPE-style internal
// sort order. Documents the core "adaptive" claim: K-means with
// k=4 here would split a tight group; Leiden discovers the right
// count from topology.
func TestClusterByLeiden_TwoFarApartClustersSplit(t *testing.T) {
	t.Parallel()
	// Two unit-norm clusters: one near (1, 0), one near (0, 1).
	// Five points each with tiny noise so cosine sim within a
	// group stays near 1 and across groups near 0.
	points := []ClusterInput{
		{ID: "a1", Embedding: []float64{0.99, 0.10}},
		{ID: "a2", Embedding: []float64{0.98, 0.12}},
		{ID: "a3", Embedding: []float64{0.97, 0.11}},
		{ID: "a4", Embedding: []float64{0.99, 0.09}},
		{ID: "a5", Embedding: []float64{0.98, 0.10}},
		{ID: "b1", Embedding: []float64{0.10, 0.99}},
		{ID: "b2", Embedding: []float64{0.11, 0.98}},
		{ID: "b3", Embedding: []float64{0.12, 0.97}},
		{ID: "b4", Embedding: []float64{0.09, 0.99}},
		{ID: "b5", Embedding: []float64{0.10, 0.98}},
	}
	clusters, err := ClusterByLeiden(points, LeidenConfig{KNeighbors: 4, Seed: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusters) != 2 {
		t.Fatalf("two-cluster topology: want 2 communities, got %d (%+v)", len(clusters), clusterIDs(clusters))
	}
	// Each cluster must be pure (all-a or all-b). A mixed cluster
	// would mean modularity didn't separate the gap.
	for _, c := range clusters {
		prefix := c.Members[0].ID[:1]
		for _, m := range c.Members[1:] {
			if m.ID[:1] != prefix {
				t.Errorf("mixed cluster (a/b): %v", c.Members)
			}
		}
	}
}

// TestClusterByLeiden_CentroidIsMemberMean: the centroid must equal
// the per-component mean of its members' embeddings. Pinned because
// downstream code (the builder's k=ceil(N/branch) iteration math)
// indirectly assumes the centroid is in the same vector space; any
// regression to "centroid = first member" would silently break
// follow-up clustering at higher RAPTOR levels.
func TestClusterByLeiden_CentroidIsMemberMean(t *testing.T) {
	t.Parallel()
	points := []ClusterInput{
		{ID: "a", Embedding: []float64{1.0, 0.0}},
		{ID: "b", Embedding: []float64{0.0, 1.0}},
	}
	clusters, err := ClusterByLeiden(points, LeidenConfig{KNeighbors: 1, Seed: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Whether they collapse to 1 cluster or stay as 2 depends on
	// the k-NN graph density; either way the centroids must be
	// the mean of their members.
	for _, c := range clusters {
		want := make([]float64, len(c.Members[0].Embedding))
		for _, m := range c.Members {
			for d, v := range m.Embedding {
				want[d] += v
			}
		}
		inv := 1.0 / float64(len(c.Members))
		for d := range want {
			want[d] *= inv
		}
		for d := range want {
			if abs(want[d]-c.Centroid[d]) > 1e-9 {
				t.Errorf("centroid[%d] = %v, want %v (members=%v)", d, c.Centroid[d], want[d], c.Members)
			}
		}
	}
}

// TestLeidenConfig_Sanitize_FillsDefaults pins the operational
// contract: zero / out-of-range fields fall through to the
// documented defaults. Without this any caller that hands a
// zero-valued Config would get a no-op clustering pass.
func TestLeidenConfig_Sanitize_FillsDefaults(t *testing.T) {
	t.Parallel()
	c := LeidenConfig{}.Sanitize()
	if c.KNeighbors != 10 {
		t.Errorf("default KNeighbors: want 10, got %d", c.KNeighbors)
	}
	if c.Resolution != 1.0 {
		t.Errorf("default Resolution: want 1.0, got %v", c.Resolution)
	}
	if c.MaxIter != 20 {
		t.Errorf("default MaxIter: want 20, got %d", c.MaxIter)
	}

	// Valid values must pass through.
	c2 := LeidenConfig{KNeighbors: 5, Resolution: 1.5, MaxIter: 50}.Sanitize()
	if c2.KNeighbors != 5 || c2.Resolution != 1.5 || c2.MaxIter != 50 {
		t.Errorf("valid values must pass through: got %+v", c2)
	}
}

// TestClusterByLeiden_DeterministicWithSeed: same Seed → same
// partition. Pinned so a re-ingest of an unchanged file produces
// a byte-identical RAPTOR tree, which the upstream debugging note
// ("Determinism: levelSeed hashes kbID + fileID + level …") relies
// on.
func TestClusterByLeiden_DeterministicWithSeed(t *testing.T) {
	t.Parallel()
	points := []ClusterInput{
		{ID: "p1", Embedding: []float64{0.99, 0.10}},
		{ID: "p2", Embedding: []float64{0.98, 0.12}},
		{ID: "p3", Embedding: []float64{0.10, 0.99}},
		{ID: "p4", Embedding: []float64{0.11, 0.98}},
		{ID: "p5", Embedding: []float64{0.50, 0.50}},
	}
	a, err := ClusterByLeiden(points, LeidenConfig{Seed: 1234, KNeighbors: 3})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ClusterByLeiden(points, LeidenConfig{Seed: 1234, KNeighbors: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("non-deterministic cluster count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		idsA := clusterIDs([]Cluster{a[i]})[0]
		idsB := clusterIDs([]Cluster{b[i]})[0]
		if idsA != idsB {
			t.Errorf("cluster %d membership drift: %v vs %v", i, idsA, idsB)
		}
	}
}

// clusterIDs returns the sorted-by-ID member set per cluster as
// a flat string for easy equality comparison. The
// materialiseClusters output already orders members by insertion;
// here we re-sort by ID so two runs that produce the same set in
// different insertion orders compare equal.
func clusterIDs(clusters []Cluster) []string {
	out := make([]string, len(clusters))
	for i, c := range clusters {
		ids := make([]string, 0, len(c.Members))
		for _, m := range c.Members {
			ids = append(ids, m.ID)
		}
		// Insertion-sort: cheap for tiny slices.
		for k := 1; k < len(ids); k++ {
			for j := k; j > 0 && ids[j-1] > ids[j]; j-- {
				ids[j-1], ids[j] = ids[j], ids[j-1]
			}
		}
		var b []byte
		for j, id := range ids {
			if j > 0 {
				b = append(b, ',')
			}
			b = append(b, id...)
		}
		out[i] = string(b)
	}
	return out
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
