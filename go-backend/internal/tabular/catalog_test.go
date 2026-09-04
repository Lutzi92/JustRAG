package tabular

import (
	"encoding/json"
	"testing"
)

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
