package eval

// Pooling of pairwise results (ruling W5-R1, 2026-09-06).
//
// A single pairwise comparison of two long-context consumers over a 12- or
// 24-question set decides on a handful of pairs, and the Wilson interval on
// that many pairs straddles 0.5 almost regardless of the outcome. W4-R7
// stated the long-context decision rule PER PAIR, which made a run where
// both cross pairs pointed the same way but neither cleared the bar on its
// own read as "inconclusive" — the two runs together carried the evidence
// that neither carried alone.
//
// W5-R1 re-registers the rule on the POOLED decisive pairs of the two cross
// comparisons (flat1 vs mr1, flat2 vs mr2). Pooling is the sum of the
// wins/ties/losses with the rates and the Wilson interval RECOMPUTED on the
// pooled counts — never an average of the two win rates, which would weight
// a pair with 4 decisive verdicts the same as one with 16.
//
// Ties stay out of the denominator exactly as in RunPairwise (W4-R4): a
// pair the judge flipped on carries no information about which side is
// better, and folding it into the denominator would pull every pooled win
// rate toward 0.5 in proportion to the judge's instability.

// PoolPairwise sums the overall wins/ties/losses of the given results and
// recomputes the win rate, the tie rate and the 95 % Wilson interval on the
// pooled counts. nil members are skipped so a caller can pass an optional
// third comparison without branching.
//
// The returned counts are in the FIRST report's (A's) terms, because that
// is how every PairwiseResult is expressed. The long-context rule is stated
// from B's view (map_reduce), which is the mirror: B's wins are the pooled
// Losses over the same decisive denominator.
func PoolPairwise(results ...*PairwiseResult) PairwiseCounts {
	var pooled PairwiseCounts
	for _, res := range results {
		if res == nil {
			continue
		}
		pooled.Wins += res.Wins
		pooled.Ties += res.Ties
		pooled.Losses += res.Losses
	}
	finalizeCounts(&pooled)
	return pooled
}

// PoolPairwiseByRoute pools the per-route tables the same way. A route that
// appears in only one of the results is kept with that result's counts
// rather than dropped: a golden set whose routes differ between two runs is
// a fact the reader should see, not one the pooling should hide.
func PoolPairwiseByRoute(results ...*PairwiseResult) map[string]PairwiseCounts {
	pooled := make(map[string]PairwiseCounts)
	for _, res := range results {
		if res == nil {
			continue
		}
		for route, c := range res.ByRoute {
			acc := pooled[route]
			acc.Wins += c.Wins
			acc.Ties += c.Ties
			acc.Losses += c.Losses
			pooled[route] = acc
		}
	}
	for route := range pooled {
		acc := pooled[route]
		finalizeCounts(&acc)
		pooled[route] = acc
	}
	return pooled
}

// MirrorCounts returns the same bucket seen from report B's side: B's wins
// are A's losses and vice versa, over the same decisive denominator. The
// pairwise printer uses it so a rule stated in B's terms (W5-R1 is stated
// from map_reduce's view) can be read off directly instead of being
// mentally inverted — an inversion that is easy to get wrong for the Wilson
// bounds, which swap AND reflect (low_B = 1 - high_A).
func MirrorCounts(c PairwiseCounts) PairwiseCounts {
	m := PairwiseCounts{Wins: c.Losses, Ties: c.Ties, Losses: c.Wins}
	finalizeCounts(&m)
	return m
}
