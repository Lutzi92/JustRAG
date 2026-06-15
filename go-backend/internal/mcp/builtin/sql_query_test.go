package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestValidateSQLQuery_Accept covers the happy paths: simple SELECT,
// SELECT with WHERE/JOIN/GROUP, multi-table joins on allowlist
// members. The allowlist passed in is the production default.
func TestValidateSQLQuery_Accept(t *testing.T) {
	t.Parallel()
	good := []string{
		"SELECT count(*) FROM messages",
		"SELECT id, content FROM messages WHERE chat_id = 'x'",
		"SELECT m.id FROM messages m JOIN chats c ON m.chat_id = c.id",
		"SELECT * FROM agent_decisions WHERE created_at > now() - interval '1 day'",
		"SELECT name FROM files",
		"select id from messages limit 10", // lowercase
	}
	for _, q := range good {
		if err := validateSQLQuery(q, allowedTablesDefault); err != nil {
			t.Errorf("expected accept, got error for %q: %v", q, err)
		}
	}
}

// TestValidateSQLQuery_DenyMutations: every mutation keyword must
// reject. These would all be rejected by the read-only role at the
// DB level too, but we want a clean tool-side error first.
func TestValidateSQLQuery_DenyMutations(t *testing.T) {
	t.Parallel()
	bad := []string{
		"DELETE FROM messages WHERE id = '1'",
		"UPDATE messages SET content = 'x'",
		"INSERT INTO messages (id) VALUES ('1')",
		"DROP TABLE messages",
		"TRUNCATE messages",
		"ALTER TABLE messages ADD COLUMN x text",
		"GRANT SELECT ON messages TO x",
		"COPY messages TO '/tmp/x'",
		"CREATE TABLE x (id int)",
	}
	for _, q := range bad {
		if err := validateSQLQuery(q, allowedTablesDefault); err == nil {
			t.Errorf("expected reject, got nil for %q", q)
		}
	}
}

// TestValidateSQLQuery_DenyTableOutsideAllowlist: SELECT against
// a table not in the allowlist must reject. This is the layer
// that catches an LLM that found `pg_class` or wrote `secrets`.
func TestValidateSQLQuery_DenyTableOutsideAllowlist(t *testing.T) {
	t.Parallel()
	bad := []string{
		"SELECT * FROM knowledge_bases", // operator-policy data, intentionally NOT in default allowlist
		"SELECT * FROM pg_class",
		"SELECT * FROM users",
		"SELECT m.* FROM messages m JOIN secrets s ON s.user_id = m.user_id",
	}
	for _, q := range bad {
		err := validateSQLQuery(q, allowedTablesDefault)
		if err == nil {
			t.Errorf("expected reject, got nil for %q", q)
		} else if !strings.Contains(err.Error(), "not in allowlist") &&
			!strings.Contains(err.Error(), "no FROM clause") {
			t.Errorf("expected allowlist error for %q, got: %v", q, err)
		}
	}
}

// TestValidateSQLQuery_DenyCommaJoin: comma-separated FROM lists must
// reject. fromOrJoinRe only validates the identifier directly after
// FROM/JOIN, so `FROM messages, knowledge_bases` would otherwise read a
// non-allowlisted table — including via a derived-table prefix that ends
// in `), other_table`.
func TestValidateSQLQuery_DenyCommaJoin(t *testing.T) {
	t.Parallel()
	bad := []string{
		"SELECT * FROM messages, knowledge_bases",
		"SELECT * FROM messages m, knowledge_bases kb WHERE m.kb_id = kb.id",
		"SELECT * FROM messages AS m, chats AS c",
		"SELECT * FROM (SELECT id FROM messages) m, knowledge_bases kb",
		"SELECT * FROM messages WHERE id IN (SELECT id FROM chats c, knowledge_bases kb)",
	}
	for _, q := range bad {
		if err := validateSQLQuery(q, allowedTablesDefault); err == nil {
			t.Errorf("expected reject, got nil for %q", q)
		}
	}
}

// TestValidateSQLQuery_AcceptLegitimateCommas: commas outside a FROM
// table list (select list, IN lists, function args, GROUP/ORDER BY,
// string literals) must NOT trip the comma-join rejection.
func TestValidateSQLQuery_AcceptLegitimateCommas(t *testing.T) {
	t.Parallel()
	good := []string{
		"SELECT id, content, created_at FROM messages",
		"SELECT * FROM messages WHERE id IN ('a', 'b', 'c')",
		"SELECT coalesce(content, '') FROM messages",
		"SELECT chat_id, count(*) FROM messages GROUP BY chat_id ORDER BY count(*) DESC, chat_id",
		"SELECT m.id, c.title FROM messages m JOIN chats c ON m.chat_id = c.id",
		// Comma inside a string literal within the FROM region (the ON
		// clause): the scanner must skip literal contents. (A literal
		// containing the word FROM would be false-rejected by the
		// pre-existing fromOrJoinRe layer — separate, known limitation.)
		"SELECT m.id FROM messages m JOIN chats c ON c.title = 'a, b'",
		"SELECT (SELECT count(*) FROM chats), id FROM messages",
		"SELECT * FROM messages UNION SELECT * FROM chats ORDER BY 1, 2",
	}
	for _, q := range good {
		if err := validateSQLQuery(q, allowedTablesDefault); err != nil {
			t.Errorf("expected accept, got error for %q: %v", q, err)
		}
	}
}

