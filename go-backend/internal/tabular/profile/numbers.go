package profile

import (
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

// ParseNumber parses s as a number. When decimalComma is true, "." is treated
// as a thousands separator (removed) and "," as the decimal point; otherwise
// "," is treated as a thousands separator (removed). Rejects strings that
// look like IDs: all-digit with a leading zero (len > 1) or more than 15
// digits.
//
// Minimal Task-10 version; Task 11 extends behaviour without changing the
// signature.
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
//
// Minimal Task-10 version; Task 11 extends behaviour without changing the
// signature.
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
