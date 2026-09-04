package tabular

import (
	"encoding/json"
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

// TestColumnSpecJSONCompat guards the Phase-1/Phase-2 JSON shape: a Phase-2
// writer's extra fields must be ignorable by a reader that only knows the
// Phase-1 shape, and a Phase-1-only payload must decode into the Phase-2
// struct with zero values for the new fields.
func TestColumnSpecJSONCompat(t *testing.T) {
	spec := ColumnSpec{
		Original:    "Fläche [m²]",
		Name:        "flaeche_m2",
		Type:        TypeNumeric,
		Role:        "measure",
		Description: "Fläche in m²",
	}
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var legacy struct {
		Original string     `json:"original"`
		Name     string     `json:"name"`
		Type     ColumnType `json:"type"`
	}
	if err := json.Unmarshal(b, &legacy); err != nil {
		t.Fatalf("unmarshal into legacy struct: %v", err)
	}
	if legacy.Original != spec.Original || legacy.Name != spec.Name || legacy.Type != spec.Type {
		t.Fatalf("legacy decode mismatch: %+v", legacy)
	}

	phase1JSON := []byte(`{"original":"a","name":"a","type":"text"}`)
	var got ColumnSpec
	if err := json.Unmarshal(phase1JSON, &got); err != nil {
		t.Fatalf("unmarshal phase1 json: %v", err)
	}
	if got.Role != "" || got.Description != "" || got.ShadowOf != "" {
		t.Fatalf("new fields not zero on phase-1 decode: %+v", got)
	}
}
