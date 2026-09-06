package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/eval"
)

func TestValidatePairwisePoolFlags(t *testing.T) {
	if err := validatePairwisePoolFlags([]string{"pw1.json", "pw2.json"}); err != nil {
		t.Errorf("two paths: unexpected error %v", err)
	}
	for _, tc := range []struct {
		name  string
		paths []string
	}{
		{"none", nil},
		{"one", []string{"pw1.json"}},
		{"empty member", []string{"pw1.json", ""}},
		// Go's flag package stops parsing at the first positional
		// argument, so `--pairwise-pool a.json b.json --pairwise-out x`
		// delivers "--pairwise-out" and "x" as extra paths while the
		// real flag stays empty. That must be an error naming the cause,
		// not an "open --pairwise-out: no such file" further downstream.
		{"trailing flag", []string{"pw1.json", "pw2.json", "--pairwise-out", "pooled.json"}},
	} {
		err := validatePairwisePoolFlags(tc.paths)
		if err == nil {
			t.Errorf("%s: want an error, got nil", tc.name)
			continue
		}
		if tc.name == "trailing flag" && !strings.Contains(err.Error(), "BEFORE") {
			t.Errorf("trailing-flag error does not explain the ordering: %v", err)
		}
	}
}

// writePairwiseFixture writes a --pairwise-out shaped JSON so the reader is
// exercised against the real on-disk shape, not a hand-built struct.
func writePairwiseFixture(t *testing.T, dir, name string, res *eval.PairwiseResult) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := writePairwiseJSON(path, res); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
	return path
}

