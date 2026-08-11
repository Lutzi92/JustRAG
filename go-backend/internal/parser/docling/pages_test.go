package docling

import (
	"reflect"
	"testing"

	"github.com/justrag/go-backend/internal/parser"
)

func TestBuildPages_GroupsItemsByProvenance(t *testing.T) {
	got := buildPages([]DocItem{
		{Page: 1, Label: "title", Text: "Bericht"},
		{Page: 1, Label: "text", Text: "Erster Absatz."},
		{Page: 2, Label: "section_header", Text: "Kapitel 2"},
		{Page: 2, Label: "list_item", Text: "Erster Punkt"},
		{Page: 3, Label: "text", Text: "Letzte Seite."},
	})
	want := []parser.PageText{
		{PageNumber: 1, Text: "# Bericht\n\nErster Absatz."},
		{PageNumber: 2, Text: "## Kapitel 2\n\n- Erster Punkt"},
		{PageNumber: 3, Text: "Letzte Seite."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pages mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildPages_SkipsFurniture(t *testing.T) {
	// Running headers and footers repeat on every page and carry no
	// information; docling leaves them out of its markdown too.
	got := buildPages([]DocItem{
		{Page: 1, Label: "page_header", Text: "Vertraulich", Furniture: true},
		{Page: 1, Label: "text", Text: "Inhalt."},
		{Page: 1, Label: "page_footer", Text: "Seite 1 von 2", Furniture: true},
	})
	want := []parser.PageText{{PageNumber: 1, Text: "Inhalt."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pages mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildPages_ItemsWithoutProvenanceJoinTheCurrentPage(t *testing.T) {
	got := buildPages([]DocItem{
		{Page: 1, Label: "text", Text: "Seite eins."},
		{Page: 0, Label: "text", Text: "Ohne Provenienz."},
		{Page: 2, Label: "text", Text: "Seite zwei."},
	})
	want := []parser.PageText{
		{PageNumber: 1, Text: "Seite eins.\n\nOhne Provenienz."},
		{PageNumber: 2, Text: "Seite zwei."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pages mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildPages_LeadingItemsWithoutProvenanceJoinTheFirstPage(t *testing.T) {
	// Content ahead of any page-numbered item would otherwise be dropped.
	got := buildPages([]DocItem{
		{Page: 0, Label: "title", Text: "Titel"},
		{Page: 4, Label: "text", Text: "Erster nummerierter Absatz."},
	})
	want := []parser.PageText{
		{PageNumber: 4, Text: "# Titel\n\nErster nummerierter Absatz."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pages mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildPages_ContentSplitAcrossPagesStaysWithItsOwnPage(t *testing.T) {
	// A page revisited later (multi-column reading order) must not create a
	// second entry for the same page number.
	got := buildPages([]DocItem{
		{Page: 1, Label: "text", Text: "Links."},
		{Page: 2, Label: "text", Text: "Seite zwei."},
		{Page: 1, Label: "text", Text: "Rechts."},
	})
	want := []parser.PageText{
		{PageNumber: 1, Text: "Links.\n\nRechts."},
		{PageNumber: 2, Text: "Seite zwei."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pages mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildPages_NoProvenanceAtAllYieldsNil(t *testing.T) {
	got := buildPages([]DocItem{
		{Page: 0, Label: "text", Text: "Kein Seitenbezug."},
	})
	if got != nil {
		t.Errorf("expected nil pages without any provenance, got %+v", got)
	}
}

func TestRenderTable_MarkdownWithHeaderSeparator(t *testing.T) {
	got := renderTable(&DocTable{
		NumRows: 3, NumCols: 3,
		Cells: []DocTableCell{
			{Text: "Produkt", Row: 0, Col: 0, Header: true},
			{Text: "Version", Row: 0, Col: 1, Header: true},
			{Text: "Status", Row: 0, Col: 2, Header: true},
			{Text: "Alpha", Row: 1, Col: 0},
			{Text: "1.0", Row: 1, Col: 1},
			{Text: "aktiv", Row: 1, Col: 2},
			{Text: "Beta", Row: 2, Col: 0},
			{Text: "2.1", Row: 2, Col: 1},
			{Text: "geplant", Row: 2, Col: 2},
		},
	})
	want := "| Produkt | Version | Status |\n" +
		"| --- | --- | --- |\n" +
		"| Alpha | 1.0 | aktiv |\n" +
		"| Beta | 2.1 | geplant |"
	if got != want {
		t.Errorf("table mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestRenderTable_PlacesCellsByOffsetNotByOrder(t *testing.T) {
	// Cells arrive in arbitrary order and a row may be sparse; placing them
	// sequentially would shift every value into the wrong column.
	got := renderTable(&DocTable{
		NumRows: 2, NumCols: 2,
		Cells: []DocTableCell{
			{Text: "unten rechts", Row: 1, Col: 1},
			{Text: "oben links", Row: 0, Col: 0},
		},
	})
	want := "| oben links |  |\n| --- | --- |\n|  | unten rechts |"
	if got != want {
		t.Errorf("table mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestRenderTable_EscapesPipesAndNewlines(t *testing.T) {
	got := renderTable(&DocTable{
		NumRows: 1, NumCols: 2,
		Cells: []DocTableCell{
			{Text: "a|b", Row: 0, Col: 0},
			{Text: "zwei\nzeilen", Row: 0, Col: 1},
		},
	})
	want := "| a\\|b | zwei zeilen |\n| --- | --- |"
	if got != want {
		t.Errorf("table mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestRenderItem_Labels(t *testing.T) {
	for _, tc := range []struct {
		label string
		want  string
	}{
		{"title", "# X"},
		{"section_header", "## X"},
		{"list_item", "- X"},
		{"text", "X"},
		{"caption", "X"},
		{"", "X"},
	} {
		if got := renderItem(DocItem{Label: tc.label, Text: "X"}); got != tc.want {
			t.Errorf("label %q: got %q, want %q", tc.label, got, tc.want)
		}
	}
	if got := renderItem(DocItem{Label: "code", Text: "run()"}); got != "```\nrun()\n```" {
		t.Errorf("code: got %q", got)
	}
}
