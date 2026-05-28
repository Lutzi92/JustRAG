package processor

import (
	"reflect"
	"testing"
)

func TestExtractMarkdownSections_NestedHeadings(t *testing.T) {
	text := `# Introduction
Some intro text here.

## Background
Background details.

### Details
More details here.

## Methods
Methods section.
`

	idx := ExtractMarkdownSections(text)

	if len(idx.headings) != 4 {
		t.Fatalf("expected 4 headings, got %d", len(idx.headings))
	}

	tests := []struct {
		name     string
		offset   int
		expected []string
	}{
		{
			name:     "at first heading",
			offset:   0,
			expected: []string{"Introduction"},
		},
		{
			name:     "in Background section",
			offset:   idx.headings[1].offset + 20,
			expected: []string{"Introduction", "Background"},
		},
		{
			name:     "in Details subsection",
			offset:   idx.headings[2].offset + 10,
			expected: []string{"Introduction", "Background", "Details"},
		},
		{
			name:     "in Methods section (## after ###)",
			offset:   idx.headings[3].offset + 5,
			expected: []string{"Introduction", "Methods"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := idx.At(tc.offset)
			if !reflect.DeepEqual(got, tc.expected) {
				t.Errorf("At(%d) = %v, want %v", tc.offset, got, tc.expected)
			}
		})
	}
}

func TestExtractMarkdownSections_EmptyText(t *testing.T) {
	idx := ExtractMarkdownSections("")
	if len(idx.headings) != 0 {
		t.Fatalf("expected 0 headings for empty text, got %d", len(idx.headings))
	}
	got := idx.At(0)
	if got != nil {
		t.Errorf("At(0) on empty index = %v, want nil", got)
	}
}

func TestExtractMarkdownSections_NoHeadings(t *testing.T) {
	text := "Just some plain text\nwith multiple lines\nbut no headings."
	idx := ExtractMarkdownSections(text)
	if len(idx.headings) != 0 {
		t.Fatalf("expected 0 headings, got %d", len(idx.headings))
	}
	got := idx.At(10)
	if got != nil {
		t.Errorf("At(10) on no-heading index = %v, want nil", got)
	}
}

func TestSectionsForChunk(t *testing.T) {
	text := `# Title
Intro paragraph.

## Section A
Content of section A.

### Subsection A1
Details of A1.

## Section B
Content of section B.
`

	idx := ExtractMarkdownSections(text)

	tests := []struct {
		name     string
		chunk    string
		expected []string
	}{
		{
			name:     "chunk in intro",
			chunk:    "Intro paragraph.",
			expected: []string{"Title"},
		},
		{
			name:     "chunk in Section A",
			chunk:    "Content of section A.",
			expected: []string{"Title", "Section A"},
		},
		{
			name:     "chunk in Subsection A1",
			chunk:    "Details of A1.",
			expected: []string{"Title", "Section A", "Subsection A1"},
		},
		{
			name:     "chunk in Section B",
			chunk:    "Content of section B.",
			expected: []string{"Title", "Section B"},
		},
		{
			name:     "chunk not found",
			chunk:    "nonexistent text",
			expected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := SectionsForChunk(idx, text, tc.chunk, 0)
			if !reflect.DeepEqual(got, tc.expected) {
				t.Errorf("SectionsForChunk(%q) = %v, want %v", tc.chunk, got, tc.expected)
			}
		})
	}
}

func TestSectionsForChunk_NilIndex(t *testing.T) {
	got, _ := SectionsForChunk(nil, "some text", "some", 0)
	if got != nil {
		t.Errorf("expected nil for nil index, got %v", got)
	}
}

func TestExtractMarkdownSections_HeadingLevels(t *testing.T) {
	text := "# H1\n## H2\n### H3\n#### H4\n##### H5\n###### H6\n####### NotAHeading\n"
	idx := ExtractMarkdownSections(text)
	if len(idx.headings) != 6 {
		t.Fatalf("expected 6 headings (levels 1-6), got %d", len(idx.headings))
	}
	for i, h := range idx.headings {
		if h.depth != i+1 {
			t.Errorf("heading %d: depth = %d, want %d", i, h.depth, i+1)
		}
	}
}
