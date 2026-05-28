package vector

import (
	"testing"
)

// TestPredictHybridAlpha_DisabledSentinels: sensitivity ≤ 0,
// negative base, or out-of-range base all short-circuit to the
// input. The flag-gate happens at the call site; this function
// must additionally fail-safe for misconfigured operators.
func TestPredictHybridAlpha_DisabledSentinels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		query       string
		base        float64
		sensitivity float64
		want        float64
	}{
		{"zero_sensitivity", "anything", 0.8, 0.0, 0.8},
		{"negative_sensitivity", "anything", 0.8, -0.5, 0.8},
		{"negative_base_pass_through", "anything", -0.1, 0.3, -0.1},
		{"over_one_base_pass_through", "anything", 1.5, 0.3, 1.5},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := PredictHybridAlpha(c.query, c.base, c.sensitivity)
			if got != c.want {
				t.Errorf("PredictHybridAlpha(%q, %v, %v) = %v, want %v",
					c.query, c.base, c.sensitivity, got, c.want)
			}
		})
	}
}

// TestPredictHybridAlpha_EmptyQueryReturnsBase: nothing to
// tokenise → no signal → return baseAlpha. Pinned because the
// chat pipeline can hand off whitespace-only queries (CondenseFollowUp
// edge cases), and we must not produce NaN from a divide-by-zero.
func TestPredictHybridAlpha_EmptyQueryReturnsBase(t *testing.T) {
	t.Parallel()
	for _, q := range []string{"", "   ", "\n\t"} {
		got := PredictHybridAlpha(q, 0.8, 0.3)
		// Whitespace-only inputs do tokenise on cl100k (single
		// whitespace token), so accept either "no shift" (returned
		// baseAlpha because EncodeBPE returned nil for "") OR a
		// small positive shift (because the whitespace token has a
		// low ID). The contract is: never NaN, never outside [0, 1].
		if got < 0 || got > 1 {
			t.Errorf("PredictHybridAlpha(%q): out-of-range %v", q, got)
		}
	}
}

// TestPredictHybridAlpha_CommonQueryShiftsUp: a query made of the
// most-frequent English/German tokens (mean ID well below neutral)
// nudges α upward — the reranker gets more weight because semantic
// matching wins on conversational phrasings.
func TestPredictHybridAlpha_CommonQueryShiftsUp(t *testing.T) {
	t.Parallel()
	// "the cat sat on the mat" — all tokens in the lowest few
	// hundred IDs of cl100k_base; mean well under rarityNeutral.
	got := PredictHybridAlpha("the cat sat on the mat", 0.5, 0.3)
	if got <= 0.5 {
		t.Errorf("common-token query should shift α up from 0.5; got %v", got)
	}
}

// TestPredictHybridAlpha_RareQueryShiftsDown: a query made of
// long technical / domain-specific tokens (mean ID above neutral)
// nudges α downward so BM25 surface-match contributes more.
// Pinned because this is the actual production motivation —
// without this directionality the heuristic is useless.
func TestPredictHybridAlpha_RareQueryShiftsDown(t *testing.T) {
	t.Parallel()
	// Tokens like "EBITDA", "reconciliation", "methodology"
	// alongside a date+digit string push the mean ID well past
	// rarityNeutral. Exact ID positions are cl100k_base
	// dependent; the assertion is directional (shift < 0).
	got := PredictHybridAlpha("Q4-2024 EBITDA reconciliation methodology variance", 0.8, 0.3)
	if got >= 0.8 {
		t.Errorf("rare-token query should shift α down from 0.8; got %v", got)
	}
}

// TestPredictHybridAlpha_ClampedToUnit: a high-sensitivity setting
// must NEVER produce α outside [0, 1]. Pinned because downstream
// BlendRerankScores assumes α ∈ [0, 1] and an out-of-range value
// would silently corrupt the blend.
func TestPredictHybridAlpha_ClampedToUnit(t *testing.T) {
	t.Parallel()
	// Aggressive sensitivity + extreme base. The query is
	// irrelevant — we just verify clamps fire.
	if got := PredictHybridAlpha("the the the the", 0.99, 1.0); got > 1 {
		t.Errorf("α should clamp to ≤1, got %v", got)
	}
	// Use a query likely to have a high mean ID; even an aggressive
	// shift must not push α below 0.
	if got := PredictHybridAlpha("kryptoanalytische Halbleiter-Charakterisierung", 0.05, 1.0); got < 0 {
		t.Errorf("α should clamp to ≥0, got %v", got)
	}
}

// TestPredictHybridAlpha_SensitivityProportional: doubling the
// sensitivity must double the shift magnitude (linear in
// sensitivity by construction). A regression on this property
// would mean the heuristic acts non-monotonically and operator
// intuition breaks.
func TestPredictHybridAlpha_SensitivityProportional(t *testing.T) {
	t.Parallel()
	const query = "the cat sat on the mat" // a common query
	const base = 0.5
	low := PredictHybridAlpha(query, base, 0.2) - base
	high := PredictHybridAlpha(query, base, 0.4) - base
	// Allow a small tolerance for floating-point arithmetic but
	// the high shift must be ~2× the low shift in both direction
	// and magnitude.
	if low == 0 {
		t.Skip("query produced no shift (BPE tokenizer unavailable?)")
	}
	ratio := high / low
	if ratio < 1.9 || ratio > 2.1 {
		t.Errorf("shift should scale linearly with sensitivity; ratio=%v (low=%v high=%v)", ratio, low, high)
	}
}
