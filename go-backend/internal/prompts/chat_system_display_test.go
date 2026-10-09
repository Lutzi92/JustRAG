package prompts

import (
	"regexp"
	"strings"
	"testing"
)

var (
	ruleLineAtStart  = regexp.MustCompile(`^\d+[a-z]?\. `)
	ruleLineIndented = regexp.MustCompile(`^\s+\d+[a-z]?\. `)
	subItemIndented  = regexp.MustCompile(`^\s+[a-e]\) `)
)

// TestChatSystemPromptDisplay_SameTextWithoutSourceIndent checks the display
// form against the raw prompt it is derived from.
//
// Oracle: the raw ChatSystemPrompt text itself, compared line by line after
// strings.TrimSpace — that comparison does not use dedentLiteral, so a
// display form that drops, reorders or rewrites a line fails it. The rule
// numbering ("1." … "15.", "11a.") is read from the raw text, not hard-coded:
// every numbered rule the literal indents must start its line in the display
// form, and the enumeration sub-items ("a)" … "e)") must stay nested.
func TestChatSystemPromptDisplay_SameTextWithoutSourceIndent(t *testing.T) {
	for _, lang := range []string{"de", "en"} {
		raw := strings.Split(ChatSystemPrompt(lang), "\n")
		disp := strings.Split(ChatSystemPromptDisplay(lang), "\n")
		if len(raw) != len(disp) {
			t.Fatalf("%s: display has %d lines, raw has %d", lang, len(disp), len(raw))
		}
		rules, subItems := 0, 0
		for i := range raw {
			if strings.TrimSpace(raw[i]) != strings.TrimSpace(disp[i]) {
				t.Fatalf("%s line %d changed:\nraw  %q\ndisp %q", lang, i, raw[i], disp[i])
			}
			if ruleLineIndented.MatchString(raw[i]) {
				rules++
				if !ruleLineAtStart.MatchString(disp[i]) {
					t.Errorf("%s line %d: numbered rule keeps indentation: %q", lang, i, disp[i])
				}
			}
			if subItemIndented.MatchString(raw[i]) {
				subItems++
				if !subItemIndented.MatchString(disp[i]) {
					t.Errorf("%s line %d: sub-item lost its nesting: %q", lang, i, disp[i])
				}
			}
		}
		// Guards against a vacuous pass if the literal is ever rewritten
		// without indentation or without the enumeration sub-items.
		if rules < 15 || subItems < 5 {
			t.Fatalf("%s: expected >=15 indented rules and >=5 sub-items in the raw prompt, got %d and %d", lang, rules, subItems)
		}
		if disp[0] != raw[0] {
			t.Errorf("%s: first line must be untouched: %q", lang, disp[0])
		}
	}
}

// TestDedentLiteral covers the shapes the helper must handle independently of
// the current prompt literal. Oracle: hand-written expected strings.
func TestDedentLiteral(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"single line", "only", "only"},
		{"four spaces", "a\n    b\n        c\n    d", "a\nb\n    c\nd"},
		{"tabs", "a\n\tb\n\t\tc", "a\nb\n\tc"},
		{"blank and whitespace-only lines ignored", "a\n\n      \n  b\n    c", "a\n\n\nb\n  c"},
		{"first line not counted", "    a\n  b\n  c", "    a\nb\nc"},
		{"no common indent", "a\nb\n  c", "a\nb\n  c"},
	} {
		if got := dedentLiteral(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
