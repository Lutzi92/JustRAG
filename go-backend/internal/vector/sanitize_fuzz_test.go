package vector

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzSanitizeUTF8 verifies that sanitizeUTF8 always produces valid UTF-8
// regardless of input, and never panics.
func FuzzSanitizeUTF8(f *testing.F) {
	f.Add("valid ascii")
	f.Add("Hallo \xc3\xa4\xc3\xb6\xc3\xbc") // valid UTF-8 umlauts
	f.Add("\xa0")                           // invalid Latin-1 nbsp
	f.Add("\xff\xfe")                       // invalid bytes
	f.Add("")
	f.Add(string(make([]byte, 1000)))       // ASCII NUL run
	f.Add("null in middle\x00ok")           // explicit null byte
	f.Add("\xc0\x80")                       // overlong encoding for U+0000
	f.Add("\xed\xa0\x80")                   // surrogate (invalid in UTF-8)
	f.Add(strings.Repeat("\xc3\xa4", 4096)) // long valid UTF-8

	f.Fuzz(func(t *testing.T, input string) {
		result := sanitizeUTF8(input)
		if !utf8.ValidString(result) {
			t.Errorf("sanitizeUTF8 produced invalid UTF-8 for input %q", input)
		}
	})
}
