package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/justrag/go-backend/internal/eval"
)

// --pairwise-pool: pool two (or more) finished --pairwise-out JSONs into one
// win/tie/loss tally with the rates and the Wilson interval recomputed on
// the pooled counts (ruling W5-R1, 2026-09-06).
//
// This mode reads only files. It runs no retrieval, calls no judge, opens no
// database and needs no AI provider — which is why it short-circuits before
// --golden AND before the --pairwise-a/--pairwise-b mode's config load.
//
// Like --pairwise-a/-b it always exits 0 on a completed pooling: it is a
// measurement instrument, not a gate. Only an unusable invocation (fewer
// than two paths, an unreadable file, a file that is not a pairwise result)
// is an error.

// minPoolInputs is the smallest number of pairwise results worth pooling.
// One result pooled with nothing is just that result, and W5-R1 is stated on
// the two cross pairs, so a single path is far more likely to be a typo than
// an intention.
const minPoolInputs = 2

// validatePairwisePoolFlags checks the invocation shape. The paths arrive as
// the flag value plus the residual positional arguments, so a caller who
// wrote `--pairwise-pool a.json` (forgetting b.json) or who let an unrelated
// trailing argument slip in must be told rather than silently pooled.
//
// The dash check is the one that earns its keep. Go's flag package stops
// parsing at the FIRST non-flag argument, so in
//
//	--pairwise-pool pw1.json pw2.json --pairwise-out pooled.json
//
// everything from pw2.json on is positional: `--pairwise-out` arrives here as
// a third "path" and `pooled.json` as a fourth, while the real --pairwise-out
// flag stays empty and no JSON is ever written. Without this check that
// misparse surfaces as a baffling "open --pairwise-out: no such file". Any
// other flag must therefore come BEFORE the positional paths.
func validatePairwisePoolFlags(paths []string) error {
	if len(paths) < minPoolInputs {
		return fmt.Errorf("--pairwise-pool needs at least %d pairwise result files (usage: --pairwise-pool a.json b.json)", minPoolInputs)
	}
	for i, p := range paths {
		if p == "" {
			return fmt.Errorf("--pairwise-pool: argument %d is empty", i+1)
		}
		if strings.HasPrefix(p, "-") {
			return fmt.Errorf("--pairwise-pool: argument %d is %q, which looks like a flag; every other flag must come BEFORE the pooled paths (e.g. --pairwise-out pooled.json --pairwise-pool a.json b.json), because flag parsing stops at the first positional argument", i+1, p)
		}
	}
	return nil
}

// readPairwiseResultFile loads one --pairwise-out JSON.
//
// It insists on the "pairs" key being present rather than just unmarshalling
// into the struct: every PairwiseResult writes that key (it carries no
// omitempty), while an eval Report — the other JSON an operator has lying
// around in the same directory — does not. Without the check, handing this
// mode a run report would decode into an all-zero result and pool silently.
func readPairwiseResultFile(path string) (*eval.PairwiseResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pairwise result: %w", err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("%s: not a JSON object: %w", path, err)
	}
	if _, ok := probe["pairs"]; !ok {
		return nil, fmt.Errorf("%s: not a --pairwise-out result (no \"pairs\" key); pass the pairwise JSON, not the eval report", path)
	}
	var res eval.PairwiseResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &res, nil
}

// pairwisePoolReport is the machine-readable output of --pairwise-pool.
// A and B are the same pooled tally seen from each side; both are written
// because the rule a reader is checking may be stated in either direction
// (W5-R1 is stated from map_reduce's = B's view).
type pairwisePoolReport struct {
	Inputs   []pairwisePoolInput            `json:"inputs"`
	Decisive int                            `json:"decisive"`
	A        eval.PairwiseCounts            `json:"a"`
	B        eval.PairwiseCounts            `json:"b"`
	ByRoute  map[string]eval.PairwiseCounts `json:"by_route,omitempty"`
}

type pairwisePoolInput struct {
	Path    string  `json:"path"`
	Wins    int     `json:"wins"`
	Ties    int     `json:"ties"`
	Losses  int     `json:"losses"`
	WinRate float64 `json:"win_rate"`
	Skipped int     `json:"skipped"`
	Errors  int     `json:"errors"`
}

