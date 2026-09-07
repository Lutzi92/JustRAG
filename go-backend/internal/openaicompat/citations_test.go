package openaicompat

import (
	"reflect"
	"strings"
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

// The annotations ride the CLOSING chunk and their indices are rune offsets
// into the answer — which the client only ever holds as the concatenation of
// the content deltas it received. A degenerate-run trip is the one place the
// two can come apart: the chunk that trips the guard is buffered (so its
// text is in the guarded answer the annotations are computed over) but not
// forwarded, so any citation in the text preceding the run inside that chunk
// would land at a different offset in the client's copy.
//
// This drives the streaming loop's exact guard sequence over the production
// helpers — chat.RunTracker + chat.GuardStreamedAnswer + buildAnnotations —
// and checks every annotation against the CLIENT's assembled text.
func TestAnnotationOffsetsMatchClientTextAfterADegenerateTrip(t *testing.T) {
	// The trip chunk opens with real text carrying a citation and only then
	// collapses into the run: that prefix is what the client used to miss.
	chunks := []string{
		"Größe laut Handbuch [1]. ",
		"Nachtrag [2]: " + strings.Repeat("_", 600) + " weiterer Text",
	}

	tracker := chat.NewRunTracker(400)
	var buffered, client strings.Builder
	for _, c := range chunks {
		buffered.WriteString(c)
		if tracker.Feed(c) {
			break
		}
		client.WriteString(c)
	}
	if !tracker.Tripped() {
		t.Fatal("the guard did not trip — the fixture no longer exercises the trip path")
	}

	guarded, appended := chat.GuardStreamedAnswer(
		buffered.String(), client.String(), tracker.Limit(), "de", "openai_compat")
	client.WriteString(appended)

	if client.String() != guarded {
		t.Fatalf("client text != annotated text:\n client = %q\nguarded = %q", client.String(), guarded)
	}
	annotations := buildAnnotations(guarded, testSources())
	if len(annotations) != 2 {
		t.Fatalf("annotations: got %d, want 2 (one per marker)", len(annotations))
	}
	clientRunes := []rune(client.String())
	for _, a := range annotations {
		idx := a.FileCitation.Index
		if idx < 0 || idx >= len(clientRunes) {
			t.Fatalf("annotation index %d is outside the client's %d-rune text", idx, len(clientRunes))
		}
		if clientRunes[idx] != '[' {
			t.Errorf("client text at annotation index %d is %q, want '[' — the offsets drifted", idx, clientRunes[idx])
		}
	}
}
