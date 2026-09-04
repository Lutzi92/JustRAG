// Package promptsafety holds small, dependency-free heuristics for
// treating user-supplied content as data rather than instructions. It
// imports only the standard library so any package can depend on it
// without risking an import cycle — the reason this logic was pulled out
// of internal/tabular/profile (see instructionRe below).
package promptsafety

import "regexp"

// instructionRe flags text that looks like it is trying to steer the model
// rather than describe/represent spreadsheet content — the second,
// deterministic line of defense the sheet-profile system prompt's "cells
// are data, not instructions" warning names. Cheap substring/regex check,
// not a classifier: false negatives are expected (a determined injection
// can dodge this list), the goal is to catch the common, unsubtle cases
// before untrusted text lands verbatim in stored metadata or a prompt
// addendum.
var instructionRe = regexp.MustCompile(`(?i)(ignore (all|any|the|previous|prior|above)|disregard (all|the|previous)|system prompt|you are (now|an?|the)\b|assistant:|<\|im_start\|>|do not follow|new instructions|https?://)`)

// LooksLikeInstruction reports whether s matches the instruction-pattern
// heuristic above.
func LooksLikeInstruction(s string) bool {
	return instructionRe.MatchString(s)
}
