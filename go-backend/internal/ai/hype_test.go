package ai

import "testing"

func TestParseHyPEQuestions(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		max  int
		want []string
	}{
		{"plain array", `["What is X?","How does Y work?"]`, 5, []string{"What is X?", "How does Y work?"}},
		{"fenced", "```json\n[\"Q1\",\"Q2\"]\n```", 5, []string{"Q1", "Q2"}},
		{"caps", `["a","b","c","d"]`, 2, []string{"a", "b"}},
		{"trims+drops empty", `[" keep ","",  "  "]`, 5, []string{"keep"}},
		{"garbage", `not json`, 5, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseHyPEQuestions(tc.raw, tc.max)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d]=%q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
