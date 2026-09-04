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

// ReadOnlyShape applies the format rules shared by sql_query, table_query
// and the tabular router: SELECT-only (optionally preceded by WITH — CTEs
// are legal router output), single statement, no comments, no DDL/DML
// keyword. Table-allowlist enforcement is the caller's responsibility.
func ReadOnlyShape(q string) error {
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		return fmt.Errorf("empty query")
	}
	upper := strings.ToUpper(trimmed)
	startsSelect := strings.HasPrefix(upper, "SELECT ") || strings.HasPrefix(upper, "SELECT\n") || strings.HasPrefix(upper, "SELECT\t")
	startsWith := strings.HasPrefix(upper, "WITH ") || strings.HasPrefix(upper, "WITH\n") || strings.HasPrefix(upper, "WITH\t")
	if !startsSelect && !startsWith {
		return fmt.Errorf("query must start with SELECT")
	}
	if strings.Contains(strings.TrimRight(trimmed, "; \n\t"), ";") {
		return fmt.Errorf("only one statement allowed (no internal `;`)")
	}
	if strings.Contains(trimmed, "--") || strings.Contains(trimmed, "/*") {
		return fmt.Errorf("comments are not allowed")
	}
	if m := deniedKeywordRe.FindString(trimmed); m != "" {
		return fmt.Errorf("disallowed keyword: %s", strings.ToUpper(m))
	}
	return nil
}
