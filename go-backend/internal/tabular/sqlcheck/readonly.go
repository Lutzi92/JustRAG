package sqlcheck

import (
	"fmt"
	"regexp"
	"strings"
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

// dollarQuoteRe matches a Postgres dollar-quote opener: "$$" or a tagged
// "$tag$" (tag = identifier characters). Fix round 2 (R46): stripQuotedAndLiteralText
// only understands '...'/"..." pairs; a single-quote character INSIDE a
// dollar-quoted string (e.g. $$'$$) desyncs its quote-parity tracking, so
// everything after it — including real SQL keywords — can end up wrongly
// blanked or wrongly left visible, depending on how the desync lands. Rather
// than teach the stripper a third quoting convention (and risk the same
// class of desync against some fourth one later), any statement containing
// a dollar-quote opener is rejected outright: dollar-quoting is not part of
// the router's supported surface, so there is no accept-side cost.
// "$5" (a dollar amount inside an ordinary literal) does not match: the
// second alternative requires an identifier-lead byte, not a digit, and
// neither alternative matches a lone "$".
var dollarQuoteRe = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)?\$`)

// ReadOnlyShape applies the format rules shared by sql_query, table_query
// and the tabular router: SELECT-only (optionally preceded by WITH, or by
// one or more "(" — CTEs are legal router output, and so is a
// parenthesized operand of a top-level UNION/INTERSECT/EXCEPT, e.g.
// "(SELECT ...) UNION (SELECT ...)"), single statement, no comments, no
// DDL/DML keyword. Table-allowlist enforcement is the caller's
// responsibility.
func ReadOnlyShape(q string) error {
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		return fmt.Errorf("empty query")
	}
	if dollarQuoteRe.MatchString(trimmed) {
		return fmt.Errorf("dollar-quoted strings are not allowed")
	}
	upper := strings.ToUpper(strings.TrimLeft(trimmed, "( \t\n"))
	startsSelect := strings.HasPrefix(upper, "SELECT ") || strings.HasPrefix(upper, "SELECT\n") || strings.HasPrefix(upper, "SELECT\t")
	startsWith := strings.HasPrefix(upper, "WITH ") || strings.HasPrefix(upper, "WITH\n") || strings.HasPrefix(upper, "WITH\t")
	if !startsSelect && !startsWith {
		return fmt.Errorf("query must start with SELECT")
	}
	// R39/R46: every check from here on judges only real SQL syntax, never
	// the contents of a double-quoted identifier (a column literally named
	// "update") or a single-quoted string literal (e.g. 'A SET B', or one
	// containing a literal ';' or '--') that merely LOOKS like a keyword,
	// statement separator, or comment marker. Safe now that dollar-quoting
	// (the one construct stripQuotedAndLiteralText cannot parse) is
	// rejected above rather than fed into it.
	stripped := stripQuotedAndLiteralText(trimmed)
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

// stripQuotedAndLiteralText blanks out the contents of every double-quoted
// identifier and single-quoted string literal in q, honouring the SQL
// ”/"" escaped-quote convention, while preserving the string's length and
// every character outside quotes verbatim. Used only to sanitize input to
// the denied-keyword regex; the original, unmodified q is what gets
// parsed and executed.
func stripQuotedAndLiteralText(q string) string {
	runes := []rune(q)
	out := make([]rune, len(runes))
	copy(out, runes)
	i := 0
	for i < len(runes) {
		quote := runes[i]
		if quote != '"' && quote != '\'' {
			i++
			continue
		}
		out[i] = ' '
		i++
		for i < len(runes) {
			if runes[i] == quote {
				out[i] = ' '
				i++
				if i < len(runes) && runes[i] == quote {
					// Escaped quote ('' or ""): still inside the literal.
					out[i] = ' '
					i++
					continue
				}
				break
			}
			out[i] = ' '
			i++
		}
	}
	return string(out)
}
