package vector

import (
	"slices"
	"testing"
)

func TestIntersectFileIDs(t *testing.T) {
	// Empty caller list → return the date-resolved list unchanged.
	got := intersectFileIDs(nil, []string{"a", "b"})
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("nil ∩ = %v", got)
	}

	// Overlap → only common IDs, in caller order.
	got = intersectFileIDs([]string{"b", "a", "c"}, []string{"a", "b"})
	if !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("intersection = %v, want [b a]", got)
	}

	// Disjoint → empty (caller should short-circuit to no results).
	got = intersectFileIDs([]string{"x"}, []string{"a", "b"})
	if len(got) != 0 {
		t.Errorf("disjoint = %v, want empty", got)
	}
}
