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
	if de != "Aktuelles Datum: Mittwoch, 2026-07-01." {
		t.Errorf("de = %q", de)
	}
	en := CurrentDateLine("en", now)
	if en != "Current date: Wednesday, 2026-07-01." {
		t.Errorf("en = %q", en)
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
