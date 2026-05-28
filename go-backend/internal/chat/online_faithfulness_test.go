package chat

import (
	"testing"

	"github.com/justrag/go-backend/internal/ai"
)

// TestComputeFaithfulnessScore_CleanIsOne: no flags, no ISUSE
// failure → 1.0. The "happy path" is the most-common case in
// production (>80% on a clean baseline); the score must be
// EXACTLY 1.0 so dashboards can use 1.0 as the visual ceiling.
func TestComputeFaithfulnessScore_CleanIsOne(t *testing.T) {
	t.Parallel()
	if got := computeFaithfulnessScore(nil, nil); got != 1.0 {
		t.Errorf("clean (no flags, no Self-RAG): got %v, want 1.0", got)
	}
	// Self-RAG ran but reported all-clean.
	clean := &ai.SelfRAGResult{
		Usefulness: ai.UsefulnessVerdict{Verdict: "yes"},
	}
	if got := computeFaithfulnessScore(nil, clean); got != 1.0 {
		t.Errorf("clean Self-RAG: got %v, want 1.0", got)
	}
}

// TestComputeFaithfulnessScore_OutOfScopeDoesNotPenalise: the
// out_of_scope claim type is a policy class the operator opted
// into (general knowledge allowed). Flagging it would penalise
// the answer for following operator policy. Pin behaviour.
func TestComputeFaithfulnessScore_OutOfScopeDoesNotPenalise(t *testing.T) {
	t.Parallel()
	flags := []ai.FlaggedClaim{
		{ClaimText: "x", Reason: "out_of_scope"},
		{ClaimText: "y", Reason: "out_of_scope"},
	}
	if got := computeFaithfulnessScore(flags, nil); got != 1.0 {
		t.Errorf("only out_of_scope flags: got %v, want 1.0 (no penalty)", got)
	}
}

// TestComputeFaithfulnessScore_FlagPenalty: each refine-eligible
// flag drops the score by 0.2. Five flags → 0. More flags also
// stay at 0 (clamped).
func TestComputeFaithfulnessScore_FlagPenalty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		flags []ai.FlaggedClaim
		want  float64
	}{
		{nil, 1.0},
		{[]ai.FlaggedClaim{{ClaimText: "a", Reason: "unsupported"}}, 0.8},
		{[]ai.FlaggedClaim{
			{ClaimText: "a", Reason: "unsupported"},
			{ClaimText: "b", Reason: "contradicted"},
		}, 0.6},
		{[]ai.FlaggedClaim{
			{ClaimText: "a", Reason: "unsupported"},
			{ClaimText: "b", Reason: "unsupported"},
			{ClaimText: "c", Reason: "unsupported"},
			{ClaimText: "d", Reason: "unsupported"},
			{ClaimText: "e", Reason: "unsupported"},
		}, 0.0},
		{[]ai.FlaggedClaim{ // mix of penalising + non-penalising
			{ClaimText: "a", Reason: "unsupported"},
			{ClaimText: "b", Reason: "out_of_scope"}, // ignored
			{ClaimText: "c", Reason: "contradicted"},
		}, 0.6},
	}
	for i, tc := range cases {
		got := computeFaithfulnessScore(tc.flags, nil)
		// float comparison with 1e-9 tolerance.
		diff := got - tc.want
		if diff < -1e-9 || diff > 1e-9 {
			t.Errorf("case %d: got %v, want %v", i, got, tc.want)
		}
	}
}

// TestComputeFaithfulnessScore_ISUSEPenalty: ISUSE = "no"
// subtracts 0.5; "partial" subtracts 0.2; "yes" / unknown
// don't penalise.
func TestComputeFaithfulnessScore_ISUSEPenalty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		verdict string
		want    float64
	}{
		{"yes", 1.0},
		{"partial", 0.8},
		{"no", 0.5},
		{"", 1.0},      // unknown / unset — treat as yes
		{"weird", 1.0}, // non-enum — treat as yes
	}
	for _, tc := range cases {
		sr := &ai.SelfRAGResult{Usefulness: ai.UsefulnessVerdict{Verdict: tc.verdict}}
		got := computeFaithfulnessScore(nil, sr)
		diff := got - tc.want
		if diff < -1e-9 || diff > 1e-9 {
			t.Errorf("verdict=%q: got %v, want %v", tc.verdict, got, tc.want)
		}
	}
}

// TestComputeFaithfulnessScore_BothAxesCompose: a turn with both
// refine-eligible flags AND a Self-RAG ISUSE failure subtracts
// from both axes. Clamped at 0.
func TestComputeFaithfulnessScore_BothAxesCompose(t *testing.T) {
	t.Parallel()
	flags := []ai.FlaggedClaim{
		{ClaimText: "a", Reason: "unsupported"},
		{ClaimText: "b", Reason: "contradicted"},
	}
	// 1.0 - 0.4 (2 flags) - 0.5 (ISUSE=no) = 0.1
	sr := &ai.SelfRAGResult{Usefulness: ai.UsefulnessVerdict{Verdict: "no"}}
	got := computeFaithfulnessScore(flags, sr)
	want := 0.1
	diff := got - want
	if diff < -1e-9 || diff > 1e-9 {
		t.Errorf("composed: got %v, want %v", got, want)
	}

	// Clamp test: 5 flags + ISUSE=no should pin at 0, not negative.
	manyFlags := []ai.FlaggedClaim{
		{ClaimText: "1", Reason: "unsupported"},
		{ClaimText: "2", Reason: "unsupported"},
		{ClaimText: "3", Reason: "unsupported"},
		{ClaimText: "4", Reason: "unsupported"},
		{ClaimText: "5", Reason: "unsupported"},
	}
	if got := computeFaithfulnessScore(manyFlags, sr); got != 0.0 {
		t.Errorf("worst case clamp: got %v, want 0.0", got)
	}
}
