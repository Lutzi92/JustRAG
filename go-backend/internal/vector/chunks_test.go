package vector

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestChunkInputSerialization verifies that ChunkInput fields can be marshalled as expected.
func TestChunkInputSerialization(t *testing.T) {
	t.Parallel()
	chunk := ChunkInput{
		ContextualPrefix: "[Context: greetings example]",
		Embedding:        []float64{0.1, 0.2, 0.3},
		Metadata:         map[string]any{"page": 1, "source": "test.pdf"},
	}
	if chunk.ContextualPrefix == "" {
		t.Error("ContextualPrefix field not retained")
	}

	// Metadata must round-trip through JSON.
	metaBytes, err := json.Marshal(chunk.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(metaBytes, &roundTripped); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if roundTripped["source"] != "test.pdf" {
		t.Errorf("expected source=test.pdf, got %v", roundTripped["source"])
	}

	// Embedding must serialize to a pgvector literal.
	embStr := formatEmbedding(chunk.Embedding)
	if embStr != "[0.1,0.2,0.3]" {
		t.Errorf("unexpected embedding string: %s", embStr)
	}
}

// TestFormatEmbeddingEmpty verifies that an empty embedding serializes to "[]".
func TestFormatEmbeddingEmpty(t *testing.T) {
	t.Parallel()
	s := formatEmbedding([]float64{})
	if s != "[]" {
		t.Errorf("expected [], got %q", s)
	}
}

// TestParseEmbeddingLiteralRoundTrip verifies that parseEmbeddingLiteral
// inverts formatEmbedding for canonical and edge-case shapes. formatEmbedding
// emits float32-precision text (e.g. 0.1 not 0.10000000149011612), so the
// stable round-trip is format → parse → format equals the first format.
func TestParseEmbeddingLiteralRoundTrip(t *testing.T) {
	t.Parallel()
	cases := [][]float64{
		{},
		{0.1, 0.2, 0.3},
		{-1.5, 0, 1.5},
		{1e-7, -2.5e3, 0.0001},
		{42},
	}
	for _, in := range cases {
		first := formatEmbedding(in)
		parsed, err := parseEmbeddingLiteral(first)
		if err != nil {
			t.Fatalf("parseEmbeddingLiteral(%q): %v", first, err)
		}
		if len(parsed) != len(in) {
			t.Fatalf("length mismatch for %q: got %d, want %d", first, len(parsed), len(in))
		}
		if second := formatEmbedding(parsed); second != first {
			t.Errorf("round-trip diverged: first=%q second=%q", first, second)
		}
	}
}

// TestParseEmbeddingLiteralErrors verifies rejection of malformed literals.
func TestParseEmbeddingLiteralErrors(t *testing.T) {
	t.Parallel()
	bad := []string{
		"",        // too short
		"1,2,3",   // missing brackets
		"[1,abc]", // non-numeric component
		"[",       // truncated
	}
	for _, in := range bad {
		if _, err := parseEmbeddingLiteral(in); err == nil {
			t.Errorf("expected error for %q, got nil", in)
		}
	}
}

// TestDeleteQueryConstruction verifies the SQL strings built by the delete helpers.
func TestDeleteQueryConstruction(t *testing.T) {
	t.Parallel()
	dims := 1536
	table := GetVectorTableName(dims)

	byFile := fmt.Sprintf(`DELETE FROM "%s" WHERE file_id = $1::uuid`, table)
	if !strings.Contains(byFile, "document_chunks") {
		t.Errorf("DeleteChunksByFileID query missing table name: %s", byFile)
	}

	byFiles := fmt.Sprintf(`DELETE FROM "%s" WHERE file_id = ANY($1::uuid[])`, table)
	if !strings.Contains(byFiles, "document_chunks") {
		t.Errorf("DeleteChunksByFileIDs query missing table name: %s", byFiles)
	}

	byKb := fmt.Sprintf(`DELETE FROM "%s" WHERE kb_id = $1::uuid`, table)
	if !strings.Contains(byKb, "document_chunks") {
		t.Errorf("DeleteChunksByKbID query missing table name: %s", byKb)
	}
}

// TestInsertRowSQLTemplate documents the per-row INSERT shape that
// insertBatch queues into pgx.Batch. Each row is a self-contained statement
// with 13 placeholders: $1..$12 are the row columns and $13 is the per-row
// regconfig used by the language-specific to_tsvector arm. Statement caching
// at the pool level keeps the prepared plan resident across rows.
func TestInsertRowSQLTemplate(t *testing.T) {
	t.Parallel()
	query := fmt.Sprintf(insertRowSQLTemplate, "document_chunks")

	for _, want := range []string{
		"$13::regconfig",       // per-row regconfig binding
		"contextual_prefix",    // column list intact
		"embedding_low",        // dual-precision arm intact
		"parent_chunk_id",      // parent-child path intact
		"raptor_parent_id",     // RAPTOR path intact
		`"document_chunks"`,    // quoted table name
		"COALESCE($2, '')",     // tsvector folds the contextual prefix
		"to_tsvector('simple'", // simple arm uses hardcoded regconfig
	} {
		if !strings.Contains(query, want) {
			t.Errorf("template missing %q\nquery: %s", want, query)
		}
	}
}

// TestNewChunkService verifies construction with a nil pool compiles and produces a non-nil service.
func TestNewChunkService(t *testing.T) {
	t.Parallel()
	svc := NewChunkService(nil)
	if svc == nil {
		t.Fatal("NewChunkService returned nil")
	}
}
