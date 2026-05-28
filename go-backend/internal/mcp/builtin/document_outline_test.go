package builtin

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeOutlineFetcher struct {
	headings []string
	gotKbID  string
	gotFile  string
}

func (f *fakeOutlineFetcher) DocumentOutline(_ context.Context, kbID, fileID string) ([]string, error) {
	f.gotKbID = kbID
	f.gotFile = fileID
	return f.headings, nil
}

func TestDocumentOutline_RequiresFileID(t *testing.T) {
	t.Parallel()
	tool := NewDocumentOutline(&fakeOutlineFetcher{})
	_, err := tool.Handler.Invoke(context.Background(), json.RawMessage(`{"kb_id":"kb-1"}`))
	if err == nil {
		t.Error("expected error on missing file_id")
	}
}

// TestDocumentOutline_HeadingsInMeta: the tool returns headings in
// `Meta["outline"]`, not in Chunks — the result type is meant for
// chunk content, but headings are a list. The frontend reads
// `meta.outline` directly. Pin the wire shape.
func TestDocumentOutline_HeadingsInMeta(t *testing.T) {
	t.Parallel()
	fake := &fakeOutlineFetcher{
		headings: []string{"Chapter 1", "Chapter 1 / Section 1.2", "Chapter 2"},
	}
	tool := NewDocumentOutline(fake)
	got, err := tool.Handler.Invoke(context.Background(),
		json.RawMessage(`{"file_id":"f1","kb_id":"kb-1"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Chunks) != 0 {
		t.Errorf("outline must NOT populate Chunks (it's not chunk content); got %d", len(got.Chunks))
	}
	outline, ok := got.Meta["outline"].([]string)
	if !ok {
		t.Fatalf("meta.outline must be []string, got %T", got.Meta["outline"])
	}
	if len(outline) != 3 || outline[0] != "Chapter 1" {
		t.Errorf("outline content lost: %v", outline)
	}
	if got.Meta["heading_count"].(int) != 3 {
		t.Errorf("heading_count mismatch: %v", got.Meta["heading_count"])
	}
}
