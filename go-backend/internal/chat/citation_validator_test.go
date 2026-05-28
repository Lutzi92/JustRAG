package chat

import "testing"

// statusByN looks up the validator output for a given citation number. Tests
// don't depend on the slice ordering — order is "first appearance" today, but
// pinning that is a fragile invariant.
func statusByN(s []CitationStatus, n int) (CitationStatus, bool) {
	for _, c := range s {
		if c.N == n {
			return c, true
		}
	}
	return CitationStatus{}, false
}

func TestValidateCitations_NoMarkersReturnsNil(t *testing.T) {
	t.Parallel()
	got := ValidateCitations("This answer has no citations.", []ChatSource{
		{Index: 1, Content: "Whatever"},
	})
	if got != nil {
		t.Errorf("expected nil for no markers, got %v", got)
	}
}

// TestValidateCitations_HappyPath: the chunk's exact wording sits in the
// citation's sentence — should be verified with no reason.
func TestValidateCitations_HappyPath(t *testing.T) {
	t.Parallel()
	answer := "The Müller GmbH contract sets the notice period at six weeks before quarter end [1]."
	sources := []ChatSource{
		{Index: 1, Content: "§3 Kündigung. The Müller GmbH contract sets the notice period at six weeks before the end of each calendar quarter, in line with the standard German employment statute."},
	}
	got := ValidateCitations(answer, sources)
	c, ok := statusByN(got, 1)
	if !ok || !c.Verified || c.Reason != "" {
		t.Errorf("expected [1] verified, got %+v", got)
	}
	if c.Method != "ngram" {
		t.Errorf("Method: got %q, want \"ngram\"", c.Method)
	}
}

// TestValidateCitations_OutOfRange: [3] in a 2-source answer must fail with
// the explicit out_of_range reason — no partial credit, no n-gram fallback.
func TestValidateCitations_OutOfRange(t *testing.T) {
	t.Parallel()
	answer := "The notice period is six weeks [3]."
	sources := []ChatSource{
		{Index: 1, Content: "Source 1 content."},
		{Index: 2, Content: "Source 2 content."},
	}
	got := ValidateCitations(answer, sources)
	c, ok := statusByN(got, 3)
	if !ok {
		t.Fatalf("missing N=3: %+v", got)
	}
	if c.Verified {
		t.Errorf("[3] must be unverified (out of range)")
	}
	if c.Reason != "out_of_range" {
		t.Errorf("expected reason out_of_range, got %q", c.Reason)
	}
}

// TestValidateCitations_NoOverlap: cited chunk and answer share no content
// tokens → should be flagged as no_overlap.
func TestValidateCitations_NoOverlap(t *testing.T) {
	t.Parallel()
	answer := "Photosynthesis occurs in plant chloroplasts during daylight hours [1]."
	sources := []ChatSource{
		{Index: 1, Content: "The German tax code requires withholding payments to be reported quarterly."},
	}
	got := ValidateCitations(answer, sources)
	c, ok := statusByN(got, 1)
	if !ok {
		t.Fatalf("missing N=1: %+v", got)
	}
	if c.Verified {
		t.Errorf("[1] must be unverified (topics disjoint)")
	}
	if c.Reason != "no_overlap" {
		t.Errorf("expected reason no_overlap, got %q", c.Reason)
	}
}

// TestValidateCitations_MultiCiteAnyOverlap: [1, 2] is verified when AT
// LEAST ONE source overlaps. Tests the agreed semantics — the user typing
// both numbers means "either source supports this".
func TestValidateCitations_MultiCiteAnyOverlap(t *testing.T) {
	t.Parallel()
	answer := "The notice period is six weeks before quarter end [1, 2]."
	sources := []ChatSource{
		{Index: 1, Content: "Completely unrelated cooking recipe with garlic and onions and tomatoes."},
		{Index: 2, Content: "Standard German employment statutes set notice period to six weeks before each calendar quarter ends."},
	}
	got := ValidateCitations(answer, sources)

	// Both Ns must be marked verified, because at least one source overlapped.
	for _, n := range []int{1, 2} {
		c, ok := statusByN(got, n)
		if !ok {
			t.Errorf("missing N=%d: %+v", n, got)
			continue
		}
		if !c.Verified {
			t.Errorf("[%d] should be verified under any-source semantics", n)
		}
		if c.Method != "ngram" {
			t.Errorf("[%d] Method: got %q, want \"ngram\"", n, c.Method)
		}
	}
}

