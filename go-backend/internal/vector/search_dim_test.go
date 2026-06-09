package vector

import "testing"

func TestShouldWarnDimMismatch(t *testing.T) {
	cases := []struct{ targetRows, otherRows, want bool }{
		{true, true, false},   // healthy
		{false, true, true},   // target empty, corpus elsewhere → warn
		{true, false, false},  // healthy single-dim
		{false, false, false}, // empty KB → no warn
	}
	for i, c := range cases {
		if got := shouldWarnDimMismatch(c.targetRows, c.otherRows); got != c.want {
			t.Fatalf("case %d: got %v want %v", i, got, c.want)
		}
	}
}
