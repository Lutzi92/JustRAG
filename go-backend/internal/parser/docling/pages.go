package docling

import (
	"strings"

	"github.com/justrag/go-backend/internal/parser"
)

// buildPages groups the converted document's items into per-page text, in
// reading order, rendering each item the way docling's own markdown export
// would. The page number comes straight from each item's provenance, so a
// chunk's page is exact rather than inferred.
//
// Returns nil when no item carries a page number — the caller then reports no
// page information at all rather than guessing one.
func buildPages(items []DocItem) []parser.PageText {
	type page struct {
		number int
		parts  []string
	}
	var pages []page
	byNumber := make(map[int]int) // page number → index in pages

	// Items whose provenance is missing attach to the page in progress; those
	// appearing before any numbered item are held until the first one arrives.
	var pending []string
	current := 0

	appendTo := func(number int, text string) {
		idx, ok := byNumber[number]
		if !ok {
			pages = append(pages, page{number: number})
			idx = len(pages) - 1
			byNumber[number] = idx
		}
		pages[idx].parts = append(pages[idx].parts, text)
	}

	for _, it := range items {
		// Running headers, footers and page numbers repeat on every page and
		// carry no information; docling leaves them out of its markdown too.
		if it.Furniture {
			continue
		}
		text := renderItem(it)
		if text == "" {
			continue
		}

		number := it.Page
		if number <= 0 {
			number = current
		}
		if number <= 0 {
			pending = append(pending, text)
			continue
		}
		if len(pending) > 0 {
			for _, p := range pending {
				appendTo(number, p)
			}
			pending = nil
		}
		current = number
		appendTo(number, text)
	}

	if len(pages) == 0 {
		return nil
	}

	out := make([]parser.PageText, 0, len(pages))
	for _, p := range pages {
		text := strings.TrimSpace(strings.Join(p.parts, "\n\n"))
		if text == "" {
			continue
		}
		out = append(out, parser.PageText{PageNumber: p.number, Text: text})
	}
	return out
}

// renderItem turns one item into the markdown docling would have produced for
// it. Labels follow docling-core's DocItemLabel values.
func renderItem(it DocItem) string {
	if it.Table != nil {
		return renderTable(it.Table)
	}
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return ""
	}
	switch it.Label {
	case "title":
		return "# " + text
	case "section_header":
		return "## " + text
	case "list_item":
		return "- " + text
	case "code":
		return "```\n" + text + "\n```"
	case "formula":
		return "$$" + text + "$$"
	default:
		return text
	}
}

// renderTable renders a cell grid as a markdown table. Cells are placed by
// their row/column offsets, so merged or out-of-order cells still land in the
// right place instead of shifting the row.
func renderTable(t *DocTable) string {
	rows := t.NumRows
	cols := t.NumCols
	for _, c := range t.Cells {
		if c.Row+1 > rows {
			rows = c.Row + 1
		}
		if c.Col+1 > cols {
			cols = c.Col + 1
		}
	}
	if rows == 0 || cols == 0 {
		return ""
	}

	grid := make([][]string, rows)
	for i := range grid {
		grid[i] = make([]string, cols)
	}
	headerRows := 0
	for _, c := range t.Cells {
		if c.Row < 0 || c.Row >= rows || c.Col < 0 || c.Col >= cols {
			continue
		}
		// A cell's text may contain pipes or newlines, either of which would
		// break the row apart.
		grid[c.Row][c.Col] = strings.ReplaceAll(
			strings.ReplaceAll(c.Text, "|", "\\|"), "\n", " ")
		if c.Header && c.Row+1 > headerRows {
			headerRows = c.Row + 1
		}
	}

	var sb strings.Builder
	for r := 0; r < rows; r++ {
		sb.WriteString("| " + strings.Join(grid[r], " | ") + " |")
		sb.WriteString("\n")
		// The separator goes after the header row; a table without a marked
		// header still needs one after the first row to stay valid markdown.
		if r == headerRows-1 || (headerRows == 0 && r == 0) {
			sb.WriteString("|" + strings.Repeat(" --- |", cols))
			sb.WriteString("\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
