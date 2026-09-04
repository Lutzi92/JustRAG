package profile

import (
	"testing"
)

func TestParseNumber(t *testing.T) {
	t.Parallel()
	ok := func(s string, dc bool, want float64) {
		t.Helper()
		if v, k := ParseNumber(s, dc); !k || v != want {
			t.Errorf("ParseNumber(%q,%v) = %v,%v want %v", s, dc, v, k, want)
		}
	}
	no := func(s string, dc bool) {
		t.Helper()
		if _, k := ParseNumber(s, dc); k {
			t.Errorf("ParseNumber(%q,%v) should fail", s, dc)
		}
	}
	ok("2143.28", false, 2143.28)
	ok("1,234.5", false, 1234.5)
	ok("12,5", true, 12.5)
	ok("1.234,75", true, 1234.75)
	ok("-3", false, -3)
	ok(" 42 ", true, 42)
	no("007", false)
	no("0002001919", false)
	no("1234567890123456", false)
	no("01.1440.055_.10", false)
	no("2007; Anbau 2018", false)
	no("", false)
}

func TestDetectDecimalComma(t *testing.T) {
	t.Parallel()
	if !DetectDecimalComma([]string{"12,5", "1.234,75", "3,0", "7"}) {
		t.Error("want comma")
	}
	if DetectDecimalComma([]string{"12.5", "1,234.75", "3.0"}) {
		t.Error("want dot")
	}
}

func TestParseDateTextAndNullTokens(t *testing.T) {
	t.Parallel()
	// "2025-03-14T10:00" is M6: the seconds-less ISO datetime that a
	// CSV/ODS export writes and that every other layout in the list rejects.
	for _, s := range []string{"2025-03-14", "14.03.2025", "4.3.2025", "14.03.25", "2025/03/14", "2025-03-14T10:00:00", "2025-03-14T10:00", "2025-03-14 10:00:00"} {
		if _, ok := ParseDateText(s); !ok {
			t.Errorf("ParseDateText(%q) failed", s)
		}
	}
	// The seconds-less form parses to the same instant as the full one.
	withSecs, _ := ParseDateText("2025-03-14T10:00:00")
	noSecs, ok := ParseDateText("2025-03-14T10:00")
	if !ok || !withSecs.Equal(noSecs) {
		t.Errorf("ParseDateText(\"2025-03-14T10:00\") = %v (ok=%v), want %v", noSecs, ok, withSecs)
	}
	for _, s := range []string{"03-14-25", "1972", "März 2025"} {
		if _, ok := ParseDateText(s); ok {
			t.Errorf("ParseDateText(%q) should fail", s)
		}
	}
	for _, s := range []string{"n/a", "N/A", "k.A.", "k. A.", "-", "–", "entfällt", "#NV", "#N/A", " null "} {
		if !IsNullToken(s) {
			t.Errorf("IsNullToken(%q) false", s)
		}
	}
	if IsNullToken("nein") || IsNullToken("0") {
		t.Error("false positive null token")
	}
}
