package prompts

import "strings"

// ChatSystemPromptDisplay returns ChatSystemPrompt(lang) in the form shown to
// KB owners (GET /api/chat/rag-system-prompt): the same lines, with the
// indentation the Go raw-string literal picks up from the surrounding source
// removed. The model always receives ChatSystemPrompt unchanged — this form
// only exists so a read-only view does not depend on how the literal happens
// to be indented in this file.
func ChatSystemPromptDisplay(lang string) string {
	return dedentLiteral(ChatSystemPrompt(lang))
}

// dedentLiteral strips the indentation common to every non-blank line after
// the first. A raw-string literal's first line starts right after the opening
// backtick and carries no source indentation, so it is left alone and does
// not count towards the common prefix. Relative indentation (nested
// sub-items) is kept; whitespace-only lines become empty.
func dedentLiteral(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return s
	}
	common := -1
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if common < 0 || n < common {
			common = n
		}
	}
	for i := 1; i < len(lines); i++ {
		switch {
		case strings.TrimSpace(lines[i]) == "":
			lines[i] = ""
		case common > 0:
			lines[i] = lines[i][common:]
		}
	}
	return strings.Join(lines, "\n")
}
