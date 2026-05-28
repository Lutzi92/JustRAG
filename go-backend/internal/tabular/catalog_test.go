package tabular

import (
	"strings"
	"testing"
)

func TestBuildSummaryCard(t *testing.T) {
	cols := []ColumnSpec{
		{Original: "Name", Name: "name", Type: TypeText},
		{Original: "Revenue", Name: "revenue", Type: TypeFloat},
	}
	card := BuildSummaryCard("sales.xlsx", "Q1", "sheet_abc_0", cols, 12345)
	for _, want := range []string{"sales.xlsx", "Q1", "sheet_abc_0", "name", "revenue", "double precision", "12345"} {
		if !strings.Contains(card, want) {
			t.Fatalf("summary card missing %q:\n%s", want, card)
		}
	}
}

func TestTableNameForFile(t *testing.T) {
	got := TableNameForFile("a1b2c3d4-e5f6-7890-abcd-ef0123456789", 2)
	want := "sheet_a1b2c3d4e5f67890abcdef0123456789_2"
	if got != want {
		t.Fatalf("TableNameForFile = %q, want %q", got, want)
	}
}
