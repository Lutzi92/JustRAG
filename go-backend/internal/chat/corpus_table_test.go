package chat

import (
	"testing"

	"github.com/justrag/go-backend/internal/vector"
)

func TestDimensionsFromTableName(t *testing.T) {
	cases := map[string]int{"document_chunks_4096": 4096, "document_chunks_2560": 2560, "bogus": 0, "document_chunks_": 0}
	for in, want := range cases {
		if got := dimensionsFromTableName(in); got != want {
			t.Errorf("dimensionsFromTableName(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSelectScopeFilesDedupAndCap(t *testing.T) {
	chunks := []vector.SearchChunk{
		{FileID: "a", FileName: "KZ 1.docx", Score: 0.9},
		{FileID: "a", FileName: "KZ 1.docx", Score: 0.8},
		{FileID: "b", FileName: "KZ 2.docx", Score: 0.7},
		{FileID: "c", FileName: "KZ 3.docx", Score: 0.6},
	}
	files, total := selectScopeFiles(chunks, 2)
	if total != 3 {
		t.Fatalf("total distinct = %d, want 3", total)
	}
	if len(files) != 2 {
		t.Fatalf("capped len = %d, want 2", len(files))
	}
	if files[0].FileID != "a" || files[1].FileID != "b" {
		t.Fatalf("order by score broken: %+v", files)
	}
}

func TestSelectScopeFilesNoCap(t *testing.T) {
	chunks := []vector.SearchChunk{{FileID: "a", FileName: "x", Score: 0.5}}
	files, total := selectScopeFiles(chunks, 0)
	if total != 1 || len(files) != 1 {
		t.Fatalf("no-cap: files=%d total=%d", len(files), total)
	}
}
