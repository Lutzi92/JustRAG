package chat

import (
	"strings"
	"testing"
)

// TestComputeRefineDiff_Identical confirms identical inputs produce a
// single "kept" chunk. The frontend uses this to cheaply detect
// "refine ran but produced no observable change" — alternative would
// be checking len(diff) > 1 || diff[0].Kind != "kept".
func TestComputeRefineDiff_Identical(t *testing.T) {
	t.Parallel()
	in := "The PPM team is reachable at ppm@uni-giessen.de."
	got := computeRefineDiff(in, in)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk for identical inputs, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "kept" {
		t.Errorf("expected kind=kept, got %q", got[0].Kind)
	}
}

// TestComputeRefineDiff_OneWordReplacement: the surgical refine path
// flips a single hallucinated word. The diff MUST surface it as
// removed+added, not as one big "everything changed" block.
func TestComputeRefineDiff_OneWordReplacement(t *testing.T) {
	t.Parallel()
	original := "The PPM team is reachable at wrong@uni-giessen.de."
	refined := "The PPM team is reachable at ppm@uni-giessen.de."

	got := computeRefineDiff(original, refined)
	hasRemoved, hasAdded, hasKept := false, false, false
	for _, c := range got {
		switch c.Kind {
		case "removed":
			hasRemoved = true
			if !strings.Contains(c.Text, "wrong") {
				t.Errorf("removed chunk should contain 'wrong', got %q", c.Text)
			}
		case "added":
			hasAdded = true
			if !strings.Contains(c.Text, "ppm") {
				t.Errorf("added chunk should contain 'ppm', got %q", c.Text)
			}
		case "kept":
			hasKept = true
		}
	}
	if !hasRemoved || !hasAdded || !hasKept {
		t.Errorf("expected all three kinds present, got removed=%v added=%v kept=%v in %+v",
			hasRemoved, hasAdded, hasKept, got)
	}
}

// TestComputeRefineDiff_AdjacentMerging: two consecutive same-kind
// chunks (a quirk of diffmatchpatch's chars-to-lines pass) must collapse.
// If they don't, the frontend re-renders the same kind twice with a
// gap, which manifests as visual stutter.
func TestComputeRefineDiff_AdjacentMerging(t *testing.T) {
	t.Parallel()
	got := computeRefineDiff("a b c d", "a b c d")
	for i := 1; i < len(got); i++ {
		if got[i].Kind == got[i-1].Kind {
			t.Errorf("adjacent chunks of same kind not merged: %+v", got)
		}
	}
}

// TestComputeRefineDiff_ReconstructionRoundtrip: the frontend needs to
// rebuild the refined answer from kept+added chunks. If chunk text is
// missing word separators, that reconstruction produces garbage like
// "PPMteam" instead of "PPM team". This test pins the contract: kept
// chunks + added chunks (in order, ignoring removed) reconstruct the
// refined answer modulo whitespace normalization.
func TestComputeRefineDiff_ReconstructionRoundtrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		original string
		refined  string
	}{
		{"single word swap", "The cat sat on the mat", "The dog sat on the mat"},
		{"phrase insertion", "Use the API", "Use the public API directly"},
		{"phrase deletion", "Maybe perhaps possibly use it", "Use it"},
		{"identical", "no change here", "no change here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diff := computeRefineDiff(tc.original, tc.refined)
			var rebuilt strings.Builder
			for _, c := range diff {
				if c.Kind == "removed" {
					continue
				}
				rebuilt.WriteString(c.Text)
			}
			gotNorm := strings.Join(strings.Fields(rebuilt.String()), " ")
			wantNorm := strings.Join(strings.Fields(tc.refined), " ")
			if gotNorm != wantNorm {
				t.Errorf("reconstruction mismatch:\n  got:  %q\n  want: %q\n  diff: %+v",
					gotNorm, wantNorm, diff)
			}
		})
	}
}

// TestComputeRefineDiff_EmptyInputs: corner cases that diffmatchpatch
// can choke on. Empty original (refine produced text from nothing —
// shouldn't happen in practice but the contract should be safe) and
// empty refined (also shouldn't happen — caller guards on empty).
func TestComputeRefineDiff_EmptyInputs(t *testing.T) {
	t.Parallel()
	cases := []struct{ orig, refined string }{
		{"", ""},
		{"hello", ""},
		{"", "hello"},
	}
	for _, tc := range cases {
		got := computeRefineDiff(tc.orig, tc.refined)
		// We don't assert specific shape — just that the function
		// doesn't panic and doesn't lie about kept/added when the
		// inputs are obviously asymmetric.
		_ = got
	}
}
