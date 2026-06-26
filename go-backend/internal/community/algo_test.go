package community

import (
	"sort"
	"strings"
	"testing"
)

func TestDetect_TwoCliques(t *testing.T) {
	edges := []Edge{
		{1, 2, 1}, {2, 3, 1}, {1, 3, 1},
		{4, 5, 1}, {5, 6, 1}, {4, 6, 1},
		{3, 4, 0.1},
	}
	comms := Detect(edges, Config{Resolution: 1.0, MinSize: 3})
	if len(comms) != 2 {
		t.Fatalf("want 2 communities, got %d: %+v", len(comms), comms)
	}
	for _, c := range comms {
		sort.Slice(c.Members, func(i, j int) bool { return c.Members[i] < c.Members[j] })
	}
}

func TestDetect_MinSizeFilter(t *testing.T) {
	comms := Detect([]Edge{{1, 2, 1}}, Config{Resolution: 1.0, MinSize: 3})
	if len(comms) != 0 {
		t.Errorf("2-node community should be dropped by MinSize=3, got %+v", comms)
	}
}

func TestBuildSummaryInput(t *testing.T) {
	members := []Member{{ID: 1, Name: "PPM", Type: "org"}, {ID: 2, Name: "Berlin", Type: "location"}}
	edges := []InternalEdge{{Src: 1, Dst: 2, Rel: "based_in", Evidence: "PPM is based in Berlin"}}
	got := BuildSummaryInput(members, edges, 10000)
	if !strings.Contains(got, "PPM") || !strings.Contains(got, "Berlin") || !strings.Contains(got, "based_in") {
		t.Errorf("summary input missing members/edges: %q", got)
	}
	short := BuildSummaryInput(members, edges, 5)
	if len([]rune(short)) > 5 {
		t.Errorf("cap not applied: %d runes", len([]rune(short)))
	}
}
