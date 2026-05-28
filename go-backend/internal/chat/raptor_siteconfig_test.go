package chat

import (
	"context"
	"testing"
)

// These tests lock the Phase F site_config accessor semantics
// (defaults, clamps, fallthrough). They share fakeSiteConfigReader
// with the rest of the chat package — see service_test.go.

func TestRaptorEnabled_DefaultsFalse(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if RaptorEnabled(context.Background(), r) {
		t.Fatal("expected default false")
	}
}

func TestRaptorEnabled_True(t *testing.T) {
	r := &fakeSiteConfigReader{values: map[string]*string{"raptor_enabled": strPtr("true")}}
	if !RaptorEnabled(context.Background(), r) {
		t.Fatal("expected true")
	}
}

func TestRaptorMinChunks_BoundsAndClamp(t *testing.T) {
	cases := []struct {
		stored   string
		expected int
	}{
		{"", 25},      // unset → default
		{"50", 50},    // in-range
		{"3", 25},     // below lo (5) → default
		{"500", 25},   // above hi (200) → default
		{"abc", 25},   // unparseable → default
		{"5", 5},      // lo bound
		{"200", 200},  // hi bound
	}
	for _, tc := range cases {
		var values map[string]*string
		if tc.stored != "" {
			values = map[string]*string{"raptor_min_chunks": strPtr(tc.stored)}
		}
		r := &fakeSiteConfigReader{values: values}
		if got := RaptorMinChunks(context.Background(), r); got != tc.expected {
			t.Errorf("stored=%q want %d got %d", tc.stored, tc.expected, got)
		}
	}
}

func TestRaptorMaxLevels_BoundsAndClamp(t *testing.T) {
	cases := []struct {
		stored   string
		expected int
	}{
		{"", 4}, {"4", 4}, {"2", 2}, {"8", 8}, {"1", 4}, {"10", 4}, {"xyz", 4},
	}
	for _, tc := range cases {
		var values map[string]*string
		if tc.stored != "" {
			values = map[string]*string{"raptor_max_levels": strPtr(tc.stored)}
		}
		r := &fakeSiteConfigReader{values: values}
		if got := RaptorMaxLevels(context.Background(), r); got != tc.expected {
			t.Errorf("stored=%q want %d got %d", tc.stored, tc.expected, got)
		}
	}
}

func TestRaptorBranchingFactor_BoundsAndClamp(t *testing.T) {
	cases := []struct {
		stored   string
		expected int
	}{
		{"", 5}, {"5", 5}, {"2", 2}, {"20", 20}, {"1", 5}, {"100", 5},
	}
	for _, tc := range cases {
		var values map[string]*string
		if tc.stored != "" {
			values = map[string]*string{"raptor_branching_factor": strPtr(tc.stored)}
		}
		r := &fakeSiteConfigReader{values: values}
		if got := RaptorBranchingFactor(context.Background(), r); got != tc.expected {
			t.Errorf("stored=%q want %d got %d", tc.stored, tc.expected, got)
		}
	}
}

func TestRaptorSummaryModel_FallsThroughToTier(t *testing.T) {
	// Per-task unset, model_tier_fast set → model_tier_fast wins.
	r := &fakeSiteConfigReader{values: map[string]*string{
		"model_tier_fast": strPtr("gemma-fast"),
	}}
	if got := RaptorSummaryModel(context.Background(), r); got != "gemma-fast" {
		t.Errorf("want gemma-fast got %q", got)
	}

	// Per-task override beats the tier default.
	r = &fakeSiteConfigReader{values: map[string]*string{
		"raptor_summary_model": strPtr("qwen-special"),
		"model_tier_fast":      strPtr("gemma-fast"),
	}}
	if got := RaptorSummaryModel(context.Background(), r); got != "qwen-special" {
		t.Errorf("want qwen-special got %q", got)
	}

	// Both empty → empty (caller uses KB chat model).
	r = &fakeSiteConfigReader{values: map[string]*string{}}
	if got := RaptorSummaryModel(context.Background(), r); got != "" {
		t.Errorf("want empty got %q", got)
	}
}
