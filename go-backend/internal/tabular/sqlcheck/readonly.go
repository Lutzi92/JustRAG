package sqlcheck

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// deniedKeywords is the explicit blacklist for whole-word DDL/DML
// keywords. Word-boundary matched (?i)\bX\b so column names
// containing the substring don't false-positive (a column named
// "update_count" wouldn't collide with the UPDATE keyword).
//
// Conservative: the production read-only role would reject these
// at the database level too, but rejecting at the tool layer
// surfaces a clean error to the LLM instead of an opaque DB error.
var deniedKeywords = []string{
	"INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "TRUNCATE",
	"CREATE", "GRANT", "REVOKE", "EXECUTE", "COPY", "SET",
	"VACUUM", "ANALYZE", "REINDEX", "CLUSTER", "LOCK", "NOTIFY",
	"LISTEN", "UNLISTEN", "PREPARE", "DEALLOCATE",
}

// deniedKeywordRe matches any denied keyword as a whole word, case-insensitive.
// Compiled once at package init rather than rebuilding 24 separate regexes on
// every sql_query / table_query invocation (ReadOnlyShape runs per tool
// call). A single alternation also short-circuits on the first match.
var deniedKeywordRe = regexp.MustCompile(`(?i)\b(?:` + strings.Join(deniedKeywords, "|") + `)\b`)

// ReadOnlyShape applies the format rules shared by sql_query, table_query
// and the tabular router: SELECT-only (optionally preceded by WITH, or by
// one or more "(" — CTEs are legal router output, and so is a
// parenthesized operand of a top-level UNION/INTERSECT/EXCEPT, e.g.
// "(SELECT ...) UNION (SELECT ...)"), single statement, no comments, no
// DDL/DML keyword, no dollar-quoted string (R46). Table-allowlist
// enforcement is the caller's responsibility.
func ReadOnlyShape(q string) error {
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		return fmt.Errorf("empty query")
	}
	// Fix round 3 (R54): every check below judges only real SQL syntax,
	// never the CONTENTS of a string/identifier/dollar-quoted literal —
	// stripLiterals is a single left-to-right tokenizer that recognizes
	// every top-level literal form Postgres itself recognizes (see its
	// doc comment), so it cannot desync the way a naive '.../"..." -only
	// stripper does on E'...' or U&'...' escape strings.
	stripped, foundDollarQuote, terminated := stripLiterals(trimmed)
	if !terminated {
		return fmt.Errorf("unterminated string or quoted identifier")
	}
	if foundDollarQuote {
		// R46: dollar-quoting is not part of the router's supported
		// surface, so reject outright rather than trust downstream
		// checks with content the real Postgres lexer treats as opaque
		// (no escapes at all — a dollar-quoted body can and does contain
		// arbitrary SQL-shaped text, by design).
		return fmt.Errorf("dollar-quoted strings are not allowed")
	}
	upper := strings.ToUpper(strings.TrimLeft(stripped, "( \t\n"))
	startsSelect := strings.HasPrefix(upper, "SELECT ") || strings.HasPrefix(upper, "SELECT\n") || strings.HasPrefix(upper, "SELECT\t")
	startsWith := strings.HasPrefix(upper, "WITH ") || strings.HasPrefix(upper, "WITH\n") || strings.HasPrefix(upper, "WITH\t")
	if !startsSelect && !startsWith {
		return fmt.Errorf("query must start with SELECT")
	}
	if strings.Contains(strings.TrimRight(stripped, "; \n\t"), ";") {
		return fmt.Errorf("only one statement allowed (no internal `;`)")
	}
	if strings.Contains(stripped, "--") || strings.Contains(stripped, "/*") {
		return fmt.Errorf("comments are not allowed")
	}
	if m := deniedKeywordRe.FindString(stripped); m != "" {
		return fmt.Errorf("disallowed keyword: %s", strings.ToUpper(m))
	}
	return nil
}

