package pipeline

import (
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/siteconfig"
)

// This guard exists because a bad preset silently reconfigures somebody's
// knowledge base with one click. Every assertion here is about a way a bundle
// can be wrong without anyone noticing:
//
//   - a key that no longer exists in the per-KB registry (renamed, dropped)
//   - a value the settings endpoint would reject (out of range, wrong type)
//   - a flag combination the apply endpoint would 400 on — including the case
//     where the OFFENDING half sits in the KB's existing state, not in the
//     bundle (that is the realistic failure: an admin who once enabled Self-RAG
//     can never apply "Hohe Präzision" unless the bundle turns it back off)
//   - a bundle that requires a re-ingest of the corpus to mean anything
//   - a key set by one preset and left behind when another is applied
//
// The conflict rules are NOT re-encoded here: siteconfig.ValidateConflicts is
// the single implementation, exactly as it is on the apply path.

// bundleUpdates renders a bundle as the batch the settings endpoint would
// receive.
func bundleUpdates(bundle map[string]string) []siteconfig.KeyValue {
	keys := make([]string, 0, len(bundle))
	for k := range bundle {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]siteconfig.KeyValue, 0, len(keys))
	for _, k := range keys {
		v := bundle[k]
		out = append(out, siteconfig.KeyValue{Key: k, Value: &v})
	}
	return out
}

func TestPresetsMetadata(t *testing.T) {
	got := Presets()
	if len(got) == 0 {
		t.Fatal("Presets() is empty")
	}

	// English words that cannot appear in a German sentence. The registry is
	// bilingual (older Help strings are English), so a copied-over English
	// label is a realistic mistake; these strings are shown to librarians.
	englishTells := []string{" the ", " and ", " with ", " for ", " of ", " to "}

	seen := map[string]bool{}
	for _, p := range got {
		if strings.TrimSpace(p.ID) == "" {
			t.Errorf("preset %+v has an empty ID", p)
			continue
		}
		if seen[p.ID] {
			t.Errorf("duplicate preset ID %q", p.ID)
		}
		seen[p.ID] = true

		if strings.TrimSpace(p.Label) == "" {
			t.Errorf("preset %q has no Label", p.ID)
		}
		desc := strings.TrimSpace(p.Description)
		if desc == "" {
			t.Errorf("preset %q has no Description", p.ID)
			continue
		}
		if len([]rune(desc)) < 40 {
			t.Errorf("preset %q: Description %q is too short to say what it optimises for and what it costs",
				p.ID, desc)
		}
		if !strings.HasSuffix(desc, ".") {
			t.Errorf("preset %q: Description %q should be a sentence ending in a period", p.ID, desc)
		}
		hay := " " + strings.ToLower(desc+" "+p.Label) + " "
		for _, tell := range englishTells {
			if strings.Contains(hay, tell) {
				t.Errorf("preset %q: Label/Description look English (%q); both must be German", p.ID, tell)
			}
		}
		if len(p.Bundle) == 0 {
			t.Errorf("preset %q has an empty Bundle", p.ID)
		}
	}
}

func TestPresetBundleKeysAreConfigurablePerKB(t *testing.T) {
	for _, p := range Presets() {
		for key, val := range p.Bundle {
			if !siteconfig.IsPerKB(key) {
				t.Errorf("preset %q: key %q is not in the per-KB registry", p.ID, key)
				continue
			}
			if err := siteconfig.Validate(key, val); err != nil {
				t.Errorf("preset %q: key %q = %q rejected by the registry: %v", p.ID, key, val, err)
			}
		}
	}
}

// A preset is applied with one click. A bundle that flips an ingest-time knob
// would silently invalidate everything already in the index — the corpus would
// have to be re-ingested before the setting means anything.
func TestPresetBundlesTouchNoReingestKeys(t *testing.T) {
	for _, p := range Presets() {
		for key := range p.Bundle {
			fld, ok := siteconfig.Field(key)
			if !ok {
				continue // reported by TestPresetBundleKeysAreConfigurablePerKB
			}
			if fld.RequiresReingest {
				t.Errorf("preset %q: key %q requires a re-ingest; presets must stay query-time only", p.ID, key)
			}
		}
	}
}

func TestPresetBundlesHaveNoInternalConflicts(t *testing.T) {
	for _, p := range Presets() {
		if err := siteconfig.ValidateConflicts(map[string]*string{}, bundleUpdates(p.Bundle)); err != nil {
			t.Errorf("preset %q conflicts with itself: %v", p.ID, err)
		}
	}
}

// The harder half of the same question: a preset must be applicable to a KB
// whose stored state is already maximally awkward. ValidateConflicts merges the
// batch over the existing rows, so a bundle that enables one half of a pair
// without turning the other half off is un-appliable on exactly the KBs that
// most need a fresh start.
//
// The hostile state is derived from the registry (every boolean on) rather
// than from the pair list, so this test does not re-encode the pairs either.
func TestPresetBundlesApplyOverEveryBooleanEnabled(t *testing.T) {
	existing := map[string]*string{}
	on := "true"
	for _, fld := range siteconfig.All() {
		if fld.Type == siteconfig.FieldBool {
			existing[fld.Key] = &on
		}
	}

	for _, p := range Presets() {
		if err := siteconfig.ValidateConflicts(existing, bundleUpdates(p.Bundle)); err != nil {
			t.Errorf("preset %q cannot be applied to a KB with every boolean enabled: %v", p.ID, err)
		}
	}
}