// TestValidateCitations_DuplicateCitationDeduped: the same N appearing
// multiple times in one answer produces a single status entry. Once any
// occurrence verifies, the entry stays verified — we never flap a "good"
// citation back to suspect because a later mention happened to land in a
// drier sentence.
func TestValidateCitations_DuplicateCitationDeduped(t *testing.T) {
	t.Parallel()
	answer := "Random unrelated sentence about cooking [1]. " +
		"The notice period is six weeks before quarter end [1]."
	sources := []ChatSource{
		{Index: 1, Content: "Standard German employment statutes set notice period to six weeks before each calendar quarter ends."},
	}
	got := ValidateCitations(answer, sources)
	if len(got) != 1 {
		t.Fatalf("expected 1 deduped entry, got %d: %+v", len(got), got)
	}
	if got[0].N != 1 {
		t.Errorf("expected N=1, got %d", got[0].N)
	}
	// The second occurrence of [1] sits in a sentence that overlaps with
	// the source, so the dedup-aware merge must end up verified=true.
	if !got[0].Verified {
		t.Errorf("expected verified=true after at least one passing occurrence, got %+v", got[0])
	}
	if got[0].Method != "ngram" {
		t.Errorf("Method: got %q, want \"ngram\"", got[0].Method)
	}
}

// TestValidateCitations_SentenceWindow: the n-gram match must come from the
// LOCAL sentence window, not from a far-away paragraph. Without window
// scoping this test would falsely verify [1].
func TestValidateCitations_SentenceWindow(t *testing.T) {
	t.Parallel()
	answer := "Cell biology is fascinating. Photosynthesis happens in chloroplasts. " +
		"Now changing topic completely. The German tax code is byzantine [1]."
	sources := []ChatSource{
		{Index: 1, Content: "Photosynthesis happens in chloroplasts inside plant cells."},
	}
	got := ValidateCitations(answer, sources)
	c, ok := statusByN(got, 1)
	if !ok {
		t.Fatalf("missing N=1: %+v", got)
	}
	if c.Verified {
		t.Errorf("[1] should NOT verify — overlap is in a far-away sentence; got %+v", c)
	}
}

// TestValidateCitations_ThreeGramFallback exercises the "4-gram missed,
// 3-gram caught it" path explicitly. The answer and source share exactly
// three consecutive content tokens (quantum entanglement enables) but
// diverge at the fourth, so the 4-gram set has no intersection. The
// validator must fall back to 3-grams and verify the citation.
func TestValidateCitations_ThreeGramFallback(t *testing.T) {
	t.Parallel()
	answer := "Quantum entanglement enables instant remote information transfer [1]."
	sources := []ChatSource{
		{Index: 1, Content: "Our experiment showed quantum entanglement enables fast measurement."},
	}
	got := ValidateCitations(answer, sources)
	c, ok := statusByN(got, 1)
	if !ok {
		t.Fatalf("missing N=1: %+v", got)
	}
	if !c.Verified {
		t.Errorf("[1] should verify via 3-gram fallback (shared: quantum/entanglement/enables), got %+v", c)
	}
	if c.Method != "ngram" {
		t.Errorf("Method: got %q, want \"ngram\"", c.Method)
	}
	if c.Reason != "" {
		t.Errorf("expected empty Reason on verified citation, got %q", c.Reason)
	}
}

// TestValidateCitations_GermanTokens: validator must work on German content
// (Umlauts, compounds). Real-world chat answers in this codebase are
// German-first.
func TestValidateCitations_GermanTokens(t *testing.T) {
	t.Parallel()
	answer := "Die Kündigungsfrist beträgt sechs Wochen zum Quartalsende [1]."
	sources := []ChatSource{
		{Index: 1, Content: "Vertrag Müller GmbH. Die Kündigungsfrist beträgt sechs Wochen zum Quartalsende laut §3 des Vertrages."},
	}
	got := ValidateCitations(answer, sources)
	c, ok := statusByN(got, 1)
	if !ok || !c.Verified {
		t.Errorf("expected verified citation on German content, got %+v", got)
	}
	if c.Method != "ngram" {
		t.Errorf("Method: got %q, want \"ngram\"", c.Method)
	}
}

