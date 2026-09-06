package eval

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
)

// --- fakes -----------------------------------------------------------------

// pairFakeCompleter answers judge prompts from a function so a test can decide
// per prompt (and therefore per answer ORDER) what the judge says.
type pairFakeCompleter struct {
	fn    func(prompt string) (string, error)
	calls int
}

func (f *pairFakeCompleter) Complete(_ context.Context, prompt, _ string) (string, error) {
	f.calls++
	return f.fn(prompt)
}

// firstOf reports whether x appears before y in prompt. The pairwise judge
// renders the two answers into one prompt, so this is how a fake decides
// which side it was handed as "A".
func firstOf(prompt, x, y string) bool {
	ix, iy := strings.Index(prompt, x), strings.Index(prompt, y)
	if ix < 0 || iy < 0 {
		return false
	}
	return ix < iy
}

// contentJudge is a position-UNBIASED fake: whichever slot carries
// winnerText wins, so both orderings agree.
func contentJudge(winnerText, loserText string) func(string) (string, error) {
	return func(prompt string) (string, error) {
		if firstOf(prompt, winnerText, loserText) {
			return `{"winner":"A","reasoning":"winner is in slot A"}`, nil
		}
		return `{"winner":"B","reasoning":"winner is in slot B"}`, nil
	}
}

// pairwiseReport builds a judged Report from (id, route, answer) triples.
func pairwiseReport(rows ...[3]string) *Report {
	rep := &Report{}
	for _, r := range rows {
		qr := QuestionReport{
			Question: Question{ID: r[0], Question: "Wie funktioniert X?", QueryType: r[1], Language: "de"},
		}
		if r[2] != "" {
			qr.Judge = &JudgeMetrics{Answer: r[2]}
		} else {
			qr.Judge = &JudgeMetrics{}
		}
		rep.Questions = append(rep.Questions, qr)
	}
	return rep
}

