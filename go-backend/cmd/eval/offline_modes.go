package main

import (
	"log/slog"
	"os"
)

// This file holds the parts of the CLI that main() used to inline and that
// carry no state from the rest of the run: the three OFFLINE modes (they read
// files, never a golden set and never the database) and the closed-set flag
// validation. Both are pure branch weight — together they were a third of
// main's cyclomatic complexity, which the gocyclo ratchet (110) caught once
// the Wave-4/5 modes landed. Nothing here changes flag semantics, exit codes
// or ordering; the subprocess exit-code tests in pairwise_pool_test.go and the
// validator tests in pairwise_test.go / print_keyword_sql_test.go are the pin.

// offlineFlags groups the flags the offline modes read. A struct rather than a
// long parameter list so a new mode adds a field instead of shifting every
// call site's argument order.
type offlineFlags struct {
	printKeywordSQL string
	// kbID backs --kb-id, which only --print-keyword-sql reads.
	kbID string
	topK int

	pairwisePool string
	// poolExtraPaths are the positional arguments after --pairwise-pool's own
	// value (Go's flag parsing stops at the first positional argument, which
	// is why every other flag must precede them).
	poolExtraPaths []string

	pairwiseA   string
	pairwiseB   string
	pairwiseOut string
	judgeModel  string
}

// runOfflineMode runs the first requested offline mode and reports whether it
// handled the invocation, plus the exit code the caller must use (0 = return
// normally). handled is false when no offline flag was given, which is the
// normal evaluation run.
//
// The three modes short-circuit BEFORE --golden is required and before any
// config/DB setup, and they keep the precedence they had inline:
// --print-keyword-sql, then --pairwise-pool, then --pairwise-a/-b. Each
// distinguishes a usage error (exit 2) from a failed run (exit 1), and a
// completed comparison always exits 0 whichever side won — these modes
// measure, they do not gate.
func runOfflineMode(f offlineFlags) (handled bool, exitCode int) {
	switch {
	// Diagnostic mode: it needs only a KB and a query.
	case f.printKeywordSQL != "":
		if err := validateKeywordSQLFlags(f.kbID); err != nil {
			slog.Error("invalid --print-keyword-sql invocation", "error", err)
			return true, 2
		}
		if err := runPrintKeywordSQL(f.printKeywordSQL, f.kbID, f.topK, os.Stdout); err != nil {
			slog.Error("--print-keyword-sql failed", "error", err)
			return true, 1
		}
		return true, 0

	// Pooling reads finished pairwise JSONs and touches neither a golden set
	// nor the database, so it must not be gated behind the config/DB setup
	// the judging modes need.
	case f.pairwisePool != "":
		poolPaths := append([]string{f.pairwisePool}, f.poolExtraPaths...)
		if err := validatePairwisePoolFlags(poolPaths); err != nil {
			slog.Error("invalid --pairwise-pool invocation", "error", err)
			return true, 2
		}
		if err := runPairwisePoolMode(poolPaths, f.pairwiseOut, os.Stdout); err != nil {
			slog.Error("--pairwise-pool failed", "error", err)
			return true, 1
		}
		return true, 0

	// Pairwise preference mode compares two finished reports and never loads
	// a golden set.
	case f.pairwiseA != "" || f.pairwiseB != "":
		if err := validatePairwiseFlags(f.pairwiseA, f.pairwiseB); err != nil {
			slog.Error("invalid --pairwise invocation", "error", err)
			return true, 2
		}
		if err := runPairwiseMode(f.pairwiseA, f.pairwiseB, f.pairwiseOut, f.judgeModel, os.Stdout); err != nil {
			slog.Error("--pairwise failed", "error", err)
			return true, 1
		}
		return true, 0
	}
	return false, 0
}

// choiceFlag is one string flag restricted to a small closed set, with the
// empty string always meaning "not set — read the live site_config".
type choiceFlag struct {
	name    string
	value   string
	allowed []string
}

// firstInvalidChoice returns the first flag in order whose value is neither
// empty nor one of its allowed values. Order is load-bearing: with two bad
// flags on one command line, the caller reports the same one it always did.
func firstInvalidChoice(flags []choiceFlag) (choiceFlag, bool) {
	for _, f := range flags {
		if f.value == "" {
			continue
		}
		ok := false
		for _, a := range f.allowed {
			if f.value == a {
				ok = true
				break
			}
		}
		if !ok {
			return f, true
		}
	}
	return choiceFlag{}, false
}

// parseBoolChoice reads an "on" | "off" | "" three-way override flag: a
// pointer to the forced value, or nil for "" (meaning "leave the decision to
// the classifier / the site_config"). ok is false for anything else, which the
// caller turns into a usage error.
func parseBoolChoice(value string) (forced *bool, ok bool) {
	switch value {
	case "on":
		b := true
		return &b, true
	case "off":
		b := false
		return &b, true
	case "":
		return nil, true
	default:
		return nil, false
	}
}