// buildPairwisePoolReport pools the results and assembles the report. Split
// out from the I/O so the arithmetic is testable without touching disk.
func buildPairwisePoolReport(paths []string, results []*eval.PairwiseResult) pairwisePoolReport {
	pooled := eval.PoolPairwise(results...)
	rep := pairwisePoolReport{
		Decisive: pooled.Wins + pooled.Losses,
		A:        pooled,
		B:        eval.MirrorCounts(pooled),
		ByRoute:  eval.PoolPairwiseByRoute(results...),
	}
	for i, res := range results {
		in := pairwisePoolInput{Path: paths[i]}
		if res != nil {
			in.Wins, in.Ties, in.Losses = res.Wins, res.Ties, res.Losses
			in.WinRate, in.Skipped, in.Errors = res.WinRate, res.Skipped, res.Errors
		}
		rep.Inputs = append(rep.Inputs, in)
	}
	if len(rep.ByRoute) == 0 {
		rep.ByRoute = nil
	}
	return rep
}

// writePairwisePoolSummary renders the human-readable output.
//
// It names the perspective on every line it prints. The pooled counts are in
// side A's terms (that is how each input result is expressed), and the B-side
// win rate with its own Wilson interval is printed right underneath, because
// the long-context rule W5-R1 is stated from map_reduce's — B's — view and
// the Wilson bounds do not merely swap when the perspective flips, they
// reflect (low_B = 1 - high_A).
func writePairwisePoolSummary(w io.Writer, rep pairwisePoolReport) error {
	if _, err := fmt.Fprintf(w, `Pooled pairwise preference (ruling W5-R1: rates and Wilson recomputed on the POOLED decisive pairs)
  inputs pooled  = %d
  decisive pairs = %d  (ties excluded from every rate, per W4-R4)

NOTE: pooling assumes every input assigned the SAME configuration to side A.
      A pairwise JSON carries no report paths, so this cannot be checked here.

Pooled, from side A's view:
  A wins   = %d
  ties     = %d  (includes pairs the judge flipped on)
  B wins   = %d
  win rate = %.3f  (A over B)  95%% Wilson [%.3f, %.3f]
  tie rate = %.3f

Pooled, from side B's view (the direction W5-R1 is stated in):
  win rate = %.3f  (B over A)  95%% Wilson [%.3f, %.3f]
`,
		len(rep.Inputs), rep.Decisive,
		rep.A.Wins, rep.A.Ties, rep.A.Losses,
		rep.A.WinRate, rep.A.WilsonLow, rep.A.WilsonHigh, rep.A.TieRate,
		rep.B.WinRate, rep.B.WilsonLow, rep.B.WilsonHigh,
	); err != nil {
		return err
	}

	fmt.Fprintf(w, "\nPer input (side A's view):\n  %-44s %5s %5s %5s %9s %9s\n", "file", "winA", "tie", "winB", "win_rate", "skipped")
	for _, in := range rep.Inputs {
		fmt.Fprintf(w, "  %-44s %5d %5d %5d %9.3f %9d\n", in.Path, in.Wins, in.Ties, in.Losses, in.WinRate, in.Skipped)
	}

	if len(rep.ByRoute) > 0 {
		routes := make([]string, 0, len(rep.ByRoute))
		for r := range rep.ByRoute {
			routes = append(routes, r)
		}
		sort.Strings(routes)
		fmt.Fprintf(w, "\nPer route (pooled, side A's view):\n  %-20s %5s %5s %5s %9s %12s\n", "route", "winA", "tie", "winB", "win_rate", "wilson_95")
		for _, r := range routes {
			c := rep.ByRoute[r]
			fmt.Fprintf(w, "  %-20s %5d %5d %5d %9.3f  [%.3f, %.3f]\n", r, c.Wins, c.Ties, c.Losses, c.WinRate, c.WilsonLow, c.WilsonHigh)
		}
	}
	return nil
}

// writePairwisePoolJSON writes the pooled report to path.
func writePairwisePoolJSON(path string, rep pairwisePoolReport) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}

// runPairwisePoolMode is the entrypoint for --pairwise-pool. Returns an error
// rather than exiting so main() owns the exit code.
func runPairwisePoolMode(paths []string, outPath string, out io.Writer) error {
	results := make([]*eval.PairwiseResult, 0, len(paths))
	for _, p := range paths {
		res, err := readPairwiseResultFile(p)
		if err != nil {
			return err
		}
		results = append(results, res)
	}
	rep := buildPairwisePoolReport(paths, results)
	if err := writePairwisePoolSummary(out, rep); err != nil {
		return err
	}
	if outPath != "" {
		if err := writePairwisePoolJSON(outPath, rep); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nJSON result: %s\n", outPath)
	}
	return nil
}