func findPair(t *testing.T, res *PairwiseResult, id string) PairVerdict {
	t.Helper()
	for _, p := range res.Pairs {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no pair verdict for id %q (pairs: %+v)", id, res.Pairs)
	return PairVerdict{}
}

// --- (a) both orders agree -------------------------------------------------

func TestRunPairwise_BothOrdersAgree(t *testing.T) {
	a := pairwiseReport([3]string{"Q1", "lookup", "ALPHA answer"})
	b := pairwiseReport([3]string{"Q1", "lookup", "BETA answer"})

	fc := &pairFakeCompleter{fn: contentJudge("ALPHA", "BETA")}
	res, err := RunPairwise(context.Background(), fc, a, b, "de")
	if err != nil {
		t.Fatalf("RunPairwise: %v", err)
	}

	if fc.calls != 2 {
		t.Errorf("judge calls = %d, want 2 (one per position order)", fc.calls)
	}
	p := findPair(t, res, "Q1")
	if p.Winner != WinnerA {
		t.Errorf("Winner = %q, want %q", p.Winner, WinnerA)
	}
	if !p.AgreeBothOrders {
		t.Error("AgreeBothOrders = false, want true")
	}
	if p.ReasoningAB == "" || p.ReasoningBA == "" {
		t.Errorf("reasoning not captured for both orders: AB=%q BA=%q", p.ReasoningAB, p.ReasoningBA)
	}
	if res.Wins != 1 || res.Losses != 0 || res.Ties != 0 {
		t.Errorf("counts = wins %d / ties %d / losses %d, want 1/0/0", res.Wins, res.Ties, res.Losses)
	}
	if res.ByRoute["lookup"].Wins != 1 {
		t.Errorf("ByRoute[lookup].Wins = %d, want 1", res.ByRoute["lookup"].Wins)
	}
}

// --- (b) orders disagree → tie (this is the position debias) ---------------

// TestRunPairwise_PositionBiasedJudgeYieldsTie is the mutation guard for the
// position swap: a judge that always picks whatever sits in slot A must not
// be able to produce a win. Remove the swap in RunPairwise and this test
// reports Winner="A".
func TestRunPairwise_PositionBiasedJudgeYieldsTie(t *testing.T) {
	a := pairwiseReport([3]string{"Q1", "complex_reasoning", "ALPHA answer"})
	b := pairwiseReport([3]string{"Q1", "complex_reasoning", "BETA answer"})

	alwaysSlotA := &pairFakeCompleter{fn: func(string) (string, error) {
		return `{"winner":"A","reasoning":"first one looks nicer"}`, nil
	}}
	res, err := RunPairwise(context.Background(), alwaysSlotA, a, b, "de")
	if err != nil {
		t.Fatalf("RunPairwise: %v", err)
	}

	p := findPair(t, res, "Q1")
	if p.Winner != WinnerTie {
		t.Errorf("Winner = %q, want %q — a position-biased judge must not win a pair", p.Winner, WinnerTie)
	}
	if p.AgreeBothOrders {
		t.Error("AgreeBothOrders = true, want false")
	}
	if res.Ties != 1 || res.Wins != 0 || res.Losses != 0 {
		t.Errorf("counts = wins %d / ties %d / losses %d, want 0/1/0", res.Wins, res.Ties, res.Losses)
	}
}

// --- (c) empty answer on one side → skipped with a note --------------------

func TestRunPairwise_EmptyAnswerSkipped(t *testing.T) {
	a := pairwiseReport([3]string{"Q1", "lookup", "ALPHA answer"})
	b := pairwiseReport([3]string{"Q1", "lookup", ""})

	fc := &pairFakeCompleter{fn: contentJudge("ALPHA", "BETA")}
	res, err := RunPairwise(context.Background(), fc, a, b, "de")
	if err != nil {
		t.Fatalf("RunPairwise: %v", err)
	}

	if fc.calls != 0 {
		t.Errorf("judge calls = %d, want 0 — a pair with a missing answer must not be judged", fc.calls)
	}
	p := findPair(t, res, "Q1")
	if p.Winner != WinnerSkipped {
		t.Errorf("Winner = %q, want %q", p.Winner, WinnerSkipped)
	}
	if p.Note == "" {
		t.Error("Note is empty, want an explanation why the pair was skipped")
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", res.Skipped)
	}
	if res.Wins+res.Ties+res.Losses != 0 {
		t.Errorf("a skipped pair must not be counted: wins %d / ties %d / losses %d", res.Wins, res.Ties, res.Losses)
	}
}

// --- (d) Wilson interval for 8/10 wins -------------------------------------

func TestRunPairwise_WilsonIntervalEightOfTen(t *testing.T) {
	var rowsA, rowsB [][3]string
	for i := 1; i <= 10; i++ {
		rowsA = append(rowsA, [3]string{fmt.Sprintf("Q%d", i), "lookup", fmt.Sprintf("ALPHA-%d", i)})
		rowsB = append(rowsB, [3]string{fmt.Sprintf("Q%d", i), "lookup", fmt.Sprintf("BETA-%d", i)})
	}
	a, b := pairwiseReport(rowsA...), pairwiseReport(rowsB...)

	// A wins Q1..Q8, B wins Q9..Q10 — consistently in both orders.
	fc := &pairFakeCompleter{fn: func(prompt string) (string, error) {
		aWins := true
		for _, id := range []string{"9", "10"} {
			if strings.Contains(prompt, "ALPHA-"+id) {
				aWins = false
			}
		}
		alphaFirst := strings.Index(prompt, "ALPHA-") < strings.Index(prompt, "BETA-")
		winner := WinnerB
		if aWins == alphaFirst {
			winner = WinnerA
		}
		return fmt.Sprintf(`{"winner":%q,"reasoning":"r"}`, winner), nil
	}}

	res, err := RunPairwise(context.Background(), fc, a, b, "de")
	if err != nil {
		t.Fatalf("RunPairwise: %v", err)
	}
	if res.Wins != 8 || res.Losses != 2 || res.Ties != 0 {
		t.Fatalf("counts = wins %d / ties %d / losses %d, want 8/0/2", res.Wins, res.Ties, res.Losses)
	}
	if math.Abs(res.WinRate-0.8) > 1e-9 {
		t.Errorf("WinRate = %.6f, want 0.8", res.WinRate)
	}
	// Hand-computed at z = 1.96: 0.4902 .. 0.9433.
	if math.Abs(res.WilsonLow-0.490167) > 5e-4 {
		t.Errorf("WilsonLow = %.6f, want ~0.4902", res.WilsonLow)
	}
	if math.Abs(res.WilsonHigh-0.943320) > 5e-4 {
		t.Errorf("WilsonHigh = %.6f, want ~0.9433", res.WilsonHigh)
	}
}

// --- (e) questions present in only one report ------------------------------

func TestRunPairwise_UnmatchedQuestionsSkippedAndCounted(t *testing.T) {
	a := pairwiseReport(
		[3]string{"Q1", "lookup", "ALPHA answer"},
		[3]string{"Q2", "lookup", "ALPHA only-in-a"},
	)
	b := pairwiseReport(
		[3]string{"Q1", "lookup", "BETA answer"},
		[3]string{"Q3", "lookup", "BETA only-in-b"},
	)

	fc := &pairFakeCompleter{fn: contentJudge("ALPHA", "BETA")}
	res, err := RunPairwise(context.Background(), fc, a, b, "de")
	if err != nil {
		t.Fatalf("RunPairwise: %v", err)
	}

	if len(res.Pairs) != 1 || res.Pairs[0].ID != "Q1" {
		t.Fatalf("Pairs = %+v, want exactly the matched Q1", res.Pairs)
	}
	if len(res.OnlyInA) != 1 || res.OnlyInA[0] != "Q2" {
		t.Errorf("OnlyInA = %v, want [Q2]", res.OnlyInA)
	}
	if len(res.OnlyInB) != 1 || res.OnlyInB[0] != "Q3" {
		t.Errorf("OnlyInB = %v, want [Q3]", res.OnlyInB)
	}
	if res.Wins != 1 {
		t.Errorf("Wins = %d, want 1", res.Wins)
	}
}

// --- tolerant winner parsing -----------------------------------------------

func TestParsePairwiseWinner_Tolerant(t *testing.T) {
	cases := map[string]string{
		`{"winner":"A"}`:      WinnerA,
		`{"winner":"a"}`:      WinnerA,
		`{"winner":"1"}`:      WinnerA,
		`{"winner":1}`:        WinnerA,
		`{"winner":"B"}`:      WinnerB,
		`{"winner":" b "}`:    WinnerB,
		`{"winner":"2"}`:      WinnerB,
		`{"winner":2}`:        WinnerB,
		`{"winner":"tie"}`:    WinnerTie,
		`{"winner":"TIE"}`:    WinnerTie,
		`{"winner":"Draw"}`:   WinnerTie,
		`{"winner":"gleich"}`: WinnerTie,
	}
	for in, want := range cases {
		got, _, err := parsePairwiseVerdict(in)
		if err != nil {
			t.Errorf("parsePairwiseVerdict(%s): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parsePairwiseVerdict(%s) = %q, want %q", in, got, want)
		}
	}
	if _, _, err := parsePairwiseVerdict(`{"winner":"maybe C"}`); err == nil {
		t.Error("expected an error for an unrecognised winner value")
	}
	if _, _, err := parsePairwiseVerdict(`not json at all`); err == nil {
		t.Error("expected an error for a non-JSON response")
	}
}

// TestRunPairwise_JudgeErrorSkipsPair keeps a transport failure from being
// silently scored as a tie (which would dilute the win rate toward 0.5).
func TestRunPairwise_JudgeErrorSkipsPair(t *testing.T) {
	a := pairwiseReport([3]string{"Q1", "", "ALPHA answer"})
	b := pairwiseReport([3]string{"Q1", "", "BETA answer"})

	fc := &pairFakeCompleter{fn: func(string) (string, error) { return "", fmt.Errorf("backend down") }}
	res, err := RunPairwise(context.Background(), fc, a, b, "de")
	if err != nil {
		t.Fatalf("RunPairwise: %v", err)
	}
	p := findPair(t, res, "Q1")
	if p.Winner != WinnerSkipped || p.Note == "" {
		t.Errorf("verdict = %+v, want a skipped pair with a note", p)
	}
	if res.Errors != 1 {
		t.Errorf("Errors = %d, want 1", res.Errors)
	}
	if res.Wins+res.Ties+res.Losses != 0 {
		t.Errorf("an errored pair must not be counted: %d/%d/%d", res.Wins, res.Ties, res.Losses)
	}
	if p.Route != RouteUnclassified {
		t.Errorf("Route = %q, want %q for an unlabeled question", p.Route, RouteUnclassified)
	}
}
