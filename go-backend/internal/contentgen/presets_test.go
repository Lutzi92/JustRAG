package contentgen

import (
	"context"
	"testing"
)

type fakeCfg struct{ vals map[string]string }

func (f fakeCfg) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if v, ok := f.vals[key]; ok {
		return &v, nil
	}
	return nil, nil
}

func TestDefaultPresetsAreLocalized(t *testing.T) {
	de := DefaultAnalysisPresets("de")
	en := DefaultAnalysisPresets("en")
	if len(de) == 0 || len(en) == 0 {
		t.Fatal("default presets must not be empty")
	}
	if de[0].Label == en[0].Label {
		t.Errorf("de and en presets are identical (%q) — localization missing", de[0].Label)
	}
	if DefaultAnalysisPresets("fr")[0].Label != en[0].Label {
		t.Error("unknown language must fall back to en")
	}
}

func TestResolvePresetsPrefersOverrideThenGlobalThenDefault(t *testing.T) {
	def := DefaultAnalysisPresets("de")

	t.Run("kein Wert gesetzt → Code-Default", func(t *testing.T) {
		got := resolvePresets(context.Background(), fakeCfg{}, "workspace_analysis_presets", def)
		if len(got) != len(def) || got[0].Label != def[0].Label {
			t.Fatalf("got %+v, want defaults", got)
		}
	})

	t.Run("gesetzter Wert schlägt den Default", func(t *testing.T) {
		cfg := fakeCfg{vals: map[string]string{"workspace_analysis_presets": `[{"label":"Eigen","prompt":"P"}]`}}
		got := resolvePresets(context.Background(), cfg, "workspace_analysis_presets", def)
		if len(got) != 1 || got[0].Label != "Eigen" {
			t.Fatalf("got %+v, want the override", got)
		}
	})

	t.Run("kaputtes JSON fällt auf den Default zurück, statt leer zu liefern", func(t *testing.T) {
		cfg := fakeCfg{vals: map[string]string{"workspace_analysis_presets": `[{"label":`}}
		got := resolvePresets(context.Background(), cfg, "workspace_analysis_presets", def)
		if len(got) != len(def) {
			t.Fatalf("got %d presets, want %d (defaults)", len(got), len(def))
		}
	})

	t.Run("leerer String fällt auf den Default zurück", func(t *testing.T) {
		cfg := fakeCfg{vals: map[string]string{"workspace_analysis_presets": ""}}
		got := resolvePresets(context.Background(), cfg, "workspace_analysis_presets", def)
		if len(got) != len(def) {
			t.Fatalf("got %d presets, want %d (defaults)", len(got), len(def))
		}
	})
}
