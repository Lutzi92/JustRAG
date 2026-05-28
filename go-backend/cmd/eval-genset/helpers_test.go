package main

import (
	"reflect"
	"testing"
)

func TestTokenizeIDs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single", "465896062", []string{"465896062"}},
		{"semicolon space", "612795354; 612795390; 612795381", []string{"612795354", "612795390", "612795381"}},
		{"comma", "1,2,3", []string{"1", "2", "3"}},
		{"mixed", "1, 2;3 4", []string{"1", "2", "3", "4"}},
		{"trailing separators", " 1 ; 2 ; ", []string{"1", "2"}},
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tokenizeIDs(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("tokenizeIDs(%q)=%v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestMapCategoryToQueryType(t *testing.T) {
	cases := []struct {
		category string
		wantQT   string
		wantSkip bool
	}{
		{"single_hop", "lookup", false},
		{"aggregation", "enumeration", false},
		{"multi_hop", "complex_reasoning", false},
		{"comparison", "complex_reasoning", false},
		{"procedural", "complex_reasoning", false},
		{"temporal", "lookup", false},
		{"yes_no", "lookup", false},
		{"negation", "lookup", false},
		{"ambiguous", "lookup", false},
		{"unanswerable", "", true},
		{"", "", false},          // empty: pass-through, no skip
		{"unknown_x", "", false}, // unknown: pass-through, no skip
	}
	for _, c := range cases {
		t.Run(c.category, func(t *testing.T) {
			gotQT, gotSkip := mapCategoryToQueryType(c.category)
			if gotQT != c.wantQT || gotSkip != c.wantSkip {
				t.Errorf("mapCategoryToQueryType(%q)=(%q, %v), want (%q, %v)",
					c.category, gotQT, gotSkip, c.wantQT, c.wantSkip)
			}
		})
	}
}

func TestApplyMapping(t *testing.T) {
	mapping := map[string]string{
		"100": "uuid-100",
		"200": "uuid-200",
		"300": "uuid-300",
	}
	t.Run("all mapped", func(t *testing.T) {
		mapped, missing := applyMapping([]string{"100", "200"}, mapping)
		if !reflect.DeepEqual(mapped, []string{"uuid-100", "uuid-200"}) {
			t.Errorf("mapped=%v", mapped)
		}
		if len(missing) != 0 {
			t.Errorf("missing=%v, want empty", missing)
		}
	})
	t.Run("partial", func(t *testing.T) {
		mapped, missing := applyMapping([]string{"100", "999", "300"}, mapping)
		if !reflect.DeepEqual(mapped, []string{"uuid-100", "uuid-300"}) {
			t.Errorf("mapped=%v", mapped)
		}
		if !reflect.DeepEqual(missing, []string{"999"}) {
			t.Errorf("missing=%v", missing)
		}
	})
	t.Run("none mapped", func(t *testing.T) {
		mapped, missing := applyMapping([]string{"888", "999"}, mapping)
		if len(mapped) != 0 {
			t.Errorf("mapped=%v, want empty", mapped)
		}
		if !reflect.DeepEqual(missing, []string{"888", "999"}) {
			t.Errorf("missing=%v", missing)
		}
	})
}
