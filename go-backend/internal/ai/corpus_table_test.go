package ai

import (
	"testing"
)

func TestMergeUserColumns(t *testing.T) {
	planned := []CorpusColumn{
		{Key: "location", Label: "Location", Type: "string"},
		{Key: "herd_size", Label: "Herd size", Type: "number"},
	}
	user := []CorpusColumn{
		{Key: "farm_id", Label: "Farm ID", Type: "string", DeriveFrom: "filename"},
	}
	merged := MergeUserColumns(planned, user)
	if merged[0].Key != "farm_id" {
		t.Fatalf("user-stated column must come first, got %q", merged[0].Key)
	}
	if len(merged) != 3 {
		t.Fatalf("len = %d, want 3", len(merged))
	}
	merged2 := MergeUserColumns(planned, []CorpusColumn{{Key: "location", Label: "Loc", Type: "string"}})
	if len(merged2) != 2 {
		t.Fatalf("dedup failed: len = %d, want 2", len(merged2))
	}
	if got := MergeUserColumns(planned, nil); len(got) != 2 {
		t.Fatalf("nil user cols: len=%d want 2", len(got))
	}
}
