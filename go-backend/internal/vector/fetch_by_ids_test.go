package vector

import (
	"context"
	"strings"
	"testing"
)

// TestFetchChunksByIDs_EmptyShortCircuits: empty / nil IDs must
// return (nil, nil) without touching the DB. Without this guard a
// chat turn with no graph routing would still pay a no-op SQL
// round-trip.
func TestFetchChunksByIDs_EmptyShortCircuits(t *testing.T) {
	t.Parallel()
	// nil pools are safe because the function must short-circuit before any DB access.
	svc := NewSearchService(nil, nil, nil)

	rows, err := svc.fetchChunksByIDs(context.Background(), "document_chunks_4096", "kb-1", nil)
	if err != nil {
		t.Errorf("nil ids: unexpected error: %v", err)
	}
	if rows != nil {
		t.Errorf("nil ids: expected nil rows, got %v", rows)
	}

	rows, err = svc.fetchChunksByIDs(context.Background(), "document_chunks_4096", "kb-1", []string{})
	if err != nil {
		t.Errorf("empty ids: unexpected error: %v", err)
	}
	if rows != nil {
		t.Errorf("empty ids: expected nil rows, got %v", rows)
	}
}

// TestFetchChunksByIDs_RejectsInvalidTable: SQL injection defence.
// The table name is interpolated unquoted into the SQL, so it MUST
// be validated against the same allowlist FetchChunksByIndices uses
// (validVectorTable). A bad name produces a non-nil error and never
// touches the DB.
func TestFetchChunksByIDs_RejectsInvalidTable(t *testing.T) {
	t.Parallel()
	svc := NewSearchService(nil, nil, nil)
	bad := []string{
		"document_chunks; DROP TABLE users; --",
		"some_other_table",
		"document_chunks_", // partial match — the digit suffix is required after the underscore
		"document_chunks_4096; --",
		"\"document_chunks_4096\"", // quoted variant — the SQL builder adds its own quoting
	}
	for _, table := range bad {
		_, err := svc.fetchChunksByIDs(context.Background(), table, "kb-1", []string{"c1"})
		if err == nil {
			t.Errorf("table=%q: expected error, got nil", table)
			continue
		}
		if !strings.Contains(err.Error(), "invalid table name") {
			t.Errorf("table=%q: error should mention invalid table; got: %v", table, err)
		}
	}
}

// TestFetchChunksByIDs_ValidTableNamesPassRegex: positive case for
// the allowlist regex — the standard dim-keyed names match. We
// can't drive fetchChunksByIDs all the way through without a DB
// (it would panic on nil vectorDB), so we test the regex directly
// to pin the allowlist contract — same regex used by
// FetchChunksByIndices.
func TestFetchChunksByIDs_ValidTableNamesPassRegex(t *testing.T) {
	t.Parallel()
	good := []string{
		"document_chunks",
		"document_chunks_1024",
		"document_chunks_4096",
		"document_chunks_2560",
	}
	for _, table := range good {
		if !validVectorTable.MatchString(table) {
			t.Errorf("table=%q: should be accepted by validVectorTable regex but was not", table)
		}
	}
}
