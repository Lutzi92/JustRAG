package processor

import (
	"reflect"
	"slices"
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

func TestBuildIndexedChunks_PageSpansAttributeLaterPages(t *testing.T) {
	// The regression under test: content from page 7 must not be labelled
	// page 1 just because the document arrived as one continuous blob.
	text := "first page prose here\n\nsecond page prose here\n\nthird page prose here"
	res := &parser.ParseResult{
		Text: text,
		PageSpans: []parser.PageSpan{
			{Start: 0, Page: 5},
			{Start: strings.Index(text, "second"), Page: 6},
			{Start: strings.Index(text, "third"), Page: 7},
		},
	}

	got := buildIndexedChunks(res, smallCfg())
	if len(got) == 0 {
		t.Fatal("expected chunks")
	}
	seen := map[int]bool{}
	for _, ic := range got {
		if len(ic.Pages) == 0 {
			t.Errorf("chunk %q got no page attribution", ic.Text)
			continue
		}
		for _, p := range ic.Pages {
			seen[p] = true
		}
		// Every chunk's pages must match the marker words it actually contains.
		for _, tc := range []struct {
			word string
			page int
		}{{"first", 5}, {"second", 6}, {"third", 7}} {
			if strings.Contains(ic.Text, tc.word) && !slices.Contains(ic.Pages, tc.page) {
				t.Errorf("chunk %q contains %q but pages are %v (want %d)", ic.Text, tc.word, ic.Pages, tc.page)
			}
		}
	}
	for _, p := range []int{5, 6, 7} {
		if !seen[p] {
			t.Errorf("page %d never attributed; got %+v", p, got)
		}
	}
}

func TestBuildIndexedChunks_ChunkCrossingPageBreakCarriesBothPages(t *testing.T) {
	text := "aaa bbb"
	res := &parser.ParseResult{
		Text:      text,
		PageSpans: []parser.PageSpan{{Start: 0, Page: 1}, {Start: 4, Page: 2}},
	}
	// A single chunk covering the whole text spans both pages.
	cfg := splitter.DefaultConfig()
	got := buildIndexedChunks(res, cfg)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk for a short text, got %d", len(got))
	}
	if !reflect.DeepEqual(got[0].Pages, []int{1, 2}) {
		t.Errorf("expected pages [1 2], got %v", got[0].Pages)
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
