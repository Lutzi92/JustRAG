package openaicompat

import (
	"reflect"
	"testing"

	"github.com/justrag/go-backend/internal/chat"
)

func testSources() []chat.ChatSource {
	return []chat.ChatSource{
		{Index: 1, FileID: "f1", FileName: "handbuch.pdf", Content: "Erster Chunk.", Score: 0.91, Pages: []int{3}, ChunkID: "c1"},
		{Index: 2, FileID: "f2", FileName: "faq.md", Content: "Zweiter Chunk.", Score: 0.72, ChunkID: "c2"},
	}
}

func TestBuildAnnotations(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		want   []annotation
	}{
		{
			name:   "answer without markers yields no annotations",
			answer: "Dazu liegen keine Informationen vor.",
			want:   nil,
		},
		{
			name:   "single marker points at its source",
			answer: "Der Wert ist 42 [1].",
			want: []annotation{
				{Type: "file_citation", FileCitation: fileCitation{FileID: "f1", Filename: "handbuch.pdf", Index: 16}},
			},
		},
		{
			name:   "multi-cite marker yields one annotation per source at the same index",
			answer: "Beides gilt [1, 2].",
			want: []annotation{
				{Type: "file_citation", FileCitation: fileCitation{FileID: "f1", Filename: "handbuch.pdf", Index: 12}},
				{Type: "file_citation", FileCitation: fileCitation{FileID: "f2", Filename: "faq.md", Index: 12}},
			},
		},
		{
			name:   "repeated citation of one source yields one annotation per occurrence",
			answer: "A [1]. B [1].",
			want: []annotation{
				{Type: "file_citation", FileCitation: fileCitation{FileID: "f1", Filename: "handbuch.pdf", Index: 2}},
				{Type: "file_citation", FileCitation: fileCitation{FileID: "f1", Filename: "handbuch.pdf", Index: 9}},
			},
		},
		{
			name:   "out-of-range marker is dropped rather than dangling",
			answer: "Erfunden [9].",
			want:   nil,
		},
		{
			name:   "in-range citations survive alongside an out-of-range one",
			answer: "Echt [2] und erfunden [9].",
			want: []annotation{
				{Type: "file_citation", FileCitation: fileCitation{FileID: "f2", Filename: "faq.md", Index: 5}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildAnnotations(tc.answer, testSources())
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("buildAnnotations(%q) = %+v, want %+v", tc.answer, got, tc.want)
			}
		})
	}
}

// OpenAI defines annotations[].index in characters, not bytes. German answers
// are full of multi-byte runes, so a byte offset would land clients a few
// characters past every citation.
func TestBuildAnnotationsIndexIsRuneOffsetNotByteOffset(t *testing.T) {
	answer := "Größe [1]."
	got := buildAnnotations(answer, testSources())
	if len(got) != 1 {
		t.Fatalf("got %d annotations, want 1", len(got))
	}
	// "Größe " is 6 characters but 8 bytes.
	if got[0].FileCitation.Index != 6 {
		t.Errorf("Index = %d, want 6 (rune offset)", got[0].FileCitation.Index)
	}
	if gotRunes := []rune(answer)[got[0].FileCitation.Index]; gotRunes != '[' {
		t.Errorf("answer rune at Index = %q, want '['", gotRunes)
	}
}

func TestBuildAnnotationsWithoutSources(t *testing.T) {
	if got := buildAnnotations("Der Wert ist 42 [1].", nil); got != nil {
		t.Errorf("buildAnnotations with no sources = %+v, want nil", got)
	}
}

func TestBuildCitations(t *testing.T) {
	got := buildCitations(testSources())
	want := []citation{
		{Content: "Erster Chunk.", Title: "handbuch.pdf", Filepath: "handbuch.pdf", FileID: "f1", ChunkID: "c1", Score: 0.91, Pages: []int{3}},
		{Content: "Zweiter Chunk.", Title: "faq.md", Filepath: "faq.md", FileID: "f2", ChunkID: "c2", Score: 0.72},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildCitations() = %+v, want %+v", got, want)
	}
}

func TestBuildCitationsWithoutSources(t *testing.T) {
	if got := buildCitations(nil); got != nil {
		t.Errorf("buildCitations(nil) = %+v, want nil", got)
	}
}
