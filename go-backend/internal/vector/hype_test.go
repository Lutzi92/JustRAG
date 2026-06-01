package vector

import "testing"

func TestGetHyPETableName(t *testing.T) {
	cases := map[int]string{
		1536: "chunk_hype_questions",
		2560: "chunk_hype_questions_2560",
		4096: "chunk_hype_questions_4096",
	}
	for dim, want := range cases {
		if got := GetHyPETableName(dim); got != want {
			t.Errorf("GetHyPETableName(%d) = %q, want %q", dim, got, want)
		}
	}
}
