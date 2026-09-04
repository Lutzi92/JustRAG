// Package numfmt classifies spreadsheet number-format codes. It is a leaf
// package (no internal imports) so that both sheetsource and its biffxls fork
// can share one implementation — before this package the tokenizer existed
// twice, verbatim, and a bounds bug had to be fixed in both copies.
package numfmt

import "strings"

// builtinDate holds the ECMA-376 built-in numFmtIds whose format is a date or
// time. Ranges: 14-22, 27-36, 45-47, 50-58.
var builtinDate = map[int]bool{14: true, 15: true, 16: true, 17: true, 18: true, 19: true, 20: true, 21: true, 22: true,
	27: true, 28: true, 29: true, 30: true, 31: true, 32: true, 33: true, 34: true, 35: true, 36: true,
	45: true, 46: true, 47: true, 50: true, 51: true, 52: true, 53: true, 54: true, 55: true, 56: true, 57: true, 58: true}

// IsBuiltin reports whether id is one of the built-in number formats this
// package classifies without a format code (the date/time ids plus the two
// percent ids 9 and 10).
func IsBuiltin(id int) bool { return builtinDate[id] || id == 9 || id == 10 }

// Classify reports whether a number format renders a date and/or a percentage,
// and the literal unit the format code carries ("€", "m²", "Mio.", …), empty
// when there is none.
//
// An empty code falls back to the built-in table for id. A non-empty code is
// tokenized: quoted literals and backslash escapes contribute to the unit, a
// [$CUR-LCID] section contributes its currency symbol, and the remaining
// characters decide date/percent.
func Classify(id int, code string) (date, percent bool, unit string) {
	if code == "" {
		return builtinDate[id], id == 9 || id == 10, ""
	}
	var rest, units strings.Builder
	for i := 0; i < len(code); i++ {
		switch c := code[i]; c {
		case '"':
			// j is the offset of the closing quote relative to i+1. An
			// unterminated literal runs to the end of the string.
			j := strings.IndexByte(code[i+1:], '"')
			if j < 0 {
				j = len(code) - i - 1
			}
			lit := strings.TrimSpace(code[i+1 : i+1+j])
			if lit != "" {
				units.WriteString(lit)
			}
			i += j + 1
		case '[':
			// j is the offset of the closing bracket relative to i. When ']'
			// is missing the section runs to the end of the string, so
			// j = len(code)-i; the section body is then code[i+1:], which is
			// empty for a trailing '['. Using len(code)-i-1 here (the pre-fix
			// bound) made the slice code[i+1 : i+j] run backwards and panic
			// for a code ending in '['.
			j := strings.IndexByte(code[i:], ']')
			if j < 0 {
				j = len(code) - i
			}
			if i+1 <= i+j {
				sec := code[i+1 : i+j]
				if strings.HasPrefix(sec, "$") { // [$€-407] currency section
					cur, _, _ := strings.Cut(sec[1:], "-")
					if cur = strings.TrimSpace(cur); cur != "" {
						units.WriteString(cur)
					}
				}
			}
			i += j
		case '\\':
			if i+1 < len(code) {
				e := code[i+1]
				if e != ' ' && (e < '0' || e > '9') {
					units.WriteByte(e)
				}
				i++
			}
		default:
			rest.WriteByte(c)
		}
	}
	r := strings.ToLower(rest.String())
	if strings.TrimSpace(r) == "general" {
		return false, false, strings.TrimSpace(units.String())
	}
	percent = strings.Contains(r, "%")
	date = strings.ContainsAny(r, "ydhs") || (strings.Contains(r, "m") && !strings.ContainsAny(r, "0#?"))
	return date, percent, strings.TrimSpace(units.String())
}
