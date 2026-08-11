package parser

import (
	"reflect"
	"testing"
)

// fullText layout (offsets):
//
//	0            "page one text\n\n"   -> page 1
//	15           "page two text\n\n"   -> page 2
//	30           "page three text"     -> page 3
const spanFullText = "page one text\n\npage two text\n\npage three text"

var testSpans = []PageSpan{{Start: 0, Page: 1}, {Start: 15, Page: 2}, {Start: 30, Page: 3}}

func TestPagesForChunk_SinglePage(t *testing.T) {
	pages, next := PagesForChunk(testSpans, spanFullText, "page two text", 0)
	if !reflect.DeepEqual(pages, []int{2}) {
		t.Errorf("expected [2], got %v", pages)
	}
	if next != 16 {
		t.Errorf("expected next offset 16, got %d", next)
	}
}

func TestPagesForChunk_ChunkSpanningTwoPages(t *testing.T) {
	pages, _ := PagesForChunk(testSpans, spanFullText, "two text\n\npage three", 0)
	if !reflect.DeepEqual(pages, []int{2, 3}) {
		t.Errorf("expected [2 3], got %v", pages)
	}
}

func TestPagesForChunk_FirstChunkIsPageOne(t *testing.T) {
	pages, _ := PagesForChunk(testSpans, spanFullText, "page one", 0)
	if !reflect.DeepEqual(pages, []int{1}) {
		t.Errorf("expected [1], got %v", pages)
	}
}

func TestPagesForChunk_MonotonicSearchPrefersLaterOccurrence(t *testing.T) {
	// "text" occurs on every page; searching from offset 16 must resolve to
	// the page-2 occurrence, not the page-1 one.
	pages, _ := PagesForChunk(testSpans, spanFullText, "text", 16)
	if !reflect.DeepEqual(pages, []int{2}) {
		t.Errorf("expected [2], got %v", pages)
	}
}

func TestPagesForChunk_NoSpansYieldsNoPages(t *testing.T) {
	pages, next := PagesForChunk(nil, spanFullText, "page two text", 0)
	if pages != nil {
		t.Errorf("expected nil pages without spans, got %v", pages)
	}
	if next != 0 {
		t.Errorf("expected offset unchanged, got %d", next)
	}
}

func TestPagesForChunk_UnfindableChunkYieldsNoPages(t *testing.T) {
	pages, next := PagesForChunk(testSpans, spanFullText, "not in the document", 0)
	if pages != nil {
		t.Errorf("expected nil pages for unfindable chunk, got %v", pages)
	}
	if next != 0 {
		t.Errorf("expected offset unchanged, got %d", next)
	}
}

func TestPagesForChunk_StaleOffsetFallsBackToFullScan(t *testing.T) {
	// searchFrom past the end of the text must not panic and must still
	// resolve the chunk via a scan from the beginning.
	pages, _ := PagesForChunk(testSpans, spanFullText, "page one text", 9999)
	if !reflect.DeepEqual(pages, []int{1}) {
		t.Errorf("expected [1], got %v", pages)
	}
}
