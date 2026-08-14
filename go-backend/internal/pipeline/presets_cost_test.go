package pipeline

import (
	"context"
	"testing"
)

// pricedByID finds one preset's priced output, failing the test if it is
// missing — every curated preset must appear in PricePresets' output.
func pricedByID(t *testing.T, priced []PricedPreset, id string) PricedPreset {
	t.Helper()
	for _, p := range priced {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("preset %q not found in PricePresets output", id)
	return PricedPreset{}
}

// TestPricePresetsFastCheaperThanHighPrecision asserts the basic ordering a
// cost badge exists to show: the cheapest preset must project fewer LLM
// calls than the most expensive one, on every lane.
func TestPricePresetsFastCheaperThanHighPrecision(t *testing.T) {
	global := fakeReader{vals: map[string]string{}}
	priced, err := PricePresets(context.Background(), global)
	if err != nil {
		t.Fatalf("PricePresets: %v", err)
	}
	fast := pricedByID(t, priced, PresetFast)
	hp := pricedByID(t, priced, PresetHighPrecision)
	for _, lane := range pricedLanes {
		if fast.Costs[lane].EstLLMCalls >= hp.Costs[lane].EstLLMCalls {
			t.Errorf("lane %s: fast (%d LLM calls) should be cheaper than high_precision (%d)",
				lane, fast.Costs[lane].EstLLMCalls, hp.Costs[lane].EstLLMCalls)
		}
	}
}

// TestPricePresetsAgreesWithProject is the assertion that makes the whole
// design worth having: PricePresets must not compute its own numbers. A
// preset's priced cost, on every lane, must equal a direct Project() call
// made over the same bundle — by construction, not because the two arithmetic
// paths happen to agree today.
func TestPricePresetsAgreesWithProject(t *testing.T) {
	global := fakeReader{vals: map[string]string{
		// Non-empty global so bundle-unset keys have something real to fall
		// through to, and the fall-through is exercised by this test too.
		"chat_supervisor_enabled": "true",
	}}
	priced, err := PricePresets(context.Background(), global)
	if err != nil {
		t.Fatalf("PricePresets: %v", err)
	}
	for _, pp := range priced {
		br := bundleReader{bundle: pp.Bundle, global: global}
		for _, lane := range pricedLanes {
			g, err := Project(context.Background(), br, global, lane)
			if err != nil {
				t.Fatalf("Project(%s, %s): %v", pp.ID, lane, err)
			}
			got := pp.Costs[lane]
			if got.EstLLMCalls != g.EstLLMCalls || got.EstLatencyMs != g.EstLatencyMs {
				t.Errorf("preset %s lane %s: PricePresets={%d calls, %dms} Project={%d calls, %dms}",
					pp.ID, lane, got.EstLLMCalls, got.EstLatencyMs, g.EstLLMCalls, g.EstLatencyMs)
			}
		}
	}
}

// TestBundleReaderFallsThroughToGlobalForUnsetKeys is the mechanism the whole
// design leans on: a bundle only states its own vocabulary, so every other
// key — including ones absent from the bundle entirely — must resolve exactly
// as the global reader would answer them, nil included.
func TestBundleReaderFallsThroughToGlobalForUnsetKeys(t *testing.T) {
	global := fakeReader{vals: map[string]string{
		"crag_enabled":            "true",
		"chat_supervisor_enabled": "true",
	}}
	br := bundleReader{bundle: map[string]string{"crag_enabled": "false"}, global: global}

	vals, err := br.GetSiteConfigValues(context.Background(), []string{
		"crag_enabled", "chat_supervisor_enabled", "chat_agentic_enabled",
	})
	if err != nil {
		t.Fatalf("GetSiteConfigValues: %v", err)
	}
	if vals["crag_enabled"] == nil || *vals["crag_enabled"] != "false" {
		t.Errorf("bundle-set key must come from the bundle, got %v", vals["crag_enabled"])
	}
	if vals["chat_supervisor_enabled"] == nil || *vals["chat_supervisor_enabled"] != "true" {
		t.Errorf("key unset in the bundle must fall through to global, got %v", vals["chat_supervisor_enabled"])
	}
	if vals["chat_agentic_enabled"] != nil {
		t.Errorf("key unset everywhere must stay nil (code default applies), got %v", *vals["chat_agentic_enabled"])
	}
}

// TestPricePresetsExpectedTable pins the known table for the current node
// weights (nodes.go) and bundles (presets.go). On the complex lane, "research"
// and "standard" are EXPECTED to project the same total: NodeDecompose is
// complex-reasoning-only and orchestrator-bypassed (Project marks it
// lane_skipped or conditional, never active, on every lane) and
// NodeOrchestrator carries a flat LLMCalls:2 regardless of which orchestrator
// wins — so the stage that actually distinguishes "research" from "standard"
// (query decomposition) never reaches EstLLMCalls on this lane. That is
// Project being honest about what it can see; it is not adjusted for here.
func TestPricePresetsExpectedTable(t *testing.T) {
	global := fakeReader{vals: map[string]string{}}
	priced, err := PricePresets(context.Background(), global)
	if err != nil {
		t.Fatalf("PricePresets: %v", err)
	}

	want := map[string]struct{ lookupEnum, complex int }{
		PresetFast:          {3, 3},
		PresetStandard:      {4, 4},
		PresetNews:          {4, 4},
		PresetResearch:      {6, 4},
		PresetHighPrecision: {11, 8},
	}
	for id, w := range want {
		pp := pricedByID(t, priced, id)
		if got := pp.Costs[LaneLookup].EstLLMCalls; got != w.lookupEnum {
			t.Errorf("%s lookup: got %d LLM calls, want %d", id, got, w.lookupEnum)
		}
		if got := pp.Costs[LaneEnumeration].EstLLMCalls; got != w.lookupEnum {
			t.Errorf("%s enumeration: got %d LLM calls, want %d", id, got, w.lookupEnum)
		}
		if got := pp.Costs[LaneComplex].EstLLMCalls; got != w.complex {
			t.Errorf("%s complex: got %d LLM calls, want %d", id, got, w.complex)
		}
	}
}