// Switching presets must be lossless: every key any preset states is stated by
// all of them, so applying a preset overwrites what the previous one set
// instead of leaving half of it behind.
func TestPresetsShareOneVocabulary(t *testing.T) {
	presets := Presets()
	if len(presets) < 2 {
		t.Fatalf("expected several presets, got %d", len(presets))
	}

	vocab := map[string]bool{}
	for _, p := range presets {
		for k := range p.Bundle {
			vocab[k] = true
		}
	}
	for _, p := range presets {
		for k := range vocab {
			if _, ok := p.Bundle[k]; !ok {
				t.Errorf("preset %q does not state %q, which another preset sets — switching to %q would leave it behind",
					p.ID, k, p.ID)
			}
		}
	}
}

// "Standard" is the way back to baseline, so every value in it must be the
// documented code default. defaultOn is the projection's own default table and
// is itself checked against the real read call sites by
// TestDefaultOnMatchesReadBoolDefaults — so this assertion is anchored in the
// code, not in a second hand-written list.
func TestStandardPresetIsTheDocumentedDefault(t *testing.T) {
	p, ok := PresetByID(PresetStandard)
	if !ok {
		t.Fatalf("preset %q is missing", PresetStandard)
	}
	for key, val := range p.Bundle {
		b, err := strconv.ParseBool(val)
		if err != nil {
			t.Errorf("standard: %q = %q is not a boolean; extend this test with its documented default", key, val)
			continue
		}
		if want := defaultOn[key]; b != want {
			t.Errorf("standard: %q = %v but the documented default is %v", key, b, want)
		}
	}
}

func TestPresetByID(t *testing.T) {
	for _, p := range Presets() {
		got, ok := PresetByID(p.ID)
		if !ok {
			t.Errorf("PresetByID(%q) returned false", p.ID)
			continue
		}
		if got.ID != p.ID || got.Label != p.Label {
			t.Errorf("PresetByID(%q) returned %+v", p.ID, got)
		}
	}
	if _, ok := PresetByID("does-not-exist"); ok {
		t.Error("PresetByID returned true for an unknown id")
	}
	if _, ok := PresetByID(""); ok {
		t.Error("PresetByID returned true for the empty id")
	}
}

// The bundles are package state; handing callers the live maps would let one
// caller's edit reconfigure every later apply.
func TestPresetsAreCopies(t *testing.T) {
	first := Presets()
	key := ""
	for k := range first[0].Bundle {
		key = k
		break
	}
	first[0].Bundle[key] = "mutated"
	first[0].Label = "mutated"

	second := Presets()
	if second[0].Bundle[key] == "mutated" {
		t.Error("Presets() hands out the live bundle map")
	}
	if second[0].Label == "mutated" {
		t.Error("Presets() hands out a mutable view of the preset slice")
	}
}

func TestDeviations(t *testing.T) {
	sp := func(s string) *string { return &s }

	b := map[string]string{
		"crag_enabled":                    "true",
		"factcheck_in_chat":               "true",
		"chat_sufficient_context_enabled": "false",
	}

	tests := []struct {
		name      string
		overrides map[string]*string
		want      []string
	}{
		{
			name: "every key matches explicitly",
			overrides: map[string]*string{
				"crag_enabled":                    sp("true"),
				"factcheck_in_chat":               sp("true"),
				"chat_sufficient_context_enabled": sp("false"),
			},
			want: []string{},
		},
		{
			name:      "unset keys fall back to the documented default",
			overrides: map[string]*string{},
			// crag_enabled defaults off, factcheck_in_chat defaults on:
			// only crag_enabled deviates from this bundle.
			want: []string{"crag_enabled"},
		},
		{
			name: "loose booleans compare semantically",
			overrides: map[string]*string{
				"crag_enabled":                    sp("1"),
				"factcheck_in_chat":               sp("TRUE"),
				"chat_sufficient_context_enabled": sp("0"),
			},
			want: []string{},
		},
		{
			name: "changed values are reported, sorted",
			overrides: map[string]*string{
				"crag_enabled":                    sp("false"),
				"factcheck_in_chat":               sp("false"),
				"chat_sufficient_context_enabled": sp("true"),
			},
			want: []string{"chat_sufficient_context_enabled", "crag_enabled", "factcheck_in_chat"},
		},
		{
			name: "a cleared key is treated as unset",
			overrides: map[string]*string{
				"crag_enabled":                    nil,
				"factcheck_in_chat":               nil,
				"chat_sufficient_context_enabled": nil,
			},
			want: []string{"crag_enabled"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Deviations(b, tc.overrides)
			if got == nil {
				t.Fatal("Deviations returned nil; it must return an empty slice")
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("Deviations = %v, want %v", got, tc.want)
			}
		})
	}
}

// A KB that is exactly on a preset must report no deviations — otherwise the
// UI would show "geändert" the second after an apply.
func TestDeviationsAreEmptyRightAfterApply(t *testing.T) {
	for _, p := range Presets() {
		overrides := map[string]*string{}
		for k, v := range p.Bundle {
			val := v
			overrides[k] = &val
		}
		if got := Deviations(p.Bundle, overrides); len(got) != 0 {
			t.Errorf("preset %q reports deviations right after apply: %v", p.ID, got)
		}
	}
}
