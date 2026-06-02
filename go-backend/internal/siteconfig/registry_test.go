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
		{"rerank_blend_alpha", "1.5", true},   // above Max
		{"rerank_blend_alpha", "abc", true},   // not a float
		{"crag_enabled", "true", false},
		{"crag_enabled", "maybe", true},       // not a bool
		{"chat_graph_routing_path_mode", "ppr", false},
		{"chat_graph_routing_path_mode", "bogus", true}, // not in Enum
		{"top_n_lookup", "20", false},
		{"top_n_lookup", "0", true},           // below Min
		{"jwt_secret", "x", true},             // not a registry key
	}
	for _, c := range cases {
		err := Validate(c.key, c.val)
		if (err != nil) != c.wantErr {
			t.Errorf("Validate(%q,%q) err=%v wantErr=%v", c.key, c.val, err, c.wantErr)
		}
	}
}
