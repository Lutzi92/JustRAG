package chatpolicy

import (
	"slices"
	"strings"
	"testing"
)

// TestParseAnswerToolsByRoute_Empty — clearing the key is not an error.
func TestParseAnswerToolsByRoute_Empty(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "  \n"} {
		m, err := ParseAnswerToolsByRoute(raw)
		if err != nil {
			t.Fatalf("ParseAnswerToolsByRoute(%q): %v", raw, err)
		}
		if m != nil {
			t.Fatalf("ParseAnswerToolsByRoute(%q) = %v, want nil", raw, m)
		}
	}
}

// TestParseAnswerToolsByRoute_Valid decodes a document that uses every route
// and both the populated and the deliberately empty list.
func TestParseAnswerToolsByRoute_Valid(t *testing.T) {
	t.Parallel()
	raw := `{"lookup": ["kb_search", "chunk_read"],
	         "enumeration": ["kb_search"],
	         "complex_reasoning": ["kb_search", "graph_search", "calculator"],
	         "global_synthesis": []}`
	m, err := ParseAnswerToolsByRoute(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 4 {
		t.Fatalf("len = %d, want 4", len(m))
	}
	if got := m["lookup"]; len(got) != 2 || got[0] != "kb_search" || got[1] != "chunk_read" {
		t.Fatalf("lookup = %v", got)
	}
	gs, ok := m["global_synthesis"]
	if !ok {
		t.Fatal("global_synthesis key lost")
	}
	if len(gs) != 0 {
		t.Fatalf("global_synthesis = %v, want empty (present) list", gs)
	}
}

// TestParseAnswerToolsByRoute_EmptyObject — "{}" is the documented default and
// must parse to a non-error, no-restriction value.
func TestParseAnswerToolsByRoute_EmptyObject(t *testing.T) {
	t.Parallel()
	m, err := ParseAnswerToolsByRoute("{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := m.Allowlist("lookup", false); ok {
		t.Fatal("empty object must impose no restriction")
	}
}

func TestParseAnswerToolsByRoute_Errors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"not an object (array)", `[]`, "must be a JSON object"},
		{"not an object (string)", `"lookup"`, "must be a JSON object"},
		{"malformed", `{"lookup":`, "invalid"},
		{"trailing data", `{} {}`, "trailing"},
		{"unknown route", `{"smalltalk": ["kb_search"]}`, `unknown route "smalltalk"`},
		{"unknown tool", `{"lookup": ["kb_serch"]}`, `unknown tool "kb_serch"`},
		{"empty tool name", `{"lookup": [""]}`, "unknown tool"},
		{"duplicate tool", `{"lookup": ["kb_search", "kb_search"]}`, "duplicate tool"},
		{"value not an array", `{"lookup": "kb_search"}`, "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseAnswerToolsByRoute(tc.raw)
			if err == nil {
				t.Fatalf("ParseAnswerToolsByRoute(%q): want error", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// TestParseAnswerToolsByRoute_AllKnownToolsAccepted keeps the parser and the
// documented tool list from drifting apart.
func TestParseAnswerToolsByRoute_AllKnownToolsAccepted(t *testing.T) {
	t.Parallel()
	for _, name := range KnownAnswerTools {
		for _, route := range Routes {
			raw := `{"` + route + `": ["` + name + `"]}`
			if _, err := ParseAnswerToolsByRoute(raw); err != nil {
				t.Fatalf("route %q tool %q rejected: %v", route, name, err)
			}
		}
	}
}

func TestValidateAnswerToolsByRouteJSON(t *testing.T) {
	t.Parallel()
	if err := ValidateAnswerToolsByRouteJSON(`{"lookup":["kb_search"]}`); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
	if err := ValidateAnswerToolsByRouteJSON(""); err != nil {
		t.Fatalf("empty document rejected: %v", err)
	}
	if err := ValidateAnswerToolsByRouteJSON(`{"lookup":["nope"]}`); err == nil {
		t.Fatal("unknown tool accepted")
	}
}

// TestAllowlist_Precedence pins that global_synthesis wins over the query
// type's own entry on a global-synthesis turn, and only then.
func TestAllowlist_Precedence(t *testing.T) {
	t.Parallel()
	m := AnswerToolsByRoute{
		"complex_reasoning": {"kb_search", "graph_search"},
		"global_synthesis":  {"kb_search"},
	}
	allow, ok := m.Allowlist("complex_reasoning", true)
	if !ok || len(allow) != 1 || allow[0] != "kb_search" {
		t.Fatalf("global-synthesis turn = (%v, %v), want the global_synthesis entry", allow, ok)
	}
	allow, ok = m.Allowlist("complex_reasoning", false)
	if !ok || len(allow) != 2 {
		t.Fatalf("non-synthesis turn = (%v, %v), want the complex_reasoning entry", allow, ok)
	}
}

// TestAllowlist_Cases covers the remaining resolution paths, including the two
// distinct "empty" outcomes: no entry (ok=false, no restriction) vs an entry
// with an empty list (ok=true, no tools at all).
func TestAllowlist_Cases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		m         AnswerToolsByRoute
		queryType string
		synthesis bool
		wantOK    bool
		wantAllow []string
	}{
		{"nil map", nil, "lookup", false, false, nil},
		{"empty map", AnswerToolsByRoute{}, "lookup", false, false, nil},
		{"missing route", AnswerToolsByRoute{"lookup": {"kb_search"}}, "enumeration", false, false, nil},
		{"present route", AnswerToolsByRoute{"lookup": {"kb_search"}}, "lookup", false, true, []string{"kb_search"}},
		{"empty list is a restriction", AnswerToolsByRoute{"lookup": {}}, "lookup", false, true, []string{}},
		{
			"synthesis turn without a global_synthesis entry falls back to the query type",
			AnswerToolsByRoute{"complex_reasoning": {"kb_search"}}, "complex_reasoning", true, true, []string{"kb_search"},
		},
		{
			"synthesis turn with neither entry",
			AnswerToolsByRoute{"lookup": {"kb_search"}}, "complex_reasoning", true, false, nil,
		},
		{
			"global_synthesis entry ignored on a non-synthesis turn",
			AnswerToolsByRoute{"global_synthesis": {"kb_search"}}, "lookup", false, false, nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			allow, ok := tc.m.Allowlist(tc.queryType, tc.synthesis)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if !slices.Equal(allow, tc.wantAllow) {
				t.Fatalf("allow = %v, want %v", allow, tc.wantAllow)
			}
		})
	}
}

// TestKnownAnswerTools_Shape guards the list itself: 14 unique, non-empty
// names (W6-R15). The registry-side half of the cross-check lives in
// internal/mcp/builtin.
func TestKnownAnswerTools_Shape(t *testing.T) {
	t.Parallel()
	if len(KnownAnswerTools) != 14 {
		t.Fatalf("KnownAnswerTools has %d entries, want 14", len(KnownAnswerTools))
	}
	seen := map[string]bool{}
	for _, n := range KnownAnswerTools {
		if strings.TrimSpace(n) == "" {
			t.Fatal("empty tool name")
		}
		if seen[n] {
			t.Fatalf("duplicate tool name %q", n)
		}
		seen[n] = true
	}
}
