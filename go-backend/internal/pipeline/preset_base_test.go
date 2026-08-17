package pipeline

import (
	"context"
	"testing"
)

func TestPresetBaseFor_Unset(t *testing.T) {
	r := fakeReader{vals: map[string]string{}}

	id, found, err := PresetBaseFor(context.Background(), r)
	if err != nil {
		t.Fatalf("PresetBaseFor: %v", err)
	}
	if id != "" || !found {
		t.Fatalf("got (id=%q, found=%v), want (\"\", true) for an unset key", id, found)
	}
}

func TestPresetBaseFor_ExplicitEmptyIsAlsoFreeform(t *testing.T) {
	// "" is the registry's own enum value for "freeform / no base"
	// (registry.go), stored explicitly rather than left unset — must report
	// the same as the unset case.
	r := fakeReader{vals: map[string]string{"workflow_preset": ""}}

	id, found, err := PresetBaseFor(context.Background(), r)
	if err != nil {
		t.Fatalf("PresetBaseFor: %v", err)
	}
	if id != "" || !found {
		t.Fatalf("got (id=%q, found=%v), want (\"\", true) for an explicit empty value", id, found)
	}
}

func TestPresetBaseFor_KnownID(t *testing.T) {
	r := fakeReader{vals: map[string]string{"workflow_preset": PresetHighPrecision}}

	id, found, err := PresetBaseFor(context.Background(), r)
	if err != nil {
		t.Fatalf("PresetBaseFor: %v", err)
	}
	if id != PresetHighPrecision || !found {
		t.Fatalf("got (id=%q, found=%v), want (%q, true)", id, found, PresetHighPrecision)
	}
}

func TestPresetBaseFor_UnknownIDIsDistinguishableFromFreeform(t *testing.T) {
	// A stored id that no longer names a live preset (renamed, removed, or
	// hand-edited) must come back with found=false, id=<the stale value> —
	// never silently folded into the freeform ("", true) result.
	r := fakeReader{vals: map[string]string{"workflow_preset": "retired_preset"}}

	id, found, err := PresetBaseFor(context.Background(), r)
	if err != nil {
		t.Fatalf("PresetBaseFor: %v", err)
	}
	if found {
		t.Fatalf("got found=true for unknown id %q, want false", id)
	}
	if id != "retired_preset" {
		t.Fatalf("got id=%q, want the stale value preserved (%q)", id, "retired_preset")
	}
}

func TestPresetBaseFor_WhitespaceIsTrimmed(t *testing.T) {
	r := fakeReader{vals: map[string]string{"workflow_preset": "  " + PresetFast + "  "}}

	id, found, err := PresetBaseFor(context.Background(), r)
	if err != nil {
		t.Fatalf("PresetBaseFor: %v", err)
	}
	if id != PresetFast || !found {
		t.Fatalf("got (id=%q, found=%v), want (%q, true)", id, found, PresetFast)
	}
}

// PresetBaseFor must be usable with every current preset id, not just one —
// guards against a hardcoded typo matching only by accident.
func TestPresetBaseFor_EveryCuratedPresetResolves(t *testing.T) {
	for _, p := range Presets() {
		r := fakeReader{vals: map[string]string{"workflow_preset": p.ID}}
		id, found, err := PresetBaseFor(context.Background(), r)
		if err != nil {
			t.Fatalf("PresetBaseFor(%q): %v", p.ID, err)
		}
		if id != p.ID || !found {
			t.Errorf("PresetBaseFor(%q) = (%q, %v), want (%q, true)", p.ID, id, found, p.ID)
		}
	}
}
