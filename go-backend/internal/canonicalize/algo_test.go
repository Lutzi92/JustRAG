package canonicalize

import (
	"math"
	"reflect"
	"sort"
	"testing"
)

func TestCosine(t *testing.T) {
	if g := cosine([]float64{1, 0}, []float64{1, 0}); math.Abs(g-1) > 1e-9 {
		t.Errorf("identical cosine = %v want 1", g)
	}
	if g := cosine([]float64{1, 0}, []float64{0, 1}); math.Abs(g) > 1e-9 {
		t.Errorf("orthogonal cosine = %v want 0", g)
	}
	if g := cosine([]float64{0, 0}, []float64{1, 0}); g != 0 {
		t.Errorf("zero vector cosine = %v want 0", g)
	}
}

func TestCandidatePairs_SameTypeThresholdAndCap(t *testing.T) {
	ents := []CanonEntity{
		{ID: 1, Name: "A", Type: "concept"},
		{ID: 2, Name: "A2", Type: "concept"},
		{ID: 3, Name: "B", Type: "person"},
	}
	embs := [][]float64{{1, 0}, {0.99, 0.01}, {1, 0}}
	pairs := candidatePairs(ents, embs, 0.9, 10)
	if len(pairs) != 1 || !(pairs[0].I == 0 && pairs[0].J == 1) {
		t.Fatalf("want one same-type pair (0,1), got %+v", pairs)
	}
	capped := candidatePairs(ents, embs, 0.0, 1)
	if len(capped) != 1 {
		t.Errorf("maxPairs=1 must cap to 1, got %d", len(capped))
	}
}

func TestCluster_Transitive(t *testing.T) {
	groups := cluster([]Pair{{I: 0, J: 1}, {I: 1, J: 2}}, 4)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d: %+v", len(groups), groups)
	}
	g := append([]int(nil), groups[0]...)
	sort.Ints(g)
	if !reflect.DeepEqual(g, []int{0, 1, 2}) {
		t.Errorf("want {0,1,2}, got %v", g)
	}
}

func TestPickSurvivor_DegreeThenNameThenID(t *testing.T) {
	group := []CanonEntity{
		{ID: 5, Name: "PPM", Type: "org", Aliases: []string{"ppm"}, Degree: 2},
		{ID: 9, Name: "PPM-Team", Type: "org", Aliases: []string{"team"}, Degree: 7},
		{ID: 3, Name: "Projektmanagement", Type: "org", Degree: 2},
	}
	survID, merged, aliases := pickSurvivor(group)
	if survID != 9 {
		t.Errorf("survivor by degree should be 9, got %d", survID)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i] < merged[j] })
	if !reflect.DeepEqual(merged, []int64{3, 5}) {
		t.Errorf("merged should be {3,5}, got %v", merged)
	}
	want := map[string]bool{"PPM": true, "ppm": true, "PPM-Team": true, "team": true, "Projektmanagement": true}
	if len(aliases) != len(want) {
		t.Errorf("aliasUnion = %v, want keys %v", aliases, want)
	}
	for _, a := range aliases {
		if !want[a] {
			t.Errorf("unexpected alias %q", a)
		}
	}
}
