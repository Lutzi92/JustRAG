package prompts

import (
	"strings"
	"testing"
	"time"
)

func TestCurrentDateLine(t *testing.T) {
	// 2026-07-01 is a Wednesday.
	now := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)

	de := CurrentDateLine("de", now)
	if !strings.HasPrefix(de, "Aktuelles Datum: Mittwoch, 2026-07-01.") {
		t.Errorf("de = %q", de)
	}
	en := CurrentDateLine("en", now)
	if !strings.HasPrefix(en, "Current date: Wednesday, 2026-07-01.") {
		t.Errorf("en = %q", en)
	}
}

// Regression for the "letzte 5 Tage" bug (2026-07-02): the answer LLM knew
// the current date but failed the month-boundary subtraction (02.07 − 5 days
// = 27.06) and declared a 30.06 article outside the window. The date line
// must therefore carry precomputed day anchors so relative windows resolve
// by lookup instead of arithmetic.
func TestCurrentDateLineDayAnchors(t *testing.T) {
	// The exact failing scenario: today = Thursday 2026-07-02.
	now := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)

	de := CurrentDateLine("de", now)
	for _, want := range []string{
		"gestern = 2026-07-01",
		"vorgestern (vor 2 Tagen) = 2026-06-30",
		"vor 5 Tagen = 2026-06-27", // the boundary the model got wrong
		"vor 7 Tagen = 2026-06-25",
		"vor 14 Tagen = 2026-06-18",
		"vor 30 Tagen = 2026-06-02",
		// Calendar anchors for "diese/letzte Woche", "dieser/letzter Monat".
		"diese Woche (Mo–So) = 2026-06-29 bis 2026-07-05",
		"letzte Woche = 2026-06-22 bis 2026-06-28",
		"dieser Monat = 2026-07-01 bis 2026-07-31",
		"letzter Monat = 2026-06-01 bis 2026-06-30",
	} {
		if !strings.Contains(de, want) {
			t.Errorf("de line missing anchor %q:\n%s", want, de)
		}
	}

	en := CurrentDateLine("en", now)
	for _, want := range []string{
		"yesterday = 2026-07-01",
		"day before yesterday (2 days ago) = 2026-06-30",
		"5 days ago = 2026-06-27",
		"7 days ago = 2026-06-25",
		"14 days ago = 2026-06-18",
		"30 days ago = 2026-06-02",
		"this week (Mon–Sun) = 2026-06-29 to 2026-07-05",
		"last week = 2026-06-22 to 2026-06-28",
		"this month = 2026-07-01 to 2026-07-31",
		"last month = 2026-06-01 to 2026-06-30",
	} {
		if !strings.Contains(en, want) {
			t.Errorf("en line missing anchor %q:\n%s", want, en)
		}
	}

	// Week anchors use ISO weeks (Monday start): on a Sunday, "this week"
	// still starts the previous Monday, not today.
	sunday := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
	if got := CurrentDateLine("de", sunday); !strings.Contains(got, "diese Woche (Mo–So) = 2026-06-29 bis 2026-07-05") {
		t.Errorf("Sunday week anchor wrong:\n%s", got)
	}

	// Both lines must state the inclusive-window rule so "last N days"
	// includes the boundary date itself.
	if !strings.Contains(de, "einschließlich") {
		t.Errorf("de line missing inclusive-window rule:\n%s", de)
	}
	if !strings.Contains(en, "inclusive") {
		t.Errorf("en line missing inclusive-window rule:\n%s", en)
	}
}

func TestChatSystemPromptWithDate(t *testing.T) {
	base := ChatSystemPrompt("en")

	// Empty date line → identical to the base prompt (byte-stable).
	if got := ChatSystemPromptWithDate("en", ""); got != base {
		t.Error("empty date line must not change the prompt")
	}

	// Non-empty date line → appended.
	line := "Current date: Wednesday, 2026-07-01."
	got := ChatSystemPromptWithDate("en", line)
	if !strings.HasPrefix(got, base) || !strings.HasSuffix(got, line) {
		t.Errorf("date line not appended: %q", got)
	}
}
