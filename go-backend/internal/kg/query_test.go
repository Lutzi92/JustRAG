package kg

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Compile-time interface check — *PgStore must satisfy Store so the chat /
// MCP packages can swap in fakes without breaking the production wiring.
// ---------------------------------------------------------------------------

var _ Store = (*PgStore)(nil)

// ---------------------------------------------------------------------------
// Guard-branch unit tests — exercise the defensive paths that return
// without touching the DB. No live DB required.
// ---------------------------------------------------------------------------

func TestPgStore_LookupEntityByName_NilPool(t *testing.T) {
	t.Parallel()
	s := &PgStore{pool: nil}
	if _, err := s.LookupEntityByName(context.Background(), "kb", "anything"); err == nil {
		t.Fatal("expected error with nil pool, got nil")
	}
}

func TestPgStore_LookupEntityByName_NilReceiver(t *testing.T) {
	t.Parallel()
	var s *PgStore
	if _, err := s.LookupEntityByName(context.Background(), "kb", "anything"); err == nil {
		t.Fatal("expected error on nil receiver, got nil")
	}
}

func TestPgStore_LookupEntityByName_EmptyName(t *testing.T) {
	t.Parallel()
	// Sentinel pool so we pass the nil-pool guard and reach the
	// name-validation branch. The function must reject before any
	// DB call — pool is unused on this branch.
	s := &PgStore{pool: &pgxpool.Pool{}}
	cases := []string{"", "   ", "\t\n"}
	for _, in := range cases {
		t.Run(fmt.Sprintf("name=%q", in), func(t *testing.T) {
			t.Parallel()
			_, err := s.LookupEntityByName(context.Background(), "kb", in)
			if err == nil {
				t.Fatal("expected error on empty/whitespace name, got nil")
			}
			if !strings.Contains(err.Error(), "empty entity name") {
				t.Fatalf("expected 'empty entity name' error, got %v", err)
			}
		})
	}
}

func TestPgStore_LookupSubgraph_NilPool(t *testing.T) {
	t.Parallel()
	s := &PgStore{pool: nil}
	if _, err := s.LookupSubgraph(context.Background(), "kb", 1, 1); err == nil {
		t.Fatal("expected error with nil pool, got nil")
	}
}

func TestPgStore_MatchAliasesInTokens_NilPool(t *testing.T) {
	t.Parallel()
	s := &PgStore{pool: nil}
	if _, err := s.MatchAliasesInTokens(context.Background(), "kb", []string{"a"}); err == nil {
		t.Fatal("expected error with nil pool, got nil")
	}
}

func TestPgStore_MatchAliasesInTokens_EmptyTokens(t *testing.T) {
	t.Parallel()
	// Sentinel pool to pass the nil-pool check and exercise the
	// empty-tokens early return. The function must short-circuit
	// BEFORE the DB call so a real query never fires on a noisy
	// caller — important because the chat code calls this on every
	// complex_reasoning turn.
	s := &PgStore{pool: &pgxpool.Pool{}}
	cases := [][]string{
		nil,
		{},
		{""},
		{"   ", "\t", ""},
	}
	for i, tokens := range cases {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			t.Parallel()
			got, err := s.MatchAliasesInTokens(context.Background(), "kb", tokens)
			if err != nil {
				t.Fatalf("expected nil error for empty/whitespace-only tokens, got %v", err)
			}
			if got != nil {
				t.Fatalf("expected nil result, got %v", got)
			}
		})
	}
}

