package migrate

import "testing"

func TestParseVectorDim(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		"vector(1536)": 1536,
		"vector(4096)": 4096,
		"vector":       0,
		"text":         0,
	}
	for in, want := range cases {
		if got := parseVectorDim(in); got != want {
			t.Errorf("parseVectorDim(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestUserMemoryHNSWSQL(t *testing.T) {
	t.Parallel()
	// dim <= 2000: plain vector_cosine_ops, partial predicate, no halfvec.
	small := userMemoryHNSWSQL(1536)
	if !umContains(small, `vector_cosine_ops`) || !umContains(small, `WHERE "superseded_by" IS NULL`) {
		t.Errorf("small-dim HNSW SQL wrong: %s", small)
	}
	if umContains(small, "halfvec") {
		t.Errorf("small-dim must not use halfvec: %s", small)
	}
	// 2000 < dim <= 4000: halfvec cast.
	mid := userMemoryHNSWSQL(3072)
	if !umContains(mid, `embedding::halfvec(3072)`) || !umContains(mid, `halfvec_cosine_ops`) {
		t.Errorf("mid-dim HNSW SQL must use halfvec: %s", mid)
	}
	if !umContains(mid, `WHERE "superseded_by" IS NULL`) {
		t.Errorf("mid-dim must preserve partial-index predicate: %s", mid)
	}
	// dim > 4000: exceeds pgvector's halfvec HNSW limit → no index (empty).
	if got := userMemoryHNSWSQL(4096); got != "" {
		t.Errorf("4096-dim must skip the index (empty SQL), got: %s", got)
	}
}

func umContains(s, sub string) bool { return umIndexOf(s, sub) >= 0 }
func umIndexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
