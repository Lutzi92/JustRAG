package chat

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/justrag/go-backend/internal/ai"
)

// runeSpan returns []rune(s)[start:end] as a string — the exact text a
// CitationSpanRef recovers from its source. Tests use this to assert the
// matcher's offsets are not just numerically right but actually bracket
// the matched text.
func spanText(s string, start, end int) string {
	r := []rune(s)
	return string(r[start:end])
}

// TestMatchQuoteSpan_ExactAndNormalised drives MatchQuoteSpan against a
// fixed content string with quotes that each exercise one leg of the
// W3-R3 normalisation (exact, case, whitespace-collapse, quote-char
// strip) plus two negative cases (absent, too short).
//
// Mutation: skip the NFC/lower-case step in normalizeForMatch → the
// upper-case row ("WIRD VON ...") stops matching and this test fails.
func TestMatchQuoteSpan_ExactAndNormalised(t *testing.T) {
	t.Parallel()
	content := "Das Stud.IP-Update wird von Frau Müller geleitet.\n\nDie   Version ist 5.4."

	// Case 1 + 2 (exact, case-insensitive): both match the same underlying
	// span, computed from the literal substring's byte range.
	exactPhrase := "wird von Frau Müller geleitet"
	exactIdx := strings.Index(content, exactPhrase)
	if exactIdx < 0 {
		t.Fatalf("fixture bug: %q not found in content", exactPhrase)
	}
	exactStart := utf8.RuneCountInString(content[:exactIdx])
	exactEnd := exactStart + utf8.RuneCountInString(exactPhrase)

	// Case 3 (whitespace collapse): the quote uses single spaces, content
	// uses a triple space before "Version". The literal substring in
	// content (triple space, no trailing period) is what Start:End must
	// recover.
	rawTripleSpacePhrase := "Die   Version ist 5.4"
	tripleIdx := strings.Index(content, rawTripleSpacePhrase)
	if tripleIdx < 0 {
		t.Fatalf("fixture bug: %q not found in content", rawTripleSpacePhrase)
	}
	tripleStart := utf8.RuneCountInString(content[:tripleIdx])
	tripleEnd := tripleStart + utf8.RuneCountInString(rawTripleSpacePhrase)

	cases := []struct {
		name      string
		quote     string
		wantStart int
		wantEnd   int
		wantText  string
		wantOK    bool
	}{
		{"exact", exactPhrase, exactStart, exactEnd, exactPhrase, true},
		{"case-insensitive", "WIRD VON FRAU MÜLLER GELEITET", exactStart, exactEnd, exactPhrase, true},
		{"whitespace collapse + no trailing period", "Die Version ist 5.4", tripleStart, tripleEnd, rawTripleSpacePhrase, true},
		{"quote characters stripped", "„wird von Frau Müller geleitet“", exactStart, exactEnd, exactPhrase, true},
		{"absent", "Frau Meier leitet", 0, 0, "", false},
		{"too short (<12 runes)", "Müller", 0, 0, "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := MatchQuoteSpan(content, c.quote)
			if ok != c.wantOK {
				t.Fatalf("%q: ok = %v, want %v (got %+v)", c.quote, ok, c.wantOK, got)
			}
			if !ok {
				return
			}
			if got.Start != c.wantStart || got.End != c.wantEnd {
				t.Errorf("%q: got [%d,%d), want [%d,%d)", c.quote, got.Start, got.End, c.wantStart, c.wantEnd)
			}
			if gotText := spanText(content, got.Start, got.End); gotText != c.wantText {
				t.Errorf("%q: matched text = %q, want %q", c.quote, gotText, c.wantText)
			}
		})
	}
}

