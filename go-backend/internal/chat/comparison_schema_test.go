package chat

import (
	"encoding/json"
	"testing"
)

func TestComparisonFindingsSpecValidJSON(t *testing.T) {
	spec := comparisonFindingsSpec()
	if spec.Name == "" {
		t.Fatal("spec name empty")
	}
	var schema map[string]any
	if err := json.Unmarshal(spec.Schema, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("expected top-level object schema, got %v", schema["type"])
	}
}
