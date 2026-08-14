package chat

import (
	"reflect"
	"testing"
)

func TestExtractCitationSpans(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		want   []CitationSpan
	}{
		{
			name:   "no markers",
			answer: "Es gibt keine Belege.",
			want:   nil,
		},
		{
			name:   "single marker",
			answer: "Der Wert ist 42 [1].",
			want:   []CitationSpan{{Start: 16, End: 19, Numbers: []int{1}}},
		},
		{
			name:   "multi-cite marker",
			answer: "Beides gilt [1, 2, 5].",
			want:   []CitationSpan{{Start: 12, End: 21, Numbers: []int{1, 2, 5}}},
		},
		{
			name:   "two markers keep document order",
			answer: "A [2]. B [1].",
			want: []CitationSpan{
				{Start: 2, End: 5, Numbers: []int{2}},
				{Start: 9, End: 12, Numbers: []int{1}},
			},
		},
		{
			name:   "zero is not a citation number",
			answer: "Siehe [0].",
			want:   nil,
		},
		{
			name:   "offsets are byte offsets past multi-byte runes",
			answer: "Größe [1].",
			// "Größe " is 8 bytes — ö and ß are two bytes each, so the byte
			// offset runs two ahead of the rune offset (6).
			want: []CitationSpan{{Start: 8, End: 11, Numbers: []int{1}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractCitationSpans(tc.answer)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ExtractCitationSpans(%q) = %+v, want %+v", tc.answer, got, tc.want)
			}
		})
	}
}

// The span bounds must delimit exactly the marker text, so callers can slice
// the answer with them without an off-by-one.
func TestExtractCitationSpansBoundsDelimitTheMarker(t *testing.T) {
	answer := "Der Wert ist 42 [1, 2] und sonst nichts."
	spans := ExtractCitationSpans(answer)
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if got := answer[spans[0].Start:spans[0].End]; got != "[1, 2]" {
		t.Errorf("answer[Start:End] = %q, want %q", got, "[1, 2]")
	}
}
