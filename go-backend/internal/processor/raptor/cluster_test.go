package raptor

import (
	"hash/fnv"
	"testing"
)

// hashSeedForTest mirrors levelSeed in builder.go: combines string
// parts via FNV-64a so the seed is deterministic across runs.
func hashSeedForTest(parts ...string) int64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return int64(h.Sum64())
}

func TestClusterByKMeans_GroupsObviousClusters(t *testing.T) {
	// Two well-separated point clouds in 4D.
	rows := []ClusterInput{
		{ID: "a1", Embedding: []float64{1, 1, 1, 1}},
		{ID: "a2", Embedding: []float64{1.1, 1.0, 0.9, 1.05}},
		{ID: "a3", Embedding: []float64{0.95, 1.05, 1.1, 1}},
		{ID: "b1", Embedding: []float64{-5, -5, -5, -5}},
		{ID: "b2", Embedding: []float64{-4.9, -5.1, -5.0, -4.95}},
		{ID: "b3", Embedding: []float64{-5.05, -4.95, -5.1, -5}},
	}
	seed := hashSeedForTest("kb", "file", "1")
	clusters, err := ClusterByKMeans(rows, 2, seed)
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	if len(clusters) != 2 {
		t.Fatalf("want 2 clusters got %d", len(clusters))
	}
	for _, c := range clusters {
		prefix := c.Members[0].ID[0]
		for _, m := range c.Members {
			if m.ID[0] != prefix {
				t.Errorf("cluster has mixed prefixes %v", c.Members)
			}
		}
	}
}

func TestClusterByKMeans_DeterministicWithSeed(t *testing.T) {
	rows := []ClusterInput{
		{ID: "a", Embedding: []float64{0.1, 0.2}},
		{ID: "b", Embedding: []float64{0.9, 0.8}},
		{ID: "c", Embedding: []float64{0.15, 0.25}},
		{ID: "d", Embedding: []float64{0.85, 0.75}},
	}
	c1, _ := ClusterByKMeans(rows, 2, 42)
	c2, _ := ClusterByKMeans(rows, 2, 42)
	if !sameClustering(c1, c2) {
		t.Errorf("clustering not deterministic")
	}
}

func TestClusterByKMeans_KGreaterOrEqualN(t *testing.T) {
	rows := []ClusterInput{
		{ID: "x", Embedding: []float64{1, 2, 3}},
		{ID: "y", Embedding: []float64{4, 5, 6}},
	}
	clusters, err := ClusterByKMeans(rows, 3, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(clusters) != 2 {
		t.Fatalf("k>=n should produce one cluster per point; got %d", len(clusters))
	}
}

func TestClusterByKMeans_EmptyInput(t *testing.T) {
	if _, err := ClusterByKMeans(nil, 2, 0); err == nil {
		t.Fatal("expected error on empty input")
	}
}

func TestClusterByKMeans_NonPositiveK(t *testing.T) {
	rows := []ClusterInput{{ID: "a", Embedding: []float64{1}}}
	if _, err := ClusterByKMeans(rows, 0, 0); err == nil {
		t.Fatal("expected error on k=0")
	}
}

func sameClustering(a, b []Cluster) bool {
	if len(a) != len(b) {
		return false
	}
	mapOf := func(cs []Cluster) map[string]int {
		m := map[string]int{}
		for i, c := range cs {
			for _, mem := range c.Members {
				m[mem.ID] = i
			}
		}
		return m
	}
	return clusterAssignmentsEqual(mapOf(a), mapOf(b))
}

// clusterAssignmentsEqual reports whether two id→cluster-label maps
// describe the same partition. Labels may permute, so we build a
// canonical mapping label-of-a → label-of-b and verify it's consistent.
func clusterAssignmentsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	mapping := map[int]int{}
	for id, ai := range a {
		bi, ok := b[id]
		if !ok {
			return false
		}
		if existing, seen := mapping[ai]; seen {
			if existing != bi {
				return false
			}
		} else {
			mapping[ai] = bi
		}
	}
	return true
}