// stripLiterals is a single left-to-right pass over q that recognizes
// every top-level string/identifier/dollar-quoted literal form Postgres's
// own lexer recognizes, and blanks (replaces with spaces, preserving
// length and position) the contents of each one — so a keyword,
// separator, or comment marker that only appears INSIDE a literal never
// reaches the checks in ReadOnlyShape, while the same text OUTSIDE any
// literal is untouched. Recognized forms:
//
//   - '...'    standard string: ” is the only escaped-quote form.
//   - E'...' / e'...'   escape string: BOTH \x (backslash escapes
//     whatever follows it, including a quote) AND ” are valid
//     escaped-quote forms — this dual convention is exactly what a
//     naive '...'-only scanner gets wrong (R54): it sees E'\” as an
//     opening quote at the position of the escaped `'`, followed by a
//     doubled-quote escape, and stays open past where Postgres actually
//     closed the string.
//   - U&'...' / u&'...'   Unicode escape string: treated the same as an
//     E-string (backslash-escape-next). Real Postgres syntax only uses
//     backslash there for \XXXX code points, never immediately before a
//     quote character, so treating it as a superset of the real escaping
//     rule is safe — it can only recognize a boundary AT LEAST as late
//     as Postgres would, never earlier.
//   - "..."    quoted identifier: "" is the only escaped-quote form.
//   - $$...$$ / $tag$...$tag$   dollar-quoted string: no escapes at all
//     — the string ends only where the SAME opening delimiter recurs.
//     Reported back via the foundDollarQuote return so the caller can
//     reject it (R46) without a separate raw-text regex, which would
//     also misfire on a "$" that is itself inside another literal (e.g.
//     'a$b$c' or "a$b$c").
//
// terminated is false if any literal never closes (ran off the end of
// q) — Postgres's own lexer would raise "unterminated string" for the
// same input, so ReadOnlyShape treats it as unsafe to reason about
// anything the caller intended to follow.
func stripLiterals(q string) (stripped string, foundDollarQuote, terminated bool) {
	r := []rune(q)
	out := make([]rune, len(r))
	copy(out, r)
	terminated = true

	isWord := func(i int) bool {
		if i < 0 || i >= len(r) {
			return false
		}
		c := r[i]
		return c == '_' || unicode.IsLetter(c) || unicode.IsDigit(c)
	}
	blank := func(from, to int) {
		for k := from; k < to && k < len(out); k++ {
			out[k] = ' '
		}
	}

	i := 0
	for i < len(r) {
		c := r[i]

		switch {
		case c == '$':
			j := i + 1
			for j < len(r) && (r[j] == '_' || unicode.IsLetter(r[j]) || unicode.IsDigit(r[j])) {
				j++
			}
			if j < len(r) && r[j] == '$' {
				delim := r[i : j+1]
				foundDollarQuote = true
				end, ok := indexDelim(r, j+1, delim)
				if !ok {
					terminated = false
					blank(i, len(r))
					i = len(r)
					continue
				}
				blank(i, end)
				i = end
				continue
			}
			i++

		case (c == 'E' || c == 'e') && i+1 < len(r) && r[i+1] == '\'' && !isWord(i-1):
			end, ok := scanQuoted(r, i+2, '\'', true)
			if !ok {
				terminated = false
			}
			blank(i, end)
			i = end

		case (c == 'U' || c == 'u') && i+2 < len(r) && r[i+1] == '&' && r[i+2] == '\'' && !isWord(i-1):
			end, ok := scanQuoted(r, i+3, '\'', true)
			if !ok {
				terminated = false
			}
			blank(i, end)
			i = end

		case c == '\'':
			end, ok := scanQuoted(r, i+1, '\'', false)
			if !ok {
				terminated = false
			}
			blank(i, end)
			i = end

		case c == '"':
			end, ok := scanQuoted(r, i+1, '"', false)
			if !ok {
				terminated = false
			}
			blank(i, end)
			i = end

		default:
			i++
		}
	}
	return string(out), foundDollarQuote, terminated
}

// scanQuoted scans forward from start (the rune right after an opening
// quote) for the matching close, honouring a doubled quote as an escaped
// quote (standard SQL rule for both '...' and "..."), and additionally
// honouring a backslash as escaping whatever rune immediately follows it
// when allowBackslash is true (E-strings and Unicode escape strings). It
// returns the index one past the closing quote, and false if the literal
// runs off the end of r unterminated.
func scanQuoted(r []rune, start int, quote rune, allowBackslash bool) (end int, ok bool) {
	i := start
	for i < len(r) {
		if allowBackslash && r[i] == '\\' {
			i += 2 // escapes exactly the next rune, whatever it is
			continue
		}
		if r[i] == quote {
			if i+1 < len(r) && r[i+1] == quote {
				i += 2 // doubled-quote escape
				continue
			}
			return i + 1, true
		}
		i++
	}
	return len(r), false
}

// indexDelim finds the next occurrence of delim in r starting at from,
// returning the index one past its end, and false if delim never
// recurs before the end of r.
func indexDelim(r []rune, from int, delim []rune) (end int, ok bool) {
	for i := from; i+len(delim) <= len(r); i++ {
		match := true
		for k, dc := range delim {
			if r[i+k] != dc {
				match = false
				break
			}
		}
		if match {
			return i + len(delim), true
		}
	}
	return len(r), false
}
