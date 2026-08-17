package siteconfig_test

import (
	"sort"
	"testing"

	"github.com/justrag/go-backend/internal/pipeline"
	"github.com/justrag/go-backend/internal/siteconfig"
)

// TestWorkflowPresetEnumMatchesPipelinePresets pins the workflow_preset
// registry row's hardcoded Enum list against internal/pipeline's real preset
// IDs (pipeline.Presets()). The two cannot be derived from one source: this
// file lives in package siteconfig_test (an EXTERNAL test package) rather
// than an in-package siteconfig test specifically so it CAN import
// internal/pipeline — internal/pipeline imports internal/siteconfig
// (project.go, handler.go), so an in-package test file (package siteconfig)
// importing internal/pipeline back would be a real import cycle ("import
// cycle not allowed in test"), confirmed by a standalone repro before this
// file was written. That is also why registry.go hardcodes the enum values
// instead of calling pipeline.Presets() directly: the production registry
// package can never import pipeline either.
//
// This test is what keeps the hardcoded list honest: add, rename, or remove
// a preset in internal/pipeline/presets.go and this fails until
// workflow_preset's Enum in registry.go is updated to match.
func TestWorkflowPresetEnumMatchesPipelinePresets(t *testing.T) {
	fld, ok := siteconfig.Field("workflow_preset")
	if !ok {
		t.Fatal(`registry has no "workflow_preset" field`)
	}
	if fld.Type != siteconfig.FieldEnum {
		t.Fatalf("workflow_preset.Type = %q, want FieldEnum", fld.Type)
	}

	registryIDs := map[string]bool{}
	sawEmpty := false
	for _, v := range fld.Enum {
		if v == "" {
			sawEmpty = true
			continue
		}
		registryIDs[v] = true
	}
	if !sawEmpty {
		t.Error(`workflow_preset.Enum is missing "" (freeform / no base)`)
	}

	presetIDs := map[string]bool{}
	for _, p := range pipeline.Presets() {
		presetIDs[p.ID] = true
	}

	var onlyInRegistry, onlyInPipeline []string
	for id := range registryIDs {
		if !presetIDs[id] {
			onlyInRegistry = append(onlyInRegistry, id)
		}
	}
	for id := range presetIDs {
		if !registryIDs[id] {
			onlyInPipeline = append(onlyInPipeline, id)
		}
	}
	sort.Strings(onlyInRegistry)
	sort.Strings(onlyInPipeline)

	if len(onlyInRegistry) > 0 {
		t.Errorf("workflow_preset.Enum lists preset IDs pipeline.Presets() does not have: %v — "+
			"a preset was renamed or removed; update registry.go", onlyInRegistry)
	}
	if len(onlyInPipeline) > 0 {
		t.Errorf("pipeline.Presets() has IDs missing from workflow_preset.Enum: %v — "+
			"a new preset was added to internal/pipeline/presets.go; add its ID to "+
			"registry.go's workflow_preset row", onlyInPipeline)
	}
}

// TestWorkflowPresetIsLastInTheRegistry pins the row's POSITION, not just
// its enum.
//
// The registry's order is the flat settings form's order (KbSettingsPanel
// renders groups in registry order), and workflow_preset is not a behaviour
// knob at all: it is the provenance marker the workflow canvas renders as
// „Basis: … · N Abweichungen". Leading the accordion with a display-only field
// put it ahead of every setting that actually changes how answers are produced.
// It now sits last, next to the other rows nobody needs on their first visit.
//
// Asserted as "last", not merely "not first", so the row cannot drift back up
// unnoticed one insertion at a time.
func TestWorkflowPresetIsLastInTheRegistry(t *testing.T) {
	all := siteconfig.All()
	if len(all) == 0 {
		t.Fatal("registry is empty")
	}
	if got := all[len(all)-1].Key; got != "workflow_preset" {
		t.Errorf("last registry row is %q, want workflow_preset — the provenance marker "+
			"must not lead (or sit among) the behaviour knobs in the settings form", got)
	}
	if all[0].Key == "workflow_preset" {
		t.Error("workflow_preset heads the registry again: a display-only marker would " +
			"lead the settings accordion")
	}
}
