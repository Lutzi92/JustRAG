package chat

import (
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Prompt Security: Validation and sanitization for LLM inputs
// Prevents prompt injection attacks where users try to manipulate system
// instructions.
// ---------------------------------------------------------------------------

// PromptValidationResult holds the outcome of prompt input validation.
type PromptValidationResult struct {
	IsValid   bool
	Sanitized string
	Warnings  []string
}

// MaxMessageLength caps user-supplied chat message bodies. Mirrored by the
// fast-path length check in http_send.go so both gates use the same value.
const MaxMessageLength = 32000

// Context-dependent maximum input lengths.
var maxLengths = map[string]int{
	"topic":   500,
	"query":   1000,
	"message": MaxMessageLength,
}

// ---------------------------------------------------------------------------
// Unicode confusable mappings (homoglyphs → ASCII)
// ---------------------------------------------------------------------------

var confusableMap = map[rune]string{
	// Cyrillic
	'а': "a", 'е': "e", 'о': "o", 'р': "p", 'с': "c", 'у': "y", 'х': "x",
	// Greek
	'ɑ': "a", 'ε': "e", 'ι': "i", 'ο': "o", 'υ': "u",
	// Fullwidth Latin
	'ａ': "a", 'ｂ': "b", 'ｃ': "c", 'ｄ': "d", 'ｅ': "e", 'ｆ': "f", 'ｇ': "g",
	'ｈ': "h", 'ｉ': "i", 'ｊ': "j", 'ｋ': "k", 'ｌ': "l", 'ｍ': "m", 'ｎ': "n",
	'ｏ': "o", 'ｐ': "p", 'ｑ': "q", 'ｒ': "r", 'ｓ': "s", 'ｔ': "t", 'ｕ': "u",
	'ｖ': "v", 'ｗ': "w", 'ｘ': "x", 'ｙ': "y", 'ｚ': "z",
	// Fullwidth digits
	'０': "0", '１': "1", '２': "2", '３': "3", '４': "4",
	'５': "5", '６': "6", '７': "7", '８': "8", '９': "9",
	// Math bold (𝐚-𝐳)
	'𝐚': "a", '𝐛': "b", '𝐜': "c", '𝐝': "d", '𝐞': "e", '𝐟': "f", '𝐠': "g",
	'𝐡': "h", '𝐢': "i", '𝐣': "j", '𝐤': "k", '𝐥': "l", '𝐦': "m", '𝐧': "n",
	'𝐨': "o", '𝐩': "p", '𝐪': "q", '𝐫': "r", '𝐬': "s", '𝐭': "t", '𝐮': "u",
	'𝐯': "v", '𝐰': "w", '𝐱': "x", '𝐲': "y", '𝐳': "z",
	// Math italic (𝑎-𝑧)
	'𝑎': "a", '𝑏': "b", '𝑐': "c", '𝑑': "d", '𝑒': "e", '𝑓': "f", '𝑔': "g",
	'𝒉': "h", '𝑖': "i", '𝑗': "j", '𝑘': "k", '𝑙': "l", '𝑚': "m", '𝑛': "n",
	'𝑜': "o", '𝑝': "p", '𝑞': "q", '𝑟': "r", '𝑠': "s", '𝑡': "t", '𝑢': "u",
	'𝑣': "v", '𝑤': "w", '𝑥': "x", '𝑦': "y", '𝑧': "z",
	// Roman numerals
	'ⅰ': "i", 'ⅱ': "ii", 'ⅲ': "iii", 'ⅳ': "iv", 'ⅴ': "v",
	'ⅵ': "vi", 'ⅶ': "vii", 'ⅷ': "viii", 'ⅸ': "ix", 'ⅹ': "x",
	// Zero-width / invisible (mapped to empty string — stripped)
	'\u200b': "", '\u200c': "", '\u200d': "", '\ufeff': "", '\u00ad': "",
}

func normalizeConfusables(input string) string {
	var b strings.Builder
	b.Grow(len(input))
	for _, r := range input {
		if repl, ok := confusableMap[r]; ok {
			b.WriteString(repl)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Known injection patterns
// ---------------------------------------------------------------------------

var injectionPatterns = []*regexp.Regexp{
	// Direct instruction override
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?(previous|above|prior)\s+instructions?`),
	regexp.MustCompile(`(?i)disregard\s+(all\s+)?(previous|above|prior)\s+instructions?`),
	regexp.MustCompile(`(?i)forget\s+(all\s+)?(previous|above|prior)\s+instructions?`),

	// System prompt manipulation
	regexp.MustCompile(`(?i)new\s+instructions?:`),
	regexp.MustCompile(`(?i)system\s*:\s*\S`),
	regexp.MustCompile(`(?i)\[system\]`),
	regexp.MustCompile(`(?i)<system>`),

	// Role manipulation
	regexp.MustCompile(`(?i)you\s+are\s+now\s+a`),
	regexp.MustCompile(`(?i)act\s+as\s+(a\s+)?different`),
	regexp.MustCompile(`(?i)pretend\s+(you\s+are|to\s+be)\s+(a\s+)?`),
	regexp.MustCompile(`(?i)simulate\s+(being|a)\s+`),

	// Delimiter attacks
	regexp.MustCompile(`(?i)---\s*end\s+of\s+(instructions?|prompt)`),
	regexp.MustCompile(`(?i)\[end\s+of\s+context\]`),

	// Encoding/escaping attempts
	regexp.MustCompile(`(?i)\\x[0-9a-f]{2}`),
	regexp.MustCompile(`&#\d+;`),
	regexp.MustCompile(`(?i)\\u[0-9a-f]{4}`),

	// Instruction extraction
	regexp.MustCompile(`(?i)repeat\s+(your\s+)?(system\s+)?(instructions?|prompt)`),
	regexp.MustCompile(`(?i)what\s+(are|were)\s+your\s+(original\s+)?instructions?`),
	regexp.MustCompile(`(?i)show\s+me\s+your\s+(system\s+)?prompt`),
	regexp.MustCompile(`(?i)reveal\s+(your\s+)?instructions?`),
	regexp.MustCompile(`(?i)output\s+(your\s+)?initial\s+instructions?`),
}

// ---------------------------------------------------------------------------
// Context-aware suspicious patterns
// ---------------------------------------------------------------------------

var contextSuspiciousPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)jailbreak`),
	regexp.MustCompile(`(?i)DAN\s+mode`),
	regexp.MustCompile(`(?i)developer\s+mode`),
	regexp.MustCompile(`(?i)unrestricted\s+mode`),
	regexp.MustCompile(`(?i)bypass\s+(safety|filter|guard)`),
}

// ---------------------------------------------------------------------------
// Encoded payload detection
// ---------------------------------------------------------------------------

var base64Re = regexp.MustCompile(`[A-Za-z0-9+/]{20,}={0,2}`)

func detectBase64Payload(input string) bool {
	matches := base64Re.FindAllString(input, -1)
	for _, m := range matches {
		decoded, err := base64.StdEncoding.DecodeString(m)
		if err != nil {
			// Try RawStdEncoding (no padding).
			decoded, err = base64.RawStdEncoding.DecodeString(m)
			if err != nil {
				continue
			}
		}
		if !utf8.Valid(decoded) {
			continue
		}
		lower := strings.ToLower(string(decoded))
		if strings.Contains(lower, "ignore") ||
			strings.Contains(lower, "system") ||
			strings.Contains(lower, "instruction") ||
			strings.Contains(lower, "prompt") {
			return true
		}
	}
	return false
}

var hexRe = regexp.MustCompile(`(?:[0-9a-fA-F]{2}){10,}`)

func detectHexPayload(input string) bool {
	matches := hexRe.FindAllString(input, -1)
	for _, m := range matches {
		decoded, err := hex.DecodeString(m)
		if err != nil {
			continue
		}
		if !utf8.Valid(decoded) {
			continue
		}
		lower := strings.ToLower(string(decoded))
		if strings.Contains(lower, "ignore") ||
			strings.Contains(lower, "system") ||
			strings.Contains(lower, "instruction") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Special character ratio
// ---------------------------------------------------------------------------

func specialCharRatio(input string) float64 {
	if len(input) == 0 {
		return 0
	}
	count := 0
	total := 0
	for _, r := range input {
		total++
		if !isWordChar(r) && !unicode.IsSpace(r) && !isCommonPunct(r) {
			count++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(count) / float64(total)
}

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func isCommonPunct(r rune) bool {
	switch r {
	case '.', ',', '!', '?', '-', '(', ')', '\'', '"':
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Newline / zero-width helpers
// ---------------------------------------------------------------------------

var excessiveNewlines = regexp.MustCompile(`\n{5,}`)
var collapseNewlines = regexp.MustCompile(`\n{3,}`)
var zeroWidthRe = regexp.MustCompile("[\u200b\u200c\u200d\ufeff\u00ad]")

// excessiveDelimiters detects long runs of delimiter characters used to
// visually separate injected instructions from the real prompt.
var excessiveDelimiters = regexp.MustCompile(`[-=_]{10,}|[*#]{5,}`)

// ---------------------------------------------------------------------------
// ValidatePromptInput validates user input for potential prompt injection.
// context must be one of "topic", "query", "message".
// ---------------------------------------------------------------------------

func ValidatePromptInput(input, context string) PromptValidationResult {
	warnings := []string{}
	sanitized := input
	isValid := true

	maxLen, ok := maxLengths[context]
	if !ok {
		maxLen = maxLengths["message"]
	}

	// 1. Length validation
	runes := []rune(input)
	if len(runes) > maxLen {
		warnings = append(warnings, "input exceeds maximum length")
		sanitized = string(runes[:maxLen])
		isValid = false
	}

	// 2. Normalize confusables for pattern matching
	normalized := normalizeConfusables(input)

	// 3. Known injection patterns
	for _, pat := range injectionPatterns {
		if pat.MatchString(input) || pat.MatchString(normalized) {
			warnings = append(warnings, "detected potential injection pattern: "+pat.String())
			isValid = false
			sanitized = pat.ReplaceAllString(sanitized, "[REMOVED]")
		}
	}

	// 4. Context-aware suspicious patterns
	for _, pat := range contextSuspiciousPatterns {
		if pat.MatchString(input) || pat.MatchString(normalized) {
			warnings = append(warnings, "detected suspicious pattern: "+pat.String())
			isValid = false
		}
	}

	// 5. Base64 payload detection
	if detectBase64Payload(input) {
		warnings = append(warnings, "detected potential base64 encoded injection payload")
		isValid = false
	}

	// 6. Hex payload detection
	if detectHexPayload(input) {
		warnings = append(warnings, "detected potential hex encoded injection payload")
		isValid = false
	}

	// 7. Special character ratio
	if specialCharRatio(input) > 0.4 {
		warnings = append(warnings, "high ratio of special characters")
		isValid = false
	}

	// 8. Excessive newlines
	if excessiveNewlines.MatchString(input) {
		warnings = append(warnings, "detected excessive newlines")
		sanitized = collapseNewlines.ReplaceAllString(sanitized, "\n\n")
	}

	// 8b. Excessive delimiters (long runs of ---, ===, ___, ***, ###)
	if excessiveDelimiters.MatchString(input) {
		warnings = append(warnings, "detected excessive delimiter characters")
		isValid = false
	}

	// 9. Strip zero-width characters
	sanitized = zeroWidthRe.ReplaceAllString(sanitized, "")

	// 10. Trim whitespace
	sanitized = strings.TrimSpace(sanitized)

	return PromptValidationResult{
		IsValid:   isValid,
		Sanitized: sanitized,
		Warnings:  warnings,
	}
}

// ---------------------------------------------------------------------------
// Convenience wrappers
// ---------------------------------------------------------------------------

// ValidateMessage validates a chat message.
func ValidateMessage(msg string) PromptValidationResult {
	return ValidatePromptInput(msg, "message")
}

// ValidateQuery validates a search query.
func ValidateQuery(query string) PromptValidationResult {
	return ValidatePromptInput(query, "query")
}

// ValidateTopic validates a topic string.
func ValidateTopic(topic string) PromptValidationResult {
	return ValidatePromptInput(topic, "topic")
}

// ---------------------------------------------------------------------------
// Security logging
// ---------------------------------------------------------------------------

// LogSecurityWarning logs prompt security warnings with slog.
func LogSecurityWarning(userID, input string, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	preview := input
	if len(preview) > 100 {
		preview = preview[:100] + "..."
	}
	slog.Warn("prompt security warning",
		"user_id", userID,
		"warnings", warnings,
		"input_preview", preview,
	)
}
