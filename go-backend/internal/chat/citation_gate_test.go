package chat

import (
	"context"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
)

// TestCitationStatusesAndGate_UsesPreSpanVerdicts pins the review-wave rule:
// span verification (W3-R1) upgrades a `no_overlap` citation to Verified for
// DISPLAY, but the verifier cost gate must keep reading the deterministic
// (pre-span) verdicts. Computing the gate from the post-span statuses silently
// skipped the factuality verifier / Self-RAG / refine gate on every turn where
// the span pass rescued the only suspect citation.
//
// The span applier here is the REAL ApplySpanVerification driven through the
// extractQuotesFn seam, so the test also proves the upgrade actually happens
// (i.e. the gate signal and the displayed status genuinely disagree).
//
// Mutation: compute the loop in citationStatusesAndGate over the post-span
// slice instead of the snapshot → suspect becomes false and this fails.
func TestCitationStatusesAndGate_UsesPreSpanVerdicts(t *testing.T) {
	content := "Das Stud.IP-Update wird von Frau Müller geleitet."
	quote := "wird von Frau Müller geleitet"
	answer := "Claim one [1]."
	sources := []ChatSource{{Index: 1, Content: content}}
	det := []CitationStatus{{N: 1, Verified: false, Reason: "no_overlap"}}

	fake := &fakeQuoteExtractor{results: []quoteResult{{N: 1, Quote: quote}}}
	withExtractQuotesFn(t, fake.fn)

	cfg := SpanConfig{Model: "m", MaxSources: 12, Timeout: time.Second, Lang: "de", KbID: "kb-1"}
	display, suspect := citationStatusesAndGate(det, func(in []CitationStatus) []CitationStatus {
		return ApplySpanVerification(context.Background(), (*ai.ConfigResolver)(nil), cfg, answer, sources, in)
	})

	if !suspect {
		t.Error("verifier gate lost its suspect: the deterministic pass said no_overlap, so the verifier must still run")
	}
	s1, ok := statusByN(display, 1)
	if !ok || !s1.Verified || s1.Method != "span" {
		t.Fatalf("displayed status must carry the span upgrade, got %+v", s1)
	}
}

// TestCitationStatusesAndGate_NoSpanPass keeps the flag-off path honest: with
// no span applier the gate is simply "any deterministic verdict unverified".
func TestCitationStatusesAndGate_NoSpanPass(t *testing.T) {
	allGood := []CitationStatus{{N: 1, Verified: true, Method: "ngram"}}
	if _, suspect := citationStatusesAndGate(allGood, nil); suspect {
		t.Error("all-verified statuses must not raise a suspect")
	}
	oneBad := []CitationStatus{{N: 1, Verified: true, Method: "ngram"}, {N: 2, Verified: false, Reason: "out_of_range"}}
	if _, suspect := citationStatusesAndGate(oneBad, nil); !suspect {
		t.Error("an unverified citation must raise a suspect")
	}
}
