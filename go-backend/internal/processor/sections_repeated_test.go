package processor

import (
	"strings"
	"testing"
)

// TestSectionsWithRepeatedIdenticalHeadings guards the assumption behind I7:
// the spreadsheet renderer (internal/tabular/render) now repeats the same
// "### <file> › <sheet>" heading at the start of EVERY row block, because the
// chunker only records a chunk's enclosing heading in metadata and never
// prepends it to the embedded text. That means a spreadsheet page contains
// dozens of byte-identical headings, and the section index has to keep
// attributing chunks correctly across them: the breadcrumb stays right, and
// the sequential searchFrom offset must advance past each occurrence rather
// than latching onto the first one.
func TestSectionsWithRepeatedIdenticalHeadings(t *testing.T) {
	const heading = "### ledger.xlsx › Gebäudeliste"
	var b strings.Builder
	b.WriteString("# ledger.xlsx\n\n")
	blocks := []string{
		"[tabular.sheet_a_0_0 rows 1–2]\nName: Haus 1\nName: Haus 2",
		"[tabular.sheet_a_0_0 rows 3–4]\nName: Haus 3\nName: Haus 4",
		"[tabular.sheet_a_0_0 rows 5–6]\nName: Haus 5\nName: Haus 6",
	}
	for _, blk := range blocks {
		b.WriteString(heading + "\n" + blk + "\n\n")
	}
	full := b.String()

	idx := ExtractMarkdownSections(full)
	if idx == nil || len(idx.headings) != 1+len(blocks) {
		t.Fatalf("expected 1 top-level + %d repeated headings, got %d", len(blocks), len(idx.headings))
	}

	offset := 0
	seen := make([]int, 0, len(blocks))
	for i, blk := range blocks {
		sections, next := SectionsForChunk(idx, full, blk, offset)
		if len(sections) != 2 || sections[0] != "ledger.xlsx" || sections[1] != "ledger.xlsx › Gebäudeliste" {
			t.Errorf("block %d breadcrumb = %v, want the file heading then the repeated sheet heading", i, sections)
		}
		if next <= offset {
			t.Fatalf("block %d: search offset did not advance (%d -> %d) — repeated headings would latch on the first block", i, offset, next)
		}
		seen = append(seen, next)
		offset = next
	}
	// Each block resolved to a strictly later position in the document.
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Errorf("blocks resolved out of order: %v", seen)
		}
	}
}
