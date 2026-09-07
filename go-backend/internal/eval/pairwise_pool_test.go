package eval

import (
	"math"
	"testing"
)

// Pooling two pairwise results (ruling W5-R1).

// TestPoolPairwise_TwoCrossPairs pins the arithmetic W5-R1 decides on: the
// long-context rule is stated on the POOLED decisive pairs of two cross
// comparisons (flat1 vs mr1, flat2 vs mr2), not on either pair alone.
//
// Shapes are the Wave-4 ones, expressed from A's (= flat's) view:
//
//	pw1: A 3 wins / 1 tie / 8 losses
//	pw2: A 1 win  / 3 ties / 8 losses
//
// Pooled: 4 wins / 4 ties / 16 losses → 20 DECISIVE pairs (ties excluded,
// per W4-R4), so B (= map_reduce) wins 16 of 20 = 0.80 with a 95 % Wilson
// interval of [0.584, 0.919] — above the 0.50 lower bound W5-R1 requires.
//
// The tie exclusion is the load-bearing part: pooling the 4 ties into the
// denominator would give 16/24 = 0.667 with a visibly different interval,
// which is why the interval is asserted and not only the rate.
func TestPoolPairwise_TwoCrossPairs(t *testing.T) {
	pw1 := &PairwiseResult{Wins: 3, Ties: 1, Losses: 8}
	pw2 := &PairwiseResult{Wins: 1, Ties: 3, Losses: 8}

	pooled := PoolPairwise(pw1, pw2)

	if pooled.Wins != 4 || pooled.Ties != 4 || pooled.Losses != 16 {
		t.Fatalf("pooled counts = %d/%d/%d, want 4/4/16", pooled.Wins, pooled.Ties, pooled.Losses)
	}
	if math.Abs(pooled.WinRate-0.2) > 1e-9 {
		t.Errorf("pooled WinRate (A's view) = %.6f, want 0.2", pooled.WinRate)
	}
	if math.Abs(pooled.TieRate-4.0/24.0) > 1e-9 {
		t.Errorf("pooled TieRate = %.6f, want %.6f", pooled.TieRate, 4.0/24.0)
	}

	// B's view is the one W5-R1 is stated in: B's wins are the pooled losses.
	decided := pooled.Wins + pooled.Losses
	if decided != 20 {
		t.Fatalf("decisive pairs = %d, want 20 (ties excluded)", decided)
	}
	bWinRate := float64(pooled.Losses) / float64(decided)
	if math.Abs(bWinRate-0.8) > 1e-9 {
		t.Errorf("B win rate = %.6f, want 0.8", bWinRate)
	}
	bLow, bHigh := WilsonInterval(pooled.Losses, decided, wilsonZ)
	if math.Abs(bLow-0.583980) > 5e-4 {
		t.Errorf("B WilsonLow = %.6f, want ~0.584", bLow)
	}
	if math.Abs(bHigh-0.919344) > 5e-4 {
		t.Errorf("B WilsonHigh = %.6f, want ~0.919", bHigh)
	}

	// A's own interval must be the mirror image of B's, which only holds
	// when the denominator excludes ties on both sides.
	if math.Abs(pooled.WilsonLow-(1-bHigh)) > 1e-9 || math.Abs(pooled.WilsonHigh-(1-bLow)) > 1e-9 {
		t.Errorf("A interval [%.6f, %.6f] is not the mirror of B's [%.6f, %.6f]",
			pooled.WilsonLow, pooled.WilsonHigh, bLow, bHigh)
	}
}

// TestPoolPairwise_ByRoute pools the per-route tables so a route present in
// only one of the two runs still shows up (with that run's counts) instead
// of being silently dropped.
func TestPoolPairwise_ByRoute(t *testing.T) {
	pw1 := &PairwiseResult{ByRoute: map[string]PairwiseCounts{
		"global_synthesis": {Wins: 3, Ties: 1, Losses: 8},
		"lookup":           {Wins: 1, Losses: 1},
	}}
	pw2 := &PairwiseResult{ByRoute: map[string]PairwiseCounts{
		"global_synthesis": {Wins: 1, Ties: 3, Losses: 8},
	}}

	byRoute := PoolPairwiseByRoute(pw1, pw2)
	if len(byRoute) != 2 {
		t.Fatalf("routes = %v, want global_synthesis + lookup", byRoute)
	}
	gs := byRoute["global_synthesis"]
	if gs.Wins != 4 || gs.Ties != 4 || gs.Losses != 16 {
		t.Errorf("global_synthesis = %d/%d/%d, want 4/4/16", gs.Wins, gs.Ties, gs.Losses)
	}
	if math.Abs(gs.WinRate-0.2) > 1e-9 {
		t.Errorf("global_synthesis WinRate = %.6f, want 0.2", gs.WinRate)
	}
	lk := byRoute["lookup"]
	if lk.Wins != 1 || lk.Losses != 1 || math.Abs(lk.WinRate-0.5) > 1e-9 {
		t.Errorf("lookup = %d/%d/%d rate %.3f, want 1/0/1 rate 0.5", lk.Wins, lk.Ties, lk.Losses, lk.WinRate)
	}
}

// TestPoolPairwise_Degenerate: no input, a nil member and an all-ties input
// must not invent a win rate. finalizeCounts reports [0, 1] for an undecided
// bucket; pooling inherits that rather than dividing by zero.
func TestPoolPairwise_Degenerate(t *testing.T) {
	empty := PoolPairwise()
	if empty.Wins != 0 || empty.WinRate != 0 || empty.WilsonLow != 0 || empty.WilsonHigh != 1 {
		t.Errorf("PoolPairwise() = %+v, want zero counts with the [0,1] interval", empty)
	}
	allTies := PoolPairwise(&PairwiseResult{Ties: 5}, nil)
	if allTies.Ties != 5 || allTies.WinRate != 0 || allTies.WilsonLow != 0 || allTies.WilsonHigh != 1 {
		t.Errorf("all-ties pool = %+v, want 5 ties with the [0,1] interval", allTies)
	}
	if math.Abs(allTies.TieRate-1) > 1e-9 {
		t.Errorf("all-ties TieRate = %.6f, want 1", allTies.TieRate)
	}
}
