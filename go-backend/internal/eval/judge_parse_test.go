package eval

import (
	"context"
	"strings"
	"testing"
)

// Regression tests for the judge JSON extractor (fix wave, finding F4).
//
// Two live judge failures were parked as "the judge wrapped its object in a
// ```json fence": G05 in t5-pw-ctrl.json (pairwise) and G08 in t5-flat1.json
// (faithfulness). Reproducing them showed the fence is NOT the cause — a
// fenced but COMPLETE object has always parsed, because unmarshalStrict falls
// back to the span between the first '{' and the last '}'. What that span
// extraction cannot survive is either of the two shapes below, both of which
// reproduce the observed error text exactly:
//
//   - a response cut off before the object closes (the span then ends at some
//     earlier inner '}' — or there is no '}' at all), and
//   - two objects in one reply (the span swallows both plus the text between
//     them, which is never valid JSON).
//
// The parser therefore scans BALANCED objects (string- and escape-aware) and
// tries each in order, and reports an unterminated object as a distinct
// "truncated JSON" error so the next occurrence is diagnosable from the
// report alone instead of being re-guessed. Garbage must still error: the
// same parser runs in production for the RAGAS background sampler and the
// in-app eval runner.

func TestUnmarshalStrict_FencedObjectParsesLikeUnfenced(t *testing.T) {
	var fenced, plain struct {
		Winner    string `json:"winner"`
		Reasoning string `json:"reasoning"`
	}
	if err := unmarshalStrict("```json\n{\"winner\":\"A\",\"reasoning\":\"kurz\"}\n```", &fenced); err != nil {
		t.Fatalf("fenced: unexpected error: %v", err)
	}
	if err := unmarshalStrict(`{"winner":"A","reasoning":"kurz"}`, &plain); err != nil {
		t.Fatalf("plain: unexpected error: %v", err)
	}
	if fenced != plain {
		t.Errorf("fenced %+v != plain %+v", fenced, plain)
	}
}

func TestUnmarshalStrict_TruncatedObjectIsADistinctError(t *testing.T) {
	// Shaped like the G05 payload: a fence, an object that never closes.
	payload := "```json\n{\"winner\":\"A\",\"reasoning\":\"Antwort A ist deutlich besser, da sie im Bereich der Schnittstellen wesentlich konkre"
	var v struct {
		Winner string `json:"winner"`
	}
	err := unmarshalStrict(payload, &v)
	if err == nil {
		t.Fatal("expected an error for a truncated object")
	}
	if !strings.Contains(err.Error(), "truncated JSON") {
		t.Errorf("expected a distinct truncated-JSON error, got %v", err)
	}
}

func TestUnmarshalStrict_TruncatedArrayOfObjectsIsADistinctError(t *testing.T) {
	// Shaped like the G08 payload: the faithfulness claim list is cut mid
	// array, so the LAST '}' sits inside the array and the old span
	// extraction produced `{"claims":[{...},{...` — invalid, but reported as
	// plain "not valid JSON".
	payload := "```json\n{\"claims\":[{\"text\":\"erste Behauptung\",\"supported\":true},{\"text\":\"zweite Behauptung"
	var v struct {
		Claims []struct {
			Supported bool `json:"supported"`
		} `json:"claims"`
	}
	err := unmarshalStrict(payload, &v)
	if err == nil {
		t.Fatal("expected an error for a truncated claim list")
	}
	if !strings.Contains(err.Error(), "truncated JSON") {
		t.Errorf("expected a distinct truncated-JSON error, got %v", err)
	}
}

