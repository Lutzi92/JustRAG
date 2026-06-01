package pgxutil

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestWithLimitOne(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "appends to bare SELECT",
			in:   `SELECT id FROM users WHERE id = $1`,
			want: `SELECT id FROM users WHERE id = $1 LIMIT 1`,
		},
		{
			name: "preserves existing LIMIT",
			in:   `SELECT id FROM users LIMIT 5`,
			want: `SELECT id FROM users LIMIT 5`,
		},
		{
			name: "preserves existing limit (lowercase)",
			in:   `SELECT id FROM users limit 5`,
			want: `SELECT id FROM users limit 5`,
		},
		{
			name: "skips INSERT...RETURNING",
			in:   `INSERT INTO users (name) VALUES ($1) RETURNING id`,
			want: `INSERT INTO users (name) VALUES ($1) RETURNING id`,
		},
		{
			name: "skips UPDATE...RETURNING",
			in:   `UPDATE users SET name = $1 WHERE id = $2 RETURNING id`,
			want: `UPDATE users SET name = $1 WHERE id = $2 RETURNING id`,
		},
		{
			name: "skips DELETE...RETURNING",
			in:   `DELETE FROM users WHERE id = $1 RETURNING id`,
			want: `DELETE FROM users WHERE id = $1 RETURNING id`,
		},
		{
			name: "appends to multiline SELECT",
			in:   "SELECT id\nFROM users\nWHERE id = $1",
			want: "SELECT id\nFROM users\nWHERE id = $1 LIMIT 1",
		},
		// Adversarial cases: identifier-like usages of "limit" /
		// "returning" must NOT suppress the appended LIMIT 1.
		{
			name: "column with limit substring is not a keyword",
			in:   `SELECT daily_limit FROM users`,
			want: `SELECT daily_limit FROM users LIMIT 1`,
		},
		{
			name: "alias prefixed with limit_ is not a keyword",
			in:   `SELECT count(*) AS limit_count FROM t`,
			want: `SELECT count(*) AS limit_count FROM t LIMIT 1`,
		},
		{
			name: "column with returning substring is not a keyword",
			in:   `SELECT returning_status FROM orders`,
			want: `SELECT returning_status FROM orders LIMIT 1`,
		},
		{
			name: "limit clause with parenthesized value",
			in:   `SELECT id FROM users LIMIT(5)`,
			want: `SELECT id FROM users LIMIT(5)`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withLimitOne(tc.in)
			if got != tc.want {
				t.Errorf("withLimitOne(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEscapeLike(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"plain", "plain"},
		{"100%", `100\%`},
		{"a_b", `a\_b`},
		{`a\b`, `a\\b`},
		// Every metachar in one input.
		{`100%_\`, `100\%\_\\`},
	}
	for _, tc := range cases {
		if got := EscapeLike(tc.in); got != tc.want {
			t.Errorf("EscapeLike(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsUniqueViolation(t *testing.T) {
	if IsUniqueViolation(nil) {
		t.Fatalf("nil error must not be a unique violation")
	}
	if IsUniqueViolation(errors.New("plain")) {
		t.Fatalf("plain error must not be a unique violation")
	}
	if IsUniqueViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatalf("FK violation 23503 must not match unique-violation check")
	}
	if !IsUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatalf("23505 must match")
	}
	// Wrapped error still matches.
	wrapped := fmt.Errorf("insert user: %w", &pgconn.PgError{Code: "23505"})
	if !IsUniqueViolation(wrapped) {
		t.Fatalf("wrapped 23505 must match via errors.As")
	}
}

func TestIsSerializationFailure(t *testing.T) {
	if IsSerializationFailure(nil) {
		t.Fatalf("nil error must not be a serialization failure")
	}
	if IsSerializationFailure(errors.New("plain")) {
		t.Fatalf("plain error must not be a serialization failure")
	}
	if !IsSerializationFailure(&pgconn.PgError{Code: "40001"}) {
		t.Fatalf("40001 must match")
	}
	if !IsSerializationFailure(&pgconn.PgError{Code: "40P01"}) {
		t.Fatalf("40P01 (deadlock) must match")
	}
	if IsSerializationFailure(&pgconn.PgError{Code: "23505"}) {
		t.Fatalf("unique violation must not match serialization-failure check")
	}
}

func TestClauseBuilderWhereFilter(t *testing.T) {
	// base=0: a self-contained WHERE filter. First clause binds $1.
	b := NewClauseBuilder(0)
	b.Add("operator_id = $%d", "op-1")
	b.Add("action = $%d", "delete")
	limitParam := b.Bind(50)
	offsetParam := b.Bind(100)

	if got, want := b.Clauses(), []string{"operator_id = $1", "action = $2"}; !equalStrings(got, want) {
		t.Fatalf("clauses = %v, want %v", got, want)
	}
	if limitParam != 3 || offsetParam != 4 {
		t.Fatalf("limit/offset params = %d/%d, want 3/4", limitParam, offsetParam)
	}
	if got, want := b.Args(), []any{"op-1", "delete", 50, 100}; !equalArgs(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	if b.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (Bind args excluded)", b.Len())
	}
}

func TestClauseBuilderSetListWithBaseOffset(t *testing.T) {
	// base=1: an id is prepended as $1, so the first SET assignment binds $2.
	b := NewClauseBuilder(1)
	b.Add("name = $%d", "alice")
	b.AddRaw("description = NULL")
	b.Add("email = $%d", "a@example.com")

	want := []string{"name = $2", "description = NULL", "email = $3"}
	if got := b.Clauses(); !equalStrings(got, want) {
		t.Fatalf("clauses = %v, want %v", got, want)
	}
	// AddRaw binds no argument: placeholder numbering must skip it.
	if got, want := b.Args(), []any{"alice", "a@example.com"}; !equalArgs(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestClauseBuilderEmpty(t *testing.T) {
	b := NewClauseBuilder(0)
	if b.Len() != 0 {
		t.Fatalf("Len = %d, want 0", b.Len())
	}
	if len(b.Args()) != 0 {
		t.Fatalf("Args len = %d, want 0", len(b.Args()))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalArgs(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
