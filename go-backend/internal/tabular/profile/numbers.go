package profile

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// dateTextLayouts are the layouts ParseDateText tries, in order.
var dateTextLayouts = []string{
	"2006-01-02",
	"02.01.2006",
	"2.1.2006",
	"02.01.06",
	"2006/01/02",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"02.01.2006 15:04",
}

// idHeaderRe matches header text that names an identifier column.
var idHeaderRe = regexp.MustCompile(`(?i)\b(nr|nummer|no|id|code|kennung|beleg|material|artikel|plz|schl(ü|ue)ssel|key|gis)\b|\bnr\.`)

// idValueRe matches ID-shaped values: digits followed by one or more
// separator-delimited segments, e.g. "01.1440.055_.10".
var idValueRe = regexp.MustCompile(`^[0-9]+([._\-/][0-9A-Za-z_]+)+$`)

// nullTokens are case-insensitive, trimmed values that mean "no value" for
// role detection purposes (not the same as an empty cell).
var nullTokens = map[string]bool{
	"n/a": true, "na": true, "k.a.": true, "k. a.": true,
	"-": true, "–": true, "—": true, "entfällt": true,
	"#n/a": true, "#nv": true, "null": true, "none": true,
}

// IsNullToken reports whether s (case-insensitive, trimmed) is a token that
// conventionally stands for "no value" (n/a, k.A., -, entfällt, #NV, …).
func IsNullToken(s string) bool {
	return nullTokens[strings.ToLower(strings.TrimSpace(s))]
}

// LooksLikeIDValue classifies a raw cell value's ID-shaped features:
// leadingZero (an all-digit string, length > 1, starting with "0"),
// longDigits (an all-digit string with more than 15 digits), and pattern
// (matches idValueRe, e.g. "01.1440.055_.10").
func LooksLikeIDValue(s string) (leadingZero, longDigits, pattern bool) {
	s = strings.TrimSpace(s)
	if isAllDigits(s) {
		leadingZero = len(s) > 1 && s[0] == '0'
		longDigits = len(s) > 15
		return
	}
	pattern = idValueRe.MatchString(s)
	return
}

// DetectDecimalComma majority-votes the decimal style (comma vs dot) among
// values that parse as a number in exactly one of the two styles. Because
// ParseNumber treats the "other" separator as a thousands grouping mark, a
// plain decimal-comma value like "12,5" actually parses under BOTH styles
// (125 vs 12.5); those ties are broken by shape: a single comma with <= 2
// trailing digits and no dot reads as a decimal comma, everything else
// (including a genuine ambiguous tie) defers to dot. Ties and an
// empty/ambiguous vote resolve to false (dot).
func DetectDecimalComma(vals []string) bool {
	comma, dot := 0, 0
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		_, okDot := ParseNumber(v, false)
		_, okComma := ParseNumber(v, true)
		switch {
		case okComma && !okDot:
			comma++
		case okDot && !okComma:
			dot++
		case okDot && okComma && strings.Contains(v, ","):
			if i := strings.LastIndex(v, ","); len(v)-i-1 <= 2 && !strings.Contains(v, ".") {
				comma++
			} else {
				dot++
			}
		}
	}
	return comma > dot
}

// ParseNumber parses s as a number. When decimalComma is true, "." is treated
// as a thousands separator (removed) and "," as the decimal point; otherwise
// "," is treated as a thousands separator (removed). Rejects strings that
// look like IDs: all-digit with a leading zero (len > 1) or more than 15
// digits.
func ParseNumber(s string, decimalComma bool) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if isAllDigits(s) && (len(s) > 15 || (len(s) > 1 && s[0] == '0')) {
		return 0, false
	}
	if decimalComma {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	} else {
		s = strings.ReplaceAll(s, ",", "")
	}
	if strings.Count(s, ".") > 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ParseDateText tries a fixed set of date/datetime layouts against s.
func ParseDateText(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 6 {
		return time.Time{}, false
	}
	for _, layout := range dateTextLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