func TestUnmarshalStrict_TwoObjectsParsesTheFirst(t *testing.T) {
	payload := "```json\n{\"winner\":\"A\",\"reasoning\":\"x\"}\n```\n```json\n{\"winner\":\"B\",\"reasoning\":\"y\"}\n```"
	var v struct {
		Winner string `json:"winner"`
	}
	if err := unmarshalStrict(payload, &v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Winner != "A" {
		t.Errorf("expected the FIRST object to win, got %q", v.Winner)
	}
}

func TestUnmarshalStrict_BracesInsideStringsDoNotConfuseTheScanner(t *testing.T) {
	var v struct {
		Reasoning string `json:"reasoning"`
	}
	if err := unmarshalStrict(`prose {"reasoning":"a } and a \" quote {"} trailing prose`, &v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Reasoning != `a } and a " quote {` {
		t.Errorf("string content mangled: %q", v.Reasoning)
	}
}

func TestUnmarshalStrict_GarbageStillErrors(t *testing.T) {
	var v struct {
		Winner string `json:"winner"`
	}
	for _, payload := range []string{"", "not json at all", "```json\n```", "[1,2,3]"} {
		if err := unmarshalStrict(payload, &v); err == nil {
			t.Errorf("expected an error for %q", payload)
		}
	}
}

// The three judge samples that failed on the Wave-5 Task-10 runs (t10-pw-2
// G06, t10-mr1 G05, t10-mr2 G09) all reported the plain "response is not
// valid JSON", which by construction rules OUT truncation (that is a distinct
// error, see above) and a code fence (that parses). What is left is a
// brace-balanced object the decoder still rejects — and the recorded
// 120-byte preview could not say which of the candidate shapes it was,
// because the offending byte sits past the preview and the decoder's own
// error was dropped. It is carried now, so one occurrence is enough to name
// the cause.
func TestUnmarshalStrict_BalancedButInvalidNamesTheCause(t *testing.T) {
	var v struct {
		Winner string `json:"winner"`
	}
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "raw newline inside a string",
			payload: "```json\n{\"winner\":\"A\",\"reasoning\":\"Zeile eins\nZeile zwei\"}\n```",
			want:    "invalid character '\\n' in string literal",
		},
		{
			name:    "unescaped quote inside a string",
			payload: "```json\n{\"winner\":\"A\",\"reasoning\":\"Die Frage nach der \"Auflösung\" ist offen\"}\n```",
			want:    "after object key:value pair",
		},
		{
			name:    "trailing comma",
			payload: "```json\n{\"winner\":\"A\",\"reasoning\":\"ok\",}\n```",
			want:    "looking for beginning of object key string",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := unmarshalStrict(tc.payload, &v)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), "truncated JSON") {
				t.Errorf("reported as truncation, which it is not: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not name the cause %q: %v", tc.want, err)
			}
		})
	}
}

// The preview must reach far enough into a realistic judge reply to show the
// offending character, and must cut on runes: a byte cut through German prose
// renders as an escape exactly where the reader is looking.
func TestUnmarshalStrict_PreviewIsWideAndRuneSafe(t *testing.T) {
	var v struct {
		Winner string `json:"winner"`
	}
	// The invalid byte sits ~200 runes in — past the old 120-BYTE preview.
	payload := "```json\n{\"winner\":\"A\",\"reasoning\":\"" + strings.Repeat("ä", 200) + "\nzweite Zeile\"}\n```"
	err := unmarshalStrict(payload, &v)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "in string literal") {
		t.Errorf("error does not name the cause: %v", err)
	}
	if !strings.Contains(err.Error(), strings.Repeat("ä", 100)) {
		t.Errorf("preview is too narrow or byte-cut mid-rune: %v", err)
	}
}

func TestUnmarshalStrict_ProseWrappedObjectStillParses(t *testing.T) {
	// Pre-existing tolerance (the RAGAS sampler relies on it): an object
	// embedded in prose must keep parsing.
	var v struct {
		Score float64 `json:"score"`
	}
	if err := unmarshalStrict("Here is my verdict:\n{\"score\":4}\nHope that helps.", &v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Score != 4 {
		t.Errorf("expected 4, got %v", v.Score)
	}
}

// --- the two judges that failed live, end to end ---

func TestPairwiseVerdict_FencedAndUnfencedAgree(t *testing.T) {
	fencedWinner, _, err := parsePairwiseVerdict("```json\n{\"winner\":\"B\",\"reasoning\":\"besser\"}\n```")
	if err != nil {
		t.Fatalf("fenced: unexpected error: %v", err)
	}
	plainWinner, _, err := parsePairwiseVerdict(`{"winner":"B","reasoning":"besser"}`)
	if err != nil {
		t.Fatalf("plain: unexpected error: %v", err)
	}
	if fencedWinner != plainWinner || fencedWinner != WinnerB {
		t.Errorf("fenced %q vs plain %q, want %q", fencedWinner, plainWinner, WinnerB)
	}
}

func TestPairwiseVerdict_TruncatedResponseReportsTruncation(t *testing.T) {
	_, _, err := parsePairwiseVerdict("```json\n{\"winner\":\"A\",\"reasoning\":\"Antwort A ist deutlich besser, da sie")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "truncated JSON") {
		t.Errorf("expected truncated-JSON error, got %v", err)
	}
}

func TestCoverage_FencedResponseParses(t *testing.T) {
	j := NewJudge(&scriptedCompleter{responses: []string{"```json\n{\"covered\":[true,false]}\n```"}})
	q := Question{ID: "q", Question: "why?", Language: "en", ExpectedPoints: []string{"p1", "p2"}}

	got, warnings, err := j.coverage(context.Background(), q, "answer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if got != 0.5 {
		t.Errorf("expected 0.5, got %v", got)
	}
}
