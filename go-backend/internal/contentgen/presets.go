package contentgen

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/justrag/go-backend/internal/logctx"
)

// Preset ist ein Auswahlvorschlag für das Prompt-Feld der Workspace-Dialoge
// („Neue Analyse“, „Dokumentenvergleich“). Ein Preset füllt das Feld vor; die
// Nutzer können es frei überschreiben.
type Preset struct {
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
}

// SiteConfigReader ist der minimale Lesevertrag für die Preset-Auflösung.
// Erfüllt von chat.PGStore und von siteconfig.KBOverlayReader.
type SiteConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// KBConfigOverrideLister lädt die per-KB-Overrides einer Wissensbank.
// Erfüllt von *kbconfig.Store.
type KBConfigOverrideLister interface {
	ListKBOverrides(ctx context.Context, kbID string) (map[string]*string, error)
}

var analysisPresetsDE = []Preset{
	{Label: "Stärken & Schwächen", Prompt: "Analysiere die Stärken und Schwächen des folgenden Themas und belege jede Aussage mit einer Quelle."},
	{Label: "Risiken & Maßnahmen", Prompt: "Nenne die wichtigsten Risiken zum folgenden Thema und je eine konkrete Gegenmaßnahme."},
	{Label: "Zeitliche Entwicklung", Prompt: "Stelle die zeitliche Entwicklung des folgenden Themas dar, chronologisch und mit Datumsangaben."},
	{Label: "Kernaussagen & Belege", Prompt: "Fasse die Kernaussagen zum folgenden Thema zusammen und belege jede mit einem wörtlichen Zitat."},
}

var analysisPresetsEN = []Preset{
	{Label: "Strengths & weaknesses", Prompt: "Analyse the strengths and weaknesses of the following topic, citing a source for every claim."},
	{Label: "Risks & mitigations", Prompt: "List the main risks around the following topic, each with one concrete mitigation."},
	{Label: "Timeline", Prompt: "Lay out how the following topic developed over time, chronologically and with dates."},
	{Label: "Key claims & evidence", Prompt: "Summarise the key claims about the following topic, backing each with a verbatim quote."},
}

var comparisonPresetsDE = []Preset{
	{Label: "Abweichungen zusammenfassen", Prompt: "Fasse zusammen, worin das hochgeladene Dokument von der Wissensbank abweicht, sortiert nach Schwere."},
	{Label: "Nur harte Widersprüche", Prompt: "Nenne ausschließlich harte inhaltliche Widersprüche und lasse formale Abweichungen weg."},
	{Label: "Was fehlt?", Prompt: "Nenne die Punkte, die in der Wissensbank stehen, im hochgeladenen Dokument aber fehlen."},
}

var comparisonPresetsEN = []Preset{
	{Label: "Summarise deviations", Prompt: "Summarise where the uploaded document deviates from the knowledge base, ordered by severity."},
	{Label: "Hard contradictions only", Prompt: "List only substantive contradictions; leave out formal deviations."},
	{Label: "What is missing?", Prompt: "List the points present in the knowledge base but absent from the uploaded document."},
}

// DefaultAnalysisPresets liefert die eingebauten Presets für „Neue Analyse“.
// Unbekannte Sprachen fallen auf Englisch zurück.
func DefaultAnalysisPresets(lang string) []Preset {
	if lang == "de" {
		return analysisPresetsDE
	}
	return analysisPresetsEN
}

// DefaultComparisonPresets liefert die eingebauten Presets für den
// Dokumentenvergleich.
func DefaultComparisonPresets(lang string) []Preset {
	if lang == "de" {
		return comparisonPresetsDE
	}
	return comparisonPresetsEN
}

// fetchPresetRaw liest einen der beiden Preset-Keys über einen literalen
// GetSiteConfigValue-Aufruf je Key — statt eines einzigen
// cfg.GetSiteConfigValue(ctx, key)-Aufrufs mit key als Variable. Das ist,
// was TestEveryRegistryKeyIsRead's AST-Walk (Route 5,
// internal/siteconfig/registry_consistency_test.go) als Lesestelle erkennt;
// key als Parameter durchzureichen wäre für den Walk unsichtbar geblieben —
// dasselbe Muster wie internal/pipeline/preset_base.go's PresetBaseFor für
// workflow_preset. Der default-Zweig deckt den Fall ab, dass resolvePresets
// künftig mit einem dritten Key aufgerufen wird, ohne die beiden echten
// Preset-Keys ihre eigene Route-5-Lesestelle zu nehmen.
func fetchPresetRaw(ctx context.Context, cfg SiteConfigReader, key string) (*string, error) {
	switch key {
	case "workspace_analysis_presets":
		return cfg.GetSiteConfigValue(ctx, "workspace_analysis_presets")
	case "workspace_comparison_presets":
		return cfg.GetSiteConfigValue(ctx, "workspace_comparison_presets")
	default:
		return cfg.GetSiteConfigValue(ctx, key)
	}
}

// resolvePresets liest key über den (ggf. KB-überlagerten) Reader. Ein leerer
// oder unlesbarer Wert fällt auf def zurück: ein Tippfehler im KB-Override
// darf den Dialog nicht ohne Auswahl zurücklassen.
func resolvePresets(ctx context.Context, cfg SiteConfigReader, key string, def []Preset) []Preset {
	if cfg == nil {
		return def
	}
	raw, err := fetchPresetRaw(ctx, cfg, key)
	if err != nil {
		logctx.From(ctx).Warn("workspace.presets.read_failed", "key", key, "error", err)
		return def
	}
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return def
	}
	var out []Preset
	if err := json.Unmarshal([]byte(*raw), &out); err != nil || len(out) == 0 {
		logctx.From(ctx).Warn("workspace.presets.parse_failed", "key", key, "error", err)
		return def
	}
	return out
}
