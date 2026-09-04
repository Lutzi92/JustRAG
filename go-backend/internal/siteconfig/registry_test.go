package siteconfig

import "testing"

func TestRegistry_IsPerKB(t *testing.T) {
	if !IsPerKB("rerank_blend_alpha") {
		t.Fatal("rerank_blend_alpha should be per-KB overridable")
	}
	if IsPerKB("jwt_secret") {
		t.Fatal("jwt_secret must NOT be per-KB overridable")
	}
	if IsPerKB("totally_unknown_key") {
		t.Fatal("unknown keys are not per-KB")
	}
}

func TestRegistry_AllKeysUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range All() {
		if seen[f.Key] {
			t.Fatalf("duplicate registry key %q", f.Key)
		}
		seen[f.Key] = true
		if f.Type == "" || f.Group == "" || f.Label == "" {
			t.Fatalf("registry key %q missing Type/Group/Label", f.Key)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		key, val string
		wantErr  bool
	}{
		{"rerank_blend_alpha", "0.8", false},
		{"rerank_blend_alpha", "1.5", true}, // above Max
		{"rerank_blend_alpha", "abc", true}, // not a float
		{"crag_enabled", "true", false},
		{"crag_enabled", "maybe", true}, // not a bool
		{"chat_graph_routing_path_mode", "ppr", false},
		{"chat_graph_routing_path_mode", "bogus", true}, // not in Enum
		{"top_n_lookup", "20", false},
		{"top_n_lookup", "0", true}, // below Min
		{"jwt_secret", "x", true},   // not a registry key
	}
	for _, c := range cases {
		err := Validate(c.key, c.val)
		if (err != nil) != c.wantErr {
			t.Errorf("Validate(%q,%q) err=%v wantErr=%v", c.key, c.val, err, c.wantErr)
		}
	}
}

func TestKGExtractionIsPerKBReingest(t *testing.T) {
	if !IsPerKB("kg_extraction_enabled") {
		t.Fatal("kg_extraction_enabled should be a per-KB key")
	}
	fld, ok := Field("kg_extraction_enabled")
	if !ok {
		t.Fatal("Field(kg_extraction_enabled) not found")
	}
	if fld.Type != FieldBool {
		t.Fatalf("type = %q, want bool", fld.Type)
	}
	if !fld.RequiresReingest {
		t.Fatal("kg_extraction_enabled must be flagged RequiresReingest")
	}
	if fld.Group != "Ingestion" {
		t.Fatalf("group = %q, want Ingestion", fld.Group)
	}
	if err := Validate("kg_extraction_enabled", "true"); err != nil {
		t.Fatalf("Validate true: %v", err)
	}
	if err := Validate("kg_extraction_enabled", "notabool"); err == nil {
		t.Fatal("Validate should reject non-bool")
	}
}

func TestFieldJSONValidate(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"leer ist erlaubt (fällt auf Code-Defaults zurück)", "", false},
		{"gültige Preset-Liste", `[{"label":"Risiken","prompt":"Nenne die Risiken."}]`, false},
		{"leeres Array", `[]`, false},
		{"kaputtes JSON", `[{"label":`, true},
		{"Objekt statt Array", `{"label":"x"}`, true},
		{"Eintrag ohne label", `[{"prompt":"x"}]`, true},
		{"Eintrag ohne prompt", `[{"label":"x"}]`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate("workspace_analysis_presets", tc.value)
			if tc.wantErr && err == nil {
				t.Fatalf("Validate(%q) = nil, want error", tc.value)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate(%q) = %v, want nil", tc.value, err)
			}
		})
	}
}

func TestTabularCatalogKeysAreRegistered(t *testing.T) {
	cases := []struct {
		key      string
		min, max float64
	}{
		{"tabular_max_rows", 1000, 5_000_000},
		{"tabular_embed_max_rows", 0, 1_000_000},
		{"tabular_column_values_max_distinct", 100, 100_000},
	}
	for _, c := range cases {
		fld, ok := Field(c.key)
		if !ok {
			t.Fatalf("registry has no %q", c.key)
		}
		if fld.Type != FieldInt {
			t.Errorf("%s.Type = %q, want FieldInt", c.key, fld.Type)
		}
		if fld.Group != "Tabular" {
			t.Errorf("%s.Group = %q, want Tabular", c.key, fld.Group)
		}
		if !fld.RequiresReingest {
			t.Errorf("%s must be flagged RequiresReingest", c.key)
		}
		if fld.Min == nil || *fld.Min != c.min {
			t.Errorf("%s.Min = %v, want %v", c.key, fld.Min, c.min)
		}
		if fld.Max == nil || *fld.Max != c.max {
			t.Errorf("%s.Max = %v, want %v", c.key, fld.Max, c.max)
		}
		if !IsPerKB(c.key) {
			t.Errorf("%s must be per-KB overridable", c.key)
		}
	}
}

func TestPresetKeysAreRegistered(t *testing.T) {
	for _, key := range []string{"workspace_analysis_presets", "workspace_comparison_presets"} {
		fld, ok := Field(key)
		if !ok {
			t.Fatalf("registry has no %q", key)
		}
		if fld.Type != FieldJSON {
			t.Errorf("%s.Type = %q, want FieldJSON", key, fld.Type)
		}
		if !IsPerKB(key) {
			t.Errorf("%s must be per-KB overridable", key)
		}
	}
}
