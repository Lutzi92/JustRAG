package ai

import (
	"testing"

	"github.com/justrag/go-backend/internal/prompts"
)

// TestClaimsTriggeringRefine_FilterReason confirms the refine trigger
// filter keeps unsupported + contradicted and drops out_of_scope. The
// guarantee matters because the gate logic in chat/http_send.go relies
// on len(triggering) == 0 to skip refine — surfacing out_of_scope as
// a trigger would refine on policy concerns the operator already
// opted into.
func TestClaimsTriggeringRefine_FilterReason(t *testing.T) {
	t.Parallel()
	in := []FlaggedClaim{
		{ClaimText: "a", Reason: "unsupported"},
		{ClaimText: "b", Reason: "contradicted"},
		{ClaimText: "c", Reason: "out_of_scope"},
		{ClaimText: "d", Reason: "unsupported"},
	}
	got := ClaimsTriggeringRefine(in)
	if len(got) != 3 {
		t.Fatalf("expected 3 triggering claims (2 unsupported + 1 contradicted), got %d", len(got))
	}
	for _, c := range got {
		if c.Reason == "out_of_scope" {
			t.Errorf("out_of_scope must not survive the filter: %+v", c)
		}
	}
}

func TestClaimsTriggeringRefine_EmptyAndNil(t *testing.T) {
	t.Parallel()
	if got := ClaimsTriggeringRefine(nil); len(got) != 0 {
		t.Errorf("nil input should produce empty slice, got %d", len(got))
	}
	if got := ClaimsTriggeringRefine([]FlaggedClaim{}); len(got) != 0 {
		t.Errorf("empty input should produce empty slice, got %d", len(got))
	}
}

// TestSelectRefineMode_ThresholdBoundary documents the surgical/holistic
// switch at exactly 4 claims — the user-confirmed default from AP-A1
// brainstorming. Drift here would silently change the answer-rewrite
// behaviour for borderline cases.
func TestSelectRefineMode_ThresholdBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		count int
		want  prompts.RefineMode
	}{
		{"zero claims falls back to surgical", 0, prompts.RefineModeSurgical},
		{"one claim is surgical", 1, prompts.RefineModeSurgical},
		{"three claims is surgical", 3, prompts.RefineModeSurgical},
		{"four claims switches to holistic", 4, prompts.RefineModeHolistic},
		{"many claims is holistic", 12, prompts.RefineModeHolistic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			claims := make([]FlaggedClaim, tc.count)
			for i := range claims {
				claims[i] = FlaggedClaim{ClaimText: "x", Reason: "unsupported"}
			}
			if got := SelectRefineMode(claims); got != tc.want {
				t.Errorf("count=%d: got %s, want %s", tc.count, got, tc.want)
			}
		})
	}
}
