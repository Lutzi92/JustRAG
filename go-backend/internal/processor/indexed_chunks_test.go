package processor

import (
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/splitter"
)

// smallCfg forces several chunks out of a short test document.
func smallCfg() splitter.Config {
	cfg := splitter.DefaultConfig()
	cfg.ChunkSize = 12 // tokens (~4 bytes each in the splitter's model)
	cfg.ChunkOverlap = 0
	return cfg
}

func TestBuildIndexedChunks_PerPageTextKeepsOnePagePerChunk(t *testing.T) {
	res := &parser.ParseResult{
		Text: "alpha page content\n\nbravo page content",
		Pages: []parser.PageText{
			{PageNumber: 4, Text: "alpha page content"},
			{PageNumber: 5, Text: "bravo page content"},
		},
	}
	got := buildIndexedChunks(res, smallCfg())
	if len(got) < 2 {
		t.Fatalf("expected at least one chunk per page, got %d", len(got))
	}
	for _, ic := range got {
		if len(ic.Pages) != 1 {
			t.Errorf("per-page split must yield exactly one page per chunk, got %v for %q", ic.Pages, ic.Text)
		}
		wantPage := 4
		if strings.Contains(ic.Text, "bravo") {
			wantPage = 5
		}
		if ic.Pages[0] != wantPage {
			t.Errorf("chunk %q: expected page %d, got %v", ic.Text, wantPage, ic.Pages)
		}
	}
}

func TestBuildIndexedChunks_NoPageInfoYieldsNoPages(t *testing.T) {
	res := &parser.ParseResult{Text: "plain text with no page information at all"}
	got := buildIndexedChunks(res, smallCfg())
	if len(got) == 0 {
		t.Fatal("expected chunks")
	}
	for _, ic := range got {
		if ic.Pages != nil {
			t.Errorf("expected no page metadata, got %v", ic.Pages)
		}
	}
}