// TestValidateSQLQuery_DenyMultiStatement covers the smuggle path
// where an LLM appends a second statement after `;`.
func TestValidateSQLQuery_DenyMultiStatement(t *testing.T) {
	t.Parallel()
	bad := []string{
		"SELECT * FROM messages; DROP TABLE messages",
		"SELECT 1; SELECT 2",
		"SELECT * FROM messages;DELETE FROM messages",
	}
	for _, q := range bad {
		if err := validateSQLQuery(q, allowedTablesDefault); err == nil {
			t.Errorf("expected reject, got nil for %q", q)
		}
	}
	// Trailing single `;` is allowed (the operator-friendly form).
	if err := validateSQLQuery("SELECT * FROM messages;", allowedTablesDefault); err != nil {
		t.Errorf("trailing single `;` should be allowed: %v", err)
	}
}

// TestValidateSQLQuery_DenyComments: comment markers could hide
// disallowed keywords from the keyword scan. Reject early.
func TestValidateSQLQuery_DenyComments(t *testing.T) {
	t.Parallel()
	bad := []string{
		"SELECT * FROM messages -- DROP TABLE messages",
		"SELECT /* DROP */ * FROM messages",
		"SELECT * FROM messages /* multi\nline */ WHERE id = 'x'",
	}
	for _, q := range bad {
		if err := validateSQLQuery(q, allowedTablesDefault); err == nil {
			t.Errorf("expected reject, got nil for %q", q)
		}
	}
}

// TestValidateSQLQuery_RejectNonSelectStart: even a query that
// would technically pass the keyword filter must start with SELECT.
// Catches "WITH x AS (...) SELECT ..." and similar.
func TestValidateSQLQuery_RejectNonSelectStart(t *testing.T) {
	t.Parallel()
	bad := []string{
		"WITH x AS (SELECT 1) SELECT * FROM x",
		"  SHOW tables",
		"EXPLAIN SELECT * FROM messages",
	}
	for _, q := range bad {
		if err := validateSQLQuery(q, allowedTablesDefault); err == nil {
			t.Errorf("expected reject, got nil for %q", q)
		}
	}
}

// fakeSQLExecutor is a deterministic stand-in for the pgx pool.
type fakeSQLExecutor struct {
	cols      []string
	rows      []map[string]any
	truncated bool
	err       error
	gotQuery  string
}

func (f *fakeSQLExecutor) Execute(_ context.Context, q string, _, _ int) ([]string, []map[string]any, bool, error) {
	f.gotQuery = q
	return f.cols, f.rows, f.truncated, f.err
}

// TestSQLQueryHandler_HappyPath: validation passes, executor returns
// rows, the tool serialises them into Structured.
func TestSQLQueryHandler_HappyPath(t *testing.T) {
	t.Parallel()
	exec := &fakeSQLExecutor{
		cols: []string{"id", "name"},
		rows: []map[string]any{
			{"id": "1", "name": "alpha"},
			{"id": "2", "name": "beta"},
		},
	}
	tool := newSQLQueryWithExecutor(exec)
	got, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"query":"SELECT id, name FROM files"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if exec.gotQuery != "SELECT id, name FROM files" {
		t.Errorf("query not propagated: %q", exec.gotQuery)
	}
	if got.Meta["row_count"].(int) != 2 {
		t.Errorf("row_count meta wrong: %v", got.Meta["row_count"])
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Structured, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["row_count"].(float64) != 2 {
		t.Errorf("structured row_count wrong: %v", payload["row_count"])
	}
}

// TestSQLQueryUnconfigured_AlwaysErrors: the disabled stub registered when
// no read-only role is configured must reject even a valid SELECT, so a
// deployment that enables MCP tools without provisioning JUSTRAG_DB_URL_READONLY
// can never run LLM-authored SQL against the read/write pool.
func TestSQLQueryUnconfigured_AlwaysErrors(t *testing.T) {
	t.Parallel()
	tool := NewSQLQueryUnconfigured()
	if tool.Name != "sql_query" {
		t.Errorf("stub must keep the sql_query name, got %q", tool.Name)
	}
	_, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"query":"SELECT id FROM files"}`),
	)
	if err == nil {
		t.Fatal("expected unconfigured stub to return an error for a valid SELECT")
	}
	if !strings.Contains(err.Error(), "JUSTRAG_DB_URL_READONLY") {
		t.Errorf("error should name the env var to set, got: %v", err)
	}
}

// TestSQLQueryHandler_RejectsBeforeExecutor: the validator runs
// BEFORE the executor — a forbidden query must never hit the DB.
// Pin this with an executor that records whether it was called.
func TestSQLQueryHandler_RejectsBeforeExecutor(t *testing.T) {
	t.Parallel()
	exec := &fakeSQLExecutor{}
	tool := newSQLQueryWithExecutor(exec)
	_, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"query":"DROP TABLE messages"}`),
	)
	if err == nil {
		t.Fatal("expected validation rejection")
	}
	if exec.gotQuery != "" {
		t.Errorf("validator failed-open: executor was called with %q", exec.gotQuery)
	}
}

// BenchmarkValidateReadOnlyShape guards the precompiled deniedKeywordRe: the
// validator runs on every sql_query / table_query call, and it previously
// rebuilt 24 regexes per invocation. A regression to per-call MustCompile
// shows up here as a large allocs/op jump.
func BenchmarkValidateReadOnlyShape(b *testing.B) {
	// A realistic accepted query (walks the full keyword scan without an early
	// reject) plus one rejected near the end of the keyword list.
	const accepted = "SELECT m.id, c.title FROM messages m JOIN chats c ON m.chat_id = c.id WHERE m.created_at > '2026-01-01' ORDER BY m.created_at DESC"
	const rejected = "SELECT * FROM messages WHERE id = 'x' DEALLOCATE all"
	b.ReportAllocs()
	for b.Loop() {
		_ = validateReadOnlyShape(accepted)
		_ = validateReadOnlyShape(rejected)
	}
}
