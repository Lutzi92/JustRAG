package ai

import (
	"testing"
)

// TestNormaliseSelfRAGResult_ClampsToEnums covers the three closed
// enums (ISREL verdict, ISSUP reason, ISUSE verdict). LLM drift
// gets coerced to the safest neighbour rather than dropped, so
// the metric counts stay accurate.
func TestNormaliseSelfRAGResult_ClampsToEnums(t *testing.T) {
	t.Parallel()
	in := SelfRAGResult{
		Relevance: []ChunkRelevance{
			{N: 1, Verdict: "Relevant"},
			{N: 2, Verdict: "very_relevant"}, // not in enum
			{N: 3, Verdict: "irrelevant"},
			{N: 0, Verdict: "relevant"}, // invalid N → drop
		},
		FlaggedClaims: []FlaggedClaim{
			{ClaimText: " good claim ", Reason: "UNSUPPORTED"},
			{ClaimText: "", Reason: "unsupported"},         // drop on empty text
			{ClaimText: "weird", Reason: "made_up_reason"}, // reason coerces to "unsupported"
		},
		Usefulness: UsefulnessVerdict{
			Verdict: "MOSTLY_YES",
			Reason:  "  whatever  ",
		},
	}
	got := normaliseSelfRAGResult(in)

	if len(got.Relevance) != 3 {
		t.Errorf("Relevance: expected 3 surviving (N>0), got %d", len(got.Relevance))
	}
	for _, r := range got.Relevance {
		switch r.Verdict {
		case "relevant", "partially_relevant", "irrelevant":
		default:
			t.Errorf("ISREL verdict %q escaped enum coercion", r.Verdict)
		}
	}

	if len(got.FlaggedClaims) != 2 {
		t.Errorf("FlaggedClaims: expected 2 surviving, got %d", len(got.FlaggedClaims))
	}
	for _, c := range got.FlaggedClaims {
		if c.ClaimText == "" {
			t.Error("empty ClaimText slipped through")
		}
		switch c.Reason {
		case "unsupported", "contradicted", "out_of_scope":
		default:
			t.Errorf("ISSUP reason %q escaped enum coercion", c.Reason)
		}
	}

	// "MOSTLY_YES" is not in the enum → coerce to "yes" (safest:
	// no false-positive ISUSE-driven refine).
	if got.Usefulness.Verdict != "yes" {
		t.Errorf("ISUSE verdict drift: got %q, want yes (safety coerce)", got.Usefulness.Verdict)
	}
	if got.Usefulness.Reason != "whatever" {
		t.Errorf("ISUSE reason should be trimmed: %q", got.Usefulness.Reason)
	}
}

// TestNormaliseSelfRAGResult_PreservesValidEnum: when the LLM
// emits clean values, normalise must NOT mutate them.
func TestNormaliseSelfRAGResult_PreservesValidEnum(t *testing.T) {
	t.Parallel()
	in := SelfRAGResult{
		Relevance: []ChunkRelevance{
			{N: 1, Verdict: "relevant"},
			{N: 2, Verdict: "partially_relevant"},
		},
		FlaggedClaims: []FlaggedClaim{
			{ClaimText: "x", Reason: "contradicted", CitedN: 3},
		},
		Usefulness: UsefulnessVerdict{Verdict: "no", Reason: "off-topic"},
	}
	got := normaliseSelfRAGResult(in)
	if got.Usefulness.Verdict != "no" {
		t.Errorf("valid 'no' got mutated to %q", got.Usefulness.Verdict)
	}
	if got.FlaggedClaims[0].Reason != "contradicted" {
		t.Errorf("valid 'contradicted' got mutated to %q", got.FlaggedClaims[0].Reason)
	}
	if got.Relevance[1].Verdict != "partially_relevant" {
		t.Errorf("valid 'partially_relevant' got mutated to %q", got.Relevance[1].Verdict)
	}
}

// TestParseSelfRAGJSON_PlainAndWrapped: tolerate markdown
// fences + trailing prose. Same behaviour as the factuality
// verifier parser.
func TestParseSelfRAGJSON_PlainAndWrapped(t *testing.T) {
	t.Parallel()
	plain := `{"isrel":[{"n":1,"verdict":"relevant"}],"issup":[],"isuse":{"verdict":"yes","reason":"ok"}}`
	got, err := parseSelfRAGJSON(plain)
	if err != nil {
		t.Fatalf("plain JSON: %v", err)
	}
	if got.Usefulness.Verdict != "yes" {
		t.Errorf("plain parse lost usefulness: %+v", got.Usefulness)
	}

	wrapped := "```json\n" + plain + "\n```\nDone."
	got, err = parseSelfRAGJSON(wrapped)
	if err != nil {
		t.Fatalf("wrapped JSON: %v", err)
	}
	if got.Usefulness.Verdict != "yes" {
		t.Errorf("wrapped parse failed: %+v", got)
	}
}

// TestAnyIrrelevantOrPartial pins the ISREL flagging classifier
// used by the metric label switch.
func TestAnyIrrelevantOrPartial(t *testing.T) {
	t.Parallel()
	all := []ChunkRelevance{{Verdict: "relevant"}, {Verdict: "relevant"}}
	if anyIrrelevantOrPartial(all) {
		t.Error("all-relevant should NOT flag")
	}
	partial := []ChunkRelevance{{Verdict: "relevant"}, {Verdict: "partially_relevant"}}
	if !anyIrrelevantOrPartial(partial) {
		t.Error("partially_relevant must flag")
	}
	irr := []ChunkRelevance{{Verdict: "relevant"}, {Verdict: "irrelevant"}}
	if !anyIrrelevantOrPartial(irr) {
		t.Error("irrelevant must flag")
	}
	if anyIrrelevantOrPartial(nil) {
		t.Error("nil should NOT flag")
	}
}
