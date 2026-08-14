package pipeline

import (
	"context"
	"fmt"

	"github.com/justrag/go-backend/internal/siteconfig"
)

// bundleReader is a siteconfig.BatchReader backed by one preset's bundle,
// falling through to the deployment's global reader for every key the bundle
// does not state. It exists so PricePresets can hand a preset's OWN
// configuration to Project exactly the way a KB that adopted the preset
// would present it — bundle keys override, everything else (including keys
// the bundle never mentions) resolves exactly as it would for a KB with no
// per-KB overrides at all.
type bundleReader struct {
	bundle map[string]string
	global siteconfig.BatchReader
}

// GetSiteConfigValues satisfies siteconfig.BatchReader.
func (b bundleReader) GetSiteConfigValues(ctx context.Context, keys []string) (map[string]*string, error) {
	out, err := b.global.GetSiteConfigValues(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("pipeline: read global site config for bundle: %w", err)
	}
	for _, k := range keys {
		if v, ok := b.bundle[k]; ok {
			vv := v
			out[k] = &vv
		}
		// Key absent from the bundle: leave out[k] exactly as global
		// answered it, nil included — that is the fall-through.
	}
	return out, nil
}

// pricedLanes is every lane PricePresets projects a preset onto. See
// PricedPreset's doc comment for why all three are priced rather than one.
var pricedLanes = []Lane{LaneLookup, LaneEnumeration, LaneComplex}

// LaneCost is one preset's projected cost on one lane. Both fields are read
// verbatim off a Project() result — PricePresets computes neither.
type LaneCost struct {
	EstLLMCalls  int `json:"estLlmCalls"`
	EstLatencyMs int `json:"estLatencyMs"`
}

// PricedPreset is a curated Preset plus its projected cost, per lane.
//
// Costs is keyed by lane rather than collapsed into one number because the
// lanes genuinely disagree on price, and on the complex lane in particular
// two presets ("research" and "standard") project the SAME total — see
// TestPricePresetsExpectedTable and project.go's prepareChatContextOwned /
// NodeOrchestrator for why. Advertising a single complex-lane figure would
// make "research" look no more expensive than "standard" for the one lane a
// user actually picks "research" for, which is a worse lie than showing
// three honestly-labelled numbers. The caller can always read Costs[LaneComplex]
// alone if a single "what does this cost me" figure is what the UI wants for a
// given surface; the wire shape just never pretends that figure covers every
// lane.
type PricedPreset struct {
	Preset
	Costs map[Lane]LaneCost `json:"costs"`
}

// PricePresets projects every curated preset's own bundle through Project, on
// every lane, and reads EstLLMCalls/EstLatencyMs off the result.
//
// It computes nothing itself. A preset's advertised cost is exactly what
// Project would return for a KB whose per-KB overrides equal the preset's
// bundle, so retuning a node's weight in nodes.go updates every preset's
// price automatically, and a preset's numbers can never disagree with the
// graph it produces — the design point of this function.
func PricePresets(ctx context.Context, global siteconfig.BatchReader) ([]PricedPreset, error) {
	all := Presets()
	out := make([]PricedPreset, 0, len(all))
	for _, p := range all {
		br := bundleReader{bundle: p.Bundle, global: global}
		costs := make(map[Lane]LaneCost, len(pricedLanes))
		for _, lane := range pricedLanes {
			g, err := Project(ctx, br, global, lane)
			if err != nil {
				return nil, fmt.Errorf("pipeline: price preset %q on lane %q: %w", p.ID, lane, err)
			}
			costs[lane] = LaneCost{EstLLMCalls: g.EstLLMCalls, EstLatencyMs: g.EstLatencyMs}
		}
		out = append(out, PricedPreset{Preset: p, Costs: costs})
	}
	return out, nil
}
