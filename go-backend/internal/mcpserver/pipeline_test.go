package mcpserver

import (
	"testing"

	"github.com/justrag/go-backend/internal/chat"
)

func TestMapSources(t *testing.T) {
	in := []chat.ChatSource{
		{Index: 1, FileName: "a.pdf", FileID: "f1", Score: 0.91},
		{Index: 2, FileName: "b.docx", FileID: "f2", Score: 0.77},
	}
	got := mapSources(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0] != (Source{Index: 1, FileID: "f1", FileName: "a.pdf", Score: 0.91}) {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].FileName != "b.docx" || got[1].Score != 0.77 {
		t.Errorf("got[1] = %+v", got[1])
	}
}