// TestReadPairwiseResultFile_RejectsAnEvalReport is the guard that matters:
// an eval report and a pairwise result are both JSON objects that live in the
// same output directory, and a report decodes into PairwiseResult as an
// all-zero struct. Pooling that would report "0 decisive pairs" instead of
// telling the operator they passed the wrong file.
func TestReadPairwiseResultFile_RejectsAnEvalReport(t *testing.T) {
	dir := t.TempDir()
	ok := writePairwiseFixture(t, dir, "pw.json", &eval.PairwiseResult{
		Pairs: []eval.PairVerdict{{ID: "G01", Route: "global_synthesis", Winner: eval.WinnerB}},
		Wins:  0, Ties: 0, Losses: 1,
	})
	res, err := readPairwiseResultFile(ok)
	if err != nil {
		t.Fatalf("readPairwiseResultFile: %v", err)
	}
	if res.Losses != 1 {
		t.Errorf("Losses = %d, want 1", res.Losses)
	}

	reportPath := filepath.Join(dir, "report.json")
	var buf bytes.Buffer
	if err := eval.WriteJSONReport(&buf, eval.Report{Questions: []eval.QuestionReport{
		{Question: eval.Question{ID: "G01", KbID: "kb"}},
	}}); err != nil {
		t.Fatalf("WriteJSONReport: %v", err)
	}
	if err := os.WriteFile(reportPath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readPairwiseResultFile(reportPath); err == nil {
		t.Error("an eval report was accepted as a pairwise result")
	}

	if _, err := readPairwiseResultFile(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("a missing path was accepted")
	}
}

// TestRunPairwisePoolMode_TwoCrossPairs is the end-to-end shape of the
// W5-R1 decision: two cross comparisons on disk, pooled to 20 decisive pairs
// with side B winning 16 of them — 0.800, 95 % Wilson [0.584, 0.919].
func TestRunPairwisePoolMode_TwoCrossPairs(t *testing.T) {
	dir := t.TempDir()
	pw1 := writePairwiseFixture(t, dir, "pw1.json", &eval.PairwiseResult{
		Pairs: []eval.PairVerdict{{ID: "G01", Route: "global_synthesis", Winner: eval.WinnerB}},
		Wins:  3, Ties: 1, Losses: 8, WinRate: 3.0 / 11.0,
		ByRoute: map[string]eval.PairwiseCounts{"global_synthesis": {Wins: 3, Ties: 1, Losses: 8}},
	})
	pw2 := writePairwiseFixture(t, dir, "pw2.json", &eval.PairwiseResult{
		Pairs: []eval.PairVerdict{{ID: "G01", Route: "global_synthesis", Winner: eval.WinnerB}},
		Wins:  1, Ties: 3, Losses: 8, WinRate: 1.0 / 9.0,
		ByRoute: map[string]eval.PairwiseCounts{"global_synthesis": {Wins: 1, Ties: 3, Losses: 8}},
	})

	outPath := filepath.Join(dir, "pooled.json")
	var out bytes.Buffer
	if err := runPairwisePoolMode([]string{pw1, pw2}, outPath, &out); err != nil {
		t.Fatalf("runPairwisePoolMode: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"inputs pooled  = 2",
		"decisive pairs = 20",
		"from side A's view",
		"A wins   = 4",
		"ties     = 4",
		"B wins   = 16",
		"win rate = 0.200",
		"from side B's view",
		"win rate = 0.800",
		"95% Wilson [0.584, 0.919]",
		"global_synthesis",
		"pw1.json",
		"pw2.json",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "%%") {
		t.Errorf("literal %%%% leaked into the summary:\n%s", got)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read pooled JSON: %v", err)
	}
	var round struct {
		Decisive int                            `json:"decisive"`
		A        eval.PairwiseCounts            `json:"a"`
		B        eval.PairwiseCounts            `json:"b"`
		ByRoute  map[string]eval.PairwiseCounts `json:"by_route"`
		Inputs   []struct {
			Path string `json:"path"`
			Wins int    `json:"wins"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("unmarshal pooled JSON: %v", err)
	}
	if round.Decisive != 20 {
		t.Errorf("decisive = %d, want 20", round.Decisive)
	}
	if round.A.Wins != 4 || round.A.Ties != 4 || round.A.Losses != 16 {
		t.Errorf("a = %d/%d/%d, want 4/4/16", round.A.Wins, round.A.Ties, round.A.Losses)
	}
	if math.Abs(round.B.WinRate-0.8) > 1e-9 {
		t.Errorf("b.win_rate = %.6f, want 0.8", round.B.WinRate)
	}
	if math.Abs(round.B.WilsonLow-0.583980) > 5e-4 || math.Abs(round.B.WilsonHigh-0.919344) > 5e-4 {
		t.Errorf("b Wilson = [%.6f, %.6f], want ~[0.584, 0.919]", round.B.WilsonLow, round.B.WilsonHigh)
	}
	if got := round.ByRoute["global_synthesis"]; got.Wins != 4 || got.Losses != 16 {
		t.Errorf("by_route[global_synthesis] = %+v, want 4 wins / 16 losses", got)
	}
	if len(round.Inputs) != 2 || round.Inputs[0].Wins != 3 || round.Inputs[1].Wins != 1 {
		t.Errorf("inputs = %+v, want the per-file counts in order", round.Inputs)
	}
}

// TestPairwisePoolExitCodes pins the contract main() promises: a usage error
// (one path instead of two) exits 2, a completed pooling exits 0 whichever
// side won. Only a subprocess can observe an exit code, so the test builds
// the command into its own temp dir — never over the tracked cmd/eval/eval
// binary.
func TestPairwisePoolExitCodes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the eval command")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "eval-test-bin")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	pw1 := writePairwiseFixture(t, dir, "pw1.json", &eval.PairwiseResult{Wins: 3, Ties: 1, Losses: 8})
	pw2 := writePairwiseFixture(t, dir, "pw2.json", &eval.PairwiseResult{Wins: 1, Ties: 3, Losses: 8})

	// Usage error: a single path.
	one := exec.Command(bin, "--pairwise-pool", pw1)
	if err := one.Run(); err == nil {
		t.Fatal("--pairwise-pool with one path exited 0, want a usage error")
	} else if code := one.ProcessState.ExitCode(); code != 2 {
		t.Errorf("exit code = %d, want 2 (usage error)", code)
	}

	// Completed pooling: exit 0 even though side B won 16 of 20.
	two := exec.Command(bin, "--pairwise-pool", pw1, pw2)
	out, err := two.CombinedOutput()
	if err != nil {
		t.Fatalf("--pairwise-pool with two paths: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "decisive pairs = 20") {
		t.Errorf("output missing the pooled count:\n%s", out)
	}
}
