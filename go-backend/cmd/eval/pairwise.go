package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"syscall"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/database"
	"github.com/justrag/go-backend/internal/eval"
)

// --pairwise-a / --pairwise-b: offline pairwise preference judging of two
// judged eval reports (ruling W4-R4).
//
// This mode runs no retrieval and generates no answers — it reads the
// `judge.answer` each report already persisted and asks the judge model
// which of the two is better, twice per pair with the positions swapped.
// It short-circuits before --golden for the same reason --print-keyword-sql
// does: there is no golden set to load, only two report files.
//
// It always exits 0 on a completed comparison, even when the B report wins
// every pair: this is a measurement instrument, not a CI gate. Only an
// unusable invocation (missing/unreadable report, no shared questions, no
// AI provider) exits non-zero.

// validatePairwiseFlags checks that the two report paths were supplied
// together. Either one alone is a typo, not a mode — and silently running
// the normal --golden path after the user asked for a comparison would
// produce a report they'd mistake for one.
func validatePairwiseFlags(pathA, pathB string) error {
	switch {
	case pathA == "" && pathB == "":
		return fmt.Errorf("--pairwise-a and --pairwise-b are both required")
	case pathA == "":
		return fmt.Errorf("--pairwise-b given without --pairwise-a")
	case pathB == "":
		return fmt.Errorf("--pairwise-a given without --pairwise-b")
	}
	return nil
}

// pairwiseKBID picks the KB the judge model is resolved against. The AI
// provider config is per-KB, so the judge needs one even though the
// comparison itself touches no KB data. Reports of the same golden set
// share a KB; the first non-empty id wins.
func pairwiseKBID(reports ...*eval.Report) string {
	for _, rep := range reports {
		if rep == nil {
			continue
		}
		for _, q := range rep.Questions {
			if q.Question.KbID != "" {
				return q.Question.KbID
			}
		}
	}
	return ""
}

// pairwiseLang is the fallback prompt language for questions whose golden
// row carries none (RunPairwise prefers the per-question Language).
func pairwiseLang(reports ...*eval.Report) string {
	for _, rep := range reports {
		if rep == nil {
			continue
		}
		for _, q := range rep.Questions {
			if q.Question.Language != "" {
				return q.Question.Language
			}
		}
	}
	return "en"
}

// readReportFile loads one cmd/eval JSON report.
func readReportFile(path string) (*eval.Report, error) {
	f, err := os.Open(path)
	if err != nil {
		// os.Open's error already names the path.
		return nil, fmt.Errorf("read report: %w", err)
	}
	defer func() { _ = f.Close() }()
	rep, err := eval.ReadJSONReport(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &rep, nil
}

// writePairwiseSummary renders the human-readable output: headline counts,
// the per-route table, and the per-question table. Stable format so a run
// log can be diffed or scraped.
func writePairwiseSummary(w io.Writer, res *eval.PairwiseResult, pathA, pathB, judgeModel string) error {
	model := judgeModel
	if model == "" {
		model = "(KB default chat model)"
	}
	judged := res.Wins + res.Ties + res.Losses
	if _, err := fmt.Fprintf(w, `Pairwise preference judge (position-debiased: every pair judged in both orders)
  report A     = %s
  report B     = %s
  judge model  = %s
  pairs judged = %d (skipped %d, judge errors %d)

Overall:
  A wins   = %d
  ties     = %d  (includes pairs the judge flipped on)
  B wins   = %d
  win rate = %.3f  (A over B, ties excluded)  95%% Wilson [%.3f, %.3f]
  tie rate = %.3f
`,
		pathA, pathB, model, judged, res.Skipped, res.Errors,
		res.Wins, res.Ties, res.Losses,
		res.WinRate, res.WilsonLow, res.WilsonHigh, res.TieRate,
	); err != nil {
		return err
	}

	if len(res.ByRoute) > 0 {
		routes := make([]string, 0, len(res.ByRoute))
		for r := range res.ByRoute {
			routes = append(routes, r)
		}
		sort.Strings(routes)
		fmt.Fprintf(w, "\nPer route:\n  %-20s %5s %5s %5s %9s %12s\n", "route", "winA", "tie", "winB", "win_rate", "wilson_95")
		for _, r := range routes {
			c := res.ByRoute[r]
			fmt.Fprintf(w, "  %-20s %5d %5d %5d %9.3f  [%.3f, %.3f]\n", r, c.Wins, c.Ties, c.Losses, c.WinRate, c.WilsonLow, c.WilsonHigh)
		}
	}

	if len(res.Pairs) > 0 {
		fmt.Fprintf(w, "\nPer question:\n  %-24s %-20s %-8s %-6s %s\n", "id", "route", "winner", "agree", "note")
		for _, p := range res.Pairs {
			agree := "no"
			if p.AgreeBothOrders {
				agree = "yes"
			}
			if p.Winner == eval.WinnerSkipped {
				agree = "-"
			}
			fmt.Fprintf(w, "  %-24s %-20s %-8s %-6s %s\n", p.ID, p.Route, p.Winner, agree, p.Note)
		}
	}

	if len(res.OnlyInA) > 0 {
		fmt.Fprintf(w, "\nOnly in A (%d): %v\n", len(res.OnlyInA), res.OnlyInA)
	}
	if len(res.OnlyInB) > 0 {
		fmt.Fprintf(w, "Only in B (%d): %v\n", len(res.OnlyInB), res.OnlyInB)
	}
	return nil
}

// writePairwiseJSON writes the machine-readable result to path.
func writePairwiseJSON(path string, res *eval.PairwiseResult) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}

// runPairwiseMode is the entrypoint for --pairwise-a/--pairwise-b. Returns
// an error rather than exiting so main() owns the exit code.
func runPairwiseMode(pathA, pathB, outPath, judgeModel string, out io.Writer) error {
	repA, err := readReportFile(pathA)
	if err != nil {
		return err
	}
	repB, err := readReportFile(pathB)
	if err != nil {
		return err
	}

	kbID := pairwiseKBID(repA, repB)
	if kbID == "" {
		return fmt.Errorf("neither report carries a kb_id — the judge model is resolved per KB")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := database.Connect(ctx, cfg.DB, cfg.VectorDB)
	if err != nil {
		return fmt.Errorf("database connect: %w", err)
	}
	defer db.Main.Close()
	if db.Vector != db.Main {
		defer db.Vector.Close()
	}

	completer := judgeCompleterAdapter{
		resolver:      ai.NewConfigResolver(ai.NewStore(db.Main)),
		kbID:          kbID,
		modelOverride: judgeModel,
	}

	res, err := eval.RunPairwise(ctx, completer, repA, repB, pairwiseLang(repA, repB))
	if err != nil {
		return err
	}
	if res.Errors > 0 {
		slog.Warn("pairwise: some pairs could not be judged", "errors", res.Errors, "skipped", res.Skipped)
	}

	if err := writePairwiseSummary(out, res, pathA, pathB, judgeModel); err != nil {
		return err
	}
	if outPath != "" {
		if err := writePairwiseJSON(outPath, res); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nJSON result: %s\n", outPath)
	}
	return nil
}
