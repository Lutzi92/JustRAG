package vector

import "testing"

func TestGetVectorTableName(t *testing.T) {
	t.Parallel()
	if GetVectorTableName(1536) != "document_chunks" {
		t.Fatal("wrong table name for 1536 dimensions")
	}
	if GetVectorTableName(3072) != "document_chunks_3072" {
		t.Fatal("wrong table name for 3072 dimensions")
	}
}

func TestPgTextSearchConfig(t *testing.T) {
	t.Parallel()
	if PgTextSearchConfig("de") != "german" {
		t.Fatal("expected german for de")
	}
	if PgTextSearchConfig("unknown") != "english" {
		t.Fatal("expected english for unknown language")
	}
}