// TestTokenize_DropsStopwordsAndShortTokens guards the token filter — any
// regression here changes every overlap result, so it deserves its own test.
func TestTokenize_DropsStopwordsAndShortTokens(t *testing.T) {
	t.Parallel()
	got := tokenize("The quick brown fox of an old day")
	want := []string{"quick", "brown", "fox", "old", "day"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestParseCitationNumbers covers single, multi-cite, and noise tolerance.
//
// Slice (not map) so iteration order is deterministic — map order shuffles
// between runs, which made "first failure" CI logs non-reproducible.
func TestParseCitationNumbers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []int
	}{
		{"3", []int{3}},
		{"1, 2", []int{1, 2}},
		{" 5 , 7 ", []int{5, 7}},
		{"1, 2, 5", []int{1, 2, 5}},
		{"", nil},
		{"abc", nil},
		{"0, 1", []int{1}}, // 0 is not a valid 1-based citation
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got := parseCitationNumbers(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("[%d]: got %d, want %d", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestMergeVerification_NilWhenEmpty confirms the wrapper returns nil when
// neither subsystem produced anything — important: callers branch on nil to
// decide whether to send an SSE event at all.
func TestMergeVerification_NilWhenEmpty(t *testing.T) {
	t.Parallel()
	if mergeVerification(nil, nil, nil, nil, nil) != nil {
		t.Error("expected nil when all inputs empty")
	}
	if mergeVerification(nil, []CitationStatus{}, nil, nil, nil) != nil {
		t.Error("expected nil when citations slice empty")
	}
}

// TestMergeVerification_CitationsOnly: validator ran, factcheck didn't.
// Issues should be empty (not nil) so the JSON wire format stays stable.
func TestMergeVerification_CitationsOnly(t *testing.T) {
	t.Parallel()
	cs := []CitationStatus{{N: 1, Verified: true}}
	v := mergeVerification(nil, cs, nil, nil, nil)
	if v == nil {
		t.Fatal("expected non-nil verification")
	}
	if v.Issues == nil {
		t.Error("Issues must be empty slice, not nil")
	}
	if len(v.Citations) != 1 {
		t.Errorf("expected 1 citation, got %d", len(v.Citations))
	}
}

// TestMergeVerification_RefineOnly: AP-A1 gate ran, no other subsystem
// produced anything. The wrapper must still surface — the frontend uses
// Refine alone to know "this answer was rewritten".
func TestMergeVerification_RefineOnly(t *testing.T) {
	t.Parallel()
	r := &RefineStatus{Mode: "surgical", ClaimsBefore: 2, ClaimsAfter: 0}
	v := mergeVerification(nil, nil, nil, r, nil)
	if v == nil {
		t.Fatal("expected non-nil verification when only Refine populated")
	}
	if v.Refine == nil || v.Refine.Mode != "surgical" {
		t.Errorf("Refine not bubbled through: %+v", v.Refine)
	}
	if v.Refine.ClaimsBefore != 2 || v.Refine.ClaimsAfter != 0 {
		t.Errorf("Refine counters lost: before=%d after=%d", v.Refine.ClaimsBefore, v.Refine.ClaimsAfter)
	}
}

func TestValidateCitations_VerifiedMethodIsNgram(t *testing.T) {
	t.Parallel()
	answer := "Foo bar baz qux. The notice period is six weeks [1]."
	sources := []ChatSource{
		{Index: 1, Content: "the notice period is six weeks for permanent staff"},
	}
	got := ValidateCitations(answer, sources)
	if len(got) != 1 {
		t.Fatalf("want 1 status, got %d", len(got))
	}
	if !got[0].Verified {
		t.Fatalf("citation should be verified by n-gram; got %+v", got[0])
	}
	if got[0].Method != "ngram" {
		t.Errorf("Method: got %q, want \"ngram\"", got[0].Method)
	}
	if got[0].Reason != "" {
		t.Errorf("Reason: got %q, want empty", got[0].Reason)
	}
}

// TestValidateCitations_SummaryCitedAgainstDescendantLeaf: when a RAPTOR
// summary is cited and the supporting n-gram only lives in a descendant
// leaf (the summary's prose is paraphrased), the validator should union
// the summary's content with DescendantContents before tokenising and
// verify the citation against the union.
func TestValidateCitations_SummaryCitedAgainstDescendantLeaf(t *testing.T) {
	t.Parallel()
	// The answer's n-gram ("acquired Beta Inc on July 12 2026") is NOT
	// in the summary content. Only the descendant leaf carries it.
	// Without the union, validation should fail (no_overlap); with the
	// union it should pass.
	sources := []ChatSource{
		{
			Index:    1,
			Content:  "This file covers Q3 revenue drivers and recent acquisitions.",
			NodeKind: "summary",
			DescendantContents: []string{
				"On July 12 2026, Acme Corp acquired Beta Inc for forty-two million dollars.",
			},
		},
	}
	answer := "Acme Corp acquired Beta Inc on July 12 2026 [1]."
	statuses := ValidateCitations(answer, sources)
	if len(statuses) != 1 {
		t.Fatalf("want 1 status, got %d", len(statuses))
	}
	if !statuses[0].Verified {
		t.Errorf("summary citation should validate against descendant; got %+v", statuses[0])
	}
}

// TestValidateCitations_SummaryWithoutDescendantsFallsThrough: a summary
// citation with no DescendantContents populated should still go through
// the legacy n-gram check on the summary content alone — no panic, no
// false-positive.
func TestValidateCitations_SummaryWithoutDescendantsFallsThrough(t *testing.T) {
	t.Parallel()
	sources := []ChatSource{
		{Index: 1, Content: "abstract summary about revenue drivers.", NodeKind: "summary"},
	}
	answer := "Acme acquired Beta on July 12 2026 [1]."
	statuses := ValidateCitations(answer, sources)
	if len(statuses) != 1 {
		t.Fatalf("want 1 status, got %d", len(statuses))
	}
	if statuses[0].Verified {
		t.Errorf("summary without descendants should not over-validate; got %+v", statuses[0])
	}
}