// TestMatchQuoteSpan_NFCComposition exercises W3-R3's NFC step across a
// rune-count change: composing a decomposed base+combining-mark sequence
// ("u" + combining diaeresis U+0308, 2 runes) into one precomposed
// character ("ü", 1 rune) changes the rune count. An earlier
// implementation bailed out of NFC entirely whenever that happened —
// exactly the case NFC exists for — so a decomposed source never matched
// a precomposed quote (or vice versa). Both directions must match here.
//
// Mutation: reintroduce that "skip NFC when rune count changes" bail-out
// in normalizeForMatch → the decomposed-content subtest fails (content's
// raw, uncomposed "u"+combining-mark sequence never equals the quote's
// precomposed "ü", so MatchQuoteSpan returns ok=false).
func TestMatchQuoteSpan_NFCComposition(t *testing.T) {
	t.Parallel()
	const decomposedU = "u\u0308" // "ü" as base "u" + combining diaeresis U+0308 (2 runes)

	t.Run("decomposed content, precomposed quote", func(t *testing.T) {
		before := "Frau M"
		after := "ller leitet"
		content := before + decomposedU + after + " das Projekt"
		quote := "Frau Müller leitet" // precomposed

		wantStart := 0
		wantEnd := utf8.RuneCountInString(before) + utf8.RuneCountInString(decomposedU) + utf8.RuneCountInString(after)

		got, ok := MatchQuoteSpan(content, quote)
		if !ok {
			t.Fatalf("expected a match, got ok=false")
		}
		if got.Start != wantStart || got.End != wantEnd {
			t.Errorf("got [%d,%d), want [%d,%d)", got.Start, got.End, wantStart, wantEnd)
		}
		extracted := string([]rune(content)[got.Start:got.End])
		gotNFC := norm.NFC.String(strings.ToLower(extracted))
		wantNFC := norm.NFC.String(strings.ToLower(quote))
		if gotNFC != wantNFC {
			t.Errorf("NFC'd extracted text = %q, want %q", gotNFC, wantNFC)
		}
	})

	t.Run("precomposed content, decomposed quote", func(t *testing.T) {
		matchText := "Frau Müller leitet" // precomposed
		content := matchText + " das Projekt"
		quote := "Frau M" + decomposedU + "ller leitet" // decomposed "ü"

		wantStart := 0
		wantEnd := utf8.RuneCountInString(matchText)

		got, ok := MatchQuoteSpan(content, quote)
		if !ok {
			t.Fatalf("expected a match, got ok=false")
		}
		if got.Start != wantStart || got.End != wantEnd {
			t.Errorf("got [%d,%d), want [%d,%d)", got.Start, got.End, wantStart, wantEnd)
		}
		extracted := string([]rune(content)[got.Start:got.End])
		gotNFC := norm.NFC.String(strings.ToLower(extracted))
		wantNFC := norm.NFC.String(strings.ToLower(quote))
		if gotNFC != wantNFC {
			t.Errorf("NFC'd extracted text = %q, want %q", gotNFC, wantNFC)
		}
	})
}

// fakeQuoteExtractor is a test double for extractQuotesFn: it records every
// call's requests (for assertions on what was — and was not — sent to the
// extractor) and returns a canned result/error.
type fakeQuoteExtractor struct {
	results      []quoteResult
	err          error
	capturedReqs [][]quoteRequest
}

func (f *fakeQuoteExtractor) fn(_ context.Context, _ *ai.ConfigResolver, reqs []quoteRequest, _, _, _ string) ([]quoteResult, error) {
	f.capturedReqs = append(f.capturedReqs, append([]quoteRequest(nil), reqs...))
	return f.results, f.err
}

// withExtractQuotesFn swaps the package-level extractQuotesFn seam for the
// duration of the calling test and restores the original on cleanup.
func withExtractQuotesFn(t *testing.T, fn func(context.Context, *ai.ConfigResolver, []quoteRequest, string, string, string) ([]quoteResult, error)) {
	t.Helper()
	orig := extractQuotesFn
	extractQuotesFn = fn
	t.Cleanup(func() { extractQuotesFn = orig })
}

