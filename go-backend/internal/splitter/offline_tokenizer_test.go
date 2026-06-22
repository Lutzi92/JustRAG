package splitter

import (
	"reflect"
	"testing"
)

// TestEmbeddedVocabIsCanonical proves the tokenizer resolves from the embedded
// cl100k_base vocab and produces the exact canonical encoding — never the
// char-estimate fallback. The IDs below are OpenAI's documented cl100k_base
// example, so a non-empty match guarantees the embedded vocab is wired in and
// deterministic without any network access.
func TestEmbeddedVocabIsCanonical(t *testing.T) {
	t.Parallel()
	got := EncodeBPE("tiktoken is great!")
	want := []int{83, 1609, 5963, 374, 2294, 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cl100k_base encoding mismatch: got %v, want %v "+
			"(embedded vocab not loaded → falling back to char estimate?)", got, want)
	}
}

// TestOfflineLoaderParsesEmbeddedVocab verifies the embedded vocabulary parses
// into the full canonical merge table.
func TestOfflineLoaderParsesEmbeddedVocab(t *testing.T) {
	t.Parallel()
	ranks, err := parseTiktokenBpe(cl100kBaseVocab)
	if err != nil {
		t.Fatalf("parse embedded vocab: %v", err)
	}
	if len(ranks) != 100256 {
		t.Fatalf("expected 100256 merge entries, got %d", len(ranks))
	}
}

func TestParseTiktokenBpe_Malformed(t *testing.T) {
	t.Parallel()
	if _, err := parseTiktokenBpe([]byte("no-rank-field\n")); err == nil {
		t.Fatal("expected error for line without a rank field")
	}
	if _, err := parseTiktokenBpe([]byte("!!!notbase64!!! 0\n")); err == nil {
		t.Fatal("expected error for non-base64 token")
	}
}
