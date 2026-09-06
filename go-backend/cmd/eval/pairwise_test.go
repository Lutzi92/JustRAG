package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/eval"
)

// TestValidatePairwiseFlags guards the CLI wiring: --pairwise-a without
// --pairwise-b must be an error, not a silent fall-through into the normal
// --golden path (which would produce a retrieval report the operator would
// mistake for a comparison).
func TestValidatePairwiseFlags(t *testing.T) {
	if err := validatePairwiseFlags("a.json", "b.json"); err != nil {
		t.Errorf("both paths given: unexpected error %v", err)
	}
	for _, tc := range []struct{ a, b, want string }{
		{"", "", "both required"},
		{"a.json", "", "without --pairwise-b"},
		{"", "b.json", "without --pairwise-a"},
	} {
		err := validatePairwiseFlags(tc.a, tc.b)
		if err == nil {
			t.Errorf("validatePairwiseFlags(%q, %q) = nil, want an error", tc.a, tc.b)
			continue
		}
		if !strings.Contains(err.Error(), "pairwise") {
			t.Errorf("error %q does not name the flags", err)
		}
	}
}

func reportWith(kbID, lang string) *eval.Report {
	return &eval.Report{Questions: []eval.QuestionReport{
		{Question: eval.Question{ID: "Q1", KbID: kbID, Language: lang}},
	}}
}

func TestPairwiseKBIDAndLang(t *testing.T) {
	empty := reportWith("", "")
	withKB := reportWith("kb-uuid", "de")

	if got := pairwiseKBID(empty, withKB); got != "kb-uuid" {
		t.Errorf("pairwiseKBID = %q, want kb-uuid (fall through to the second report)", got)
	}
	if got := pairwiseKBID(empty, empty); got != "" {
		t.Errorf("pairwiseKBID = %q, want empty so the caller can refuse the run", got)
	}
	if got := pairwiseLang(empty, withKB); got != "de" {
		t.Errorf("pairwiseLang = %q, want de", got)
	}
	if got := pairwiseLang(empty, empty); got != "en" {
		t.Errorf("pairwiseLang = %q, want the en fallback", got)
	}
}

func TestReadReportFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	buf := &bytes.Buffer{}
	if err := eval.WriteJSONReport(buf, eval.Report{Questions: []eval.QuestionReport{
		{Question: eval.Question{ID: "Q1", KbID: "kb"}, Judge: &eval.JudgeMetrics{Answer: "a"}},
	}}); err != nil {
		t.Fatalf("WriteJSONReport: %v", err)
	}
	if err := os.WriteFile(good, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	rep, err := readReportFile(good)
	if err != nil {
		t.Fatalf("readReportFile: %v", err)
	}
	if len(rep.Questions) != 1 {
		t.Errorf("questions = %d, want 1", len(rep.Questions))
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readReportFile(bad); err == nil {
		t.Error("readReportFile on an empty object: want an error (not a report)")
	}
	if _, err := readReportFile(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("readReportFile on a missing path: want an error")
	}
}

// TestWritePairwiseSummary checks the numbers a reader acts on actually
// reach the output — in particular the Wilson interval and the tie rate,
// which are the difference between "B is better" and "we cannot tell".
func TestWritePairwiseSummary(t *testing.T) {
	res := &eval.PairwiseResult{
		Pairs: []eval.PairVerdict{
			{ID: "Q1", Route: "lookup", Winner: eval.WinnerA, AgreeBothOrders: true},
			{ID: "Q2", Route: "lookup", Winner: eval.WinnerTie},
			{ID: "Q3", Route: "complex_reasoning", Winner: eval.WinnerSkipped, Note: "no judge.answer in report B"},
		},
		Wins: 1, Ties: 1, Losses: 0,
		WinRate: 1.0, TieRate: 0.5, WilsonLow: 0.2065, WilsonHigh: 1.0,
		Skipped: 1,
		ByRoute: map[string]eval.PairwiseCounts{
			"lookup": {Wins: 1, Ties: 1, WinRate: 1.0, TieRate: 0.5, WilsonLow: 0.2065, WilsonHigh: 1.0},
		},
		OnlyInA: []string{"Q9"},
	}

	var out bytes.Buffer
	if err := writePairwiseSummary(&out, res, "A.json", "B.json", "gemma-4-26b"); err != nil {
		t.Fatalf("writePairwiseSummary: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"A.json", "B.json", "gemma-4-26b",
		"pairs judged = 2 (skipped 1, judge errors 0)",
		"win rate = 1.000",
		"95% Wilson [0.206, 1.000]",
		"tie rate = 0.500",
		"lookup",
		"complex_reasoning",
		"no judge.answer in report B",
		"Only in A (1): [Q9]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "%%") {
		t.Errorf("literal %%%% leaked into the summary:\n%s", got)
	}
}

func TestWritePairwiseJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairwise.json")
	res := &eval.PairwiseResult{
		Pairs:   []eval.PairVerdict{{ID: "Q1", Route: "lookup", Winner: eval.WinnerA, AgreeBothOrders: true, ReasoningAB: "ab", ReasoningBA: "ba"}},
		Wins:    1,
		WinRate: 1.0,
	}
	if err := writePairwiseJSON(path, res); err != nil {
		t.Fatalf("writePairwiseJSON: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var round eval.PairwiseResult
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(round.Pairs) != 1 || round.Pairs[0].ReasoningBA != "ba" {
		t.Errorf("round-trip lost the per-pair reasoning: %+v", round.Pairs)
	}
	if round.Wins != 1 || round.WinRate != 1.0 {
		t.Errorf("round-trip lost the counts: %+v", round)
	}
}