// TestApplySpanVerification_FillsSpanAndMethod exercises the whole
// collect-request → extract → match → fill pipeline via an injected
// extractQuotesFn, with three cited sources: one the extractor grounds
// (N=1), one it returns nothing for (N=2), and one RAPTOR summary (N=3)
// that must never reach the extractor at all.
//
// Mutation: drop the summary exclusion in collectQuoteRequests → the
// captured-requests assertion below (no request for N=3) fails.
func TestApplySpanVerification_FillsSpanAndMethod(t *testing.T) {
	content1 := "Das Stud.IP-Update wird von Frau Müller geleitet.\n\nDie   Version ist 5.4."
	exactPhrase := "wird von Frau Müller geleitet"
	exactIdx := strings.Index(content1, exactPhrase)
	wantStart := utf8.RuneCountInString(content1[:exactIdx])
	wantEnd := wantStart + utf8.RuneCountInString(exactPhrase)

	answer := "Claim one [1]. Claim two [2]. Claim three [3]."
	sources := []ChatSource{
		{Index: 1, Content: content1},
		{Index: 2, Content: "Completely unrelated source text with no bearing on anything."},
		{Index: 3, Content: "A paraphrased RAPTOR summary of several documents.", NodeKind: "summary"},
	}
	statuses := []CitationStatus{
		{N: 1, Verified: false, Reason: "no_overlap"},
		{N: 2, Verified: false, Reason: "no_overlap"},
		{N: 3, Verified: false, Reason: "no_overlap"},
	}

	fake := &fakeQuoteExtractor{results: []quoteResult{{N: 1, Quote: exactPhrase}}}
	withExtractQuotesFn(t, fake.fn)

	cfg := SpanConfig{Model: "m", MaxSources: 12, Timeout: time.Second, Lang: "de", KbID: "kb-1"}
	got := ApplySpanVerification(context.Background(), nil, cfg, answer, sources, statuses)

	// N=1: filled.
	s1, ok := statusByN(got, 1)
	if !ok || !s1.Verified || s1.Method != "span" || s1.Span == nil {
		t.Fatalf("status 1: expected span-verified, got %+v", s1)
	}
	if s1.Span.Start != wantStart || s1.Span.End != wantEnd {
		t.Errorf("status 1 span: got [%d,%d), want [%d,%d)", s1.Span.Start, s1.Span.End, wantStart, wantEnd)
	}

	// N=2: no quote returned for it — unchanged.
	s2, ok := statusByN(got, 2)
	if !ok || s2.Verified || s2.Method != "" || s2.Reason != "no_overlap" || s2.Span != nil {
		t.Errorf("status 2: expected unchanged, got %+v", s2)
	}

	// N=3: summary source — unchanged, AND must never have been requested.
	s3, ok := statusByN(got, 3)
	if !ok || s3.Verified || s3.Method != "" || s3.Span != nil {
		t.Errorf("status 3: expected unchanged, got %+v", s3)
	}
	if len(fake.capturedReqs) != 1 {
		t.Fatalf("expected exactly one extractor call, got %d", len(fake.capturedReqs))
	}
	for _, r := range fake.capturedReqs[0] {
		if r.N == 3 {
			t.Errorf("summary source N=3 was sent to the extractor: %+v", r)
		}
	}
	if len(fake.capturedReqs[0]) != 2 {
		t.Errorf("expected 2 requests (N=1, N=2), got %d: %+v", len(fake.capturedReqs[0]), fake.capturedReqs[0])
	}
}

// TestApplySpanVerification_TimeoutOrErrorLeavesStatuses asserts the
// fail-soft contract: any extractor error leaves statuses byte-identical,
// even when the extractor also returned (bogus, in this case) results
// alongside the error.
//
// Mutation: drop the `err != nil` guard before applying results → this
// test fails because status 1 would pick up the bogus span the fake
// returns alongside its error.
func TestApplySpanVerification_TimeoutOrErrorLeavesStatuses(t *testing.T) {
	content1 := "Das Stud.IP-Update wird von Frau Müller geleitet.\n\nDie   Version ist 5.4."
	answer := "Claim one [1]."
	sources := []ChatSource{{Index: 1, Content: content1}}
	statuses := []CitationStatus{{N: 1, Verified: false, Reason: "no_overlap"}}
	want := append([]CitationStatus(nil), statuses...)

	fake := &fakeQuoteExtractor{
		results: []quoteResult{{N: 1, Quote: "wird von Frau Müller geleitet"}},
		err:     errors.New("extractor boom"),
	}
	withExtractQuotesFn(t, fake.fn)

	cfg := SpanConfig{Model: "m", MaxSources: 12, Timeout: time.Second, Lang: "de", KbID: "kb-1"}
	got := ApplySpanVerification(context.Background(), nil, cfg, answer, sources, statuses)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("statuses changed on extractor error: got %+v, want %+v", got, want)
	}
}
