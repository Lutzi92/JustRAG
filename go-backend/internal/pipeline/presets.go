package pipeline

import (
	"sort"
	"strconv"
	"strings"
)

// Preset is one curated starting point for the workflow canvas: a coherent
// set of per-KB config values a KB admin can apply with one click and then
// adjust node by node. See the design rules above `presets` for what may go
// into a Bundle.
type Preset struct {
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Bundle      map[string]string `json:"bundle"`
}

// Preset IDs. These strings are a wire contract (the workflow API payload and
// the per-KB workflow_preset marker), so they are frozen the way NodeID is.
const (
	PresetStandard      = "standard"
	PresetHighPrecision = "high_precision"
	PresetFast          = "fast"
	PresetResearch      = "research"
	PresetNews          = "news"
)

// The presets exist because the target user is a librarian, not an ML
// engineer: asked to reason about rerank_blend_alpha or
// chat_context_compression_threshold they will either do nothing or do
// something incoherent. They live here as a versioned const set rather than as
// database rows so they can be tuned per release and evaluated.
//
// ---------------------------------------------------------------------------
// Design rules the bundles follow. All five were derived from
// internal/siteconfig/registry.go (what is settable, with which default) and
// from nodes.go / project.go (which stage owns a key, what it costs, and what
// it silently depends on):
//
//  1. QUERY-TIME KEYS ONLY. No bundle touches a RequiresReingest key
//     (raptor_enabled, parent_child_enabled, contextual_enrichment,
//     kg_extraction_enabled). A preset is applied with one click; it must not
//     silently invalidate an already-ingested corpus.
//     Guarded by TestPresetBundlesTouchNoReingestKeys.
//
//  2. NO KEY WHOSE "INHERIT" SENTINEL IS UNREPRESENTABLE. top_n_* inherits
//     default_top_k at 0 and rerank_blend_alpha_* inherits the base alpha at
//     -1, but the registry clamps them to [1,200] and [0,1]. A preset could
//     set them and no preset could ever put them back — a one-way door. They
//     stay out of the vocabulary entirely.
//
//  3. ONE VOCABULARY, STATED BY EVERY PRESET. A key enters the vocabulary when
//     at least one preset needs it (either at a non-default value, or because
//     a value left over from another preset would contradict this preset's
//     promise). Once in, EVERY preset states it — otherwise switching from
//     "Hohe Präzision" to "Schnell" would leave the refine loop running.
//     Guarded by TestPresetsShareOneVocabulary.
//
//  4. NO STAGE WITHOUT ITS PREREQUISITE. Three registry keys document a
//     dependency that cannot be satisfied from a per-KB bundle, so no preset
//     enables them:
//     - hype_search_enabled is inert unless hype_enabled ran at ingest time
//     (global-only key, plus a re-ingest).
//     - chat_drift_enabled needs KG community summaries built by an admin
//     batch job (kg_communities_enabled, global-only).
//     - chat_graph_routing_enabled needs kg_extraction_enabled on the KB,
//     which is rule 1's re-ingest.
//     chat_longcontext_enabled is a fourth case: it requires a
//     complex_reasoning query AND chat.PrepareChatContext (service.go:898),
//     and the streaming chat never combines the two — see the paragraph
//     below. No preset enables it either.
//
//  5. NO INVENTED NUMBERS. Every value below is a boolean the registry or the
//     read call site documents. Tuning floats (mmr_lambda,
//     chat_context_compression_threshold, hybrid_dynamic_alpha_sensitivity)
//     would need a value nobody has evaluated per preset, so the bundles leave
//     them at their deployment default.
//
// ---------------------------------------------------------------------------
// One mechanism explains several choices below and is worth stating once.
// project.go's prepareChatContextOwned documents that the STREAMING chat path
// routes every complex_reasoning turn into an orchestrator and therefore never
// reaches chat.PrepareChatContext. CRAG, ECoRAG compression, the
// sufficient-context gate, step-back and decomposition all live inside
// PrepareChatContext, so on the complex lane they only run on the
// non-streaming surfaces (public API, MCP, eval) or when an orchestrator
// errors. Two consequences the bundles depend on:
//
//   - adaptive_routing_enabled skips CRAG on the lookup and enumeration lanes
//     (project.go:439) — the only lanes on which CRAG can run at all. So a
//     preset that wants correction must ALSO state adaptive_routing_enabled =
//     false, and a preset that disables CRAG gains nothing from enabling it
//     (chat/service.go:859 reads it only inside `cragCfg.enabled && …`).
//   - the sufficient-context gate is the one PrepareChatContext stage that the
//     supervisor carries itself (http_send.go:720 → supervisor_chat.go:161),
//     which is why "Hohe Präzision" pairs the two.
var presets = []Preset{
	{
		// The way back to baseline: every vocabulary key at the default its
		// read call site documents (guarded against defaultOn, which
		// defaults_test.go re-derives from those call sites). Only the two
		// documented kill switches are on.
		ID:    PresetStandard,
		Label: "Standard",
		Description: "Auslieferungszustand: Suche, Antwort, Faktencheck und Zitatprüfung — " +
			"ohne zusätzliche Korrektur-, Prüf- oder Planungsstufen. Setzt alles zurück, " +
			"was die anderen Vorlagen verstellen.",
		Bundle: map[string]string{
			"query_cache_enabled":                 "false",
			"recency_boost_enabled":               "false",
			"query_decompose_enabled":             "false",
			"crag_enabled":                        "false",
			"adaptive_routing_enabled":            "false",
			"chat_context_compression_enabled":    "false",
			"chat_sufficient_context_enabled":     "false",
			"chat_drift_enabled":                  "false",
			"chat_supervisor_enabled":             "false",
			"chat_plan_execute_enabled":           "false",
			"chat_plan_execute_dag":               "false",
			"chat_plan_execute_tool_aware":        "false",
			"chat_agentic_enabled":                "false",
			"chat_corpus_table_enabled":           "false",
			"chat_answer_tools_enabled":           "false",
			"factcheck_in_chat":                   "true",
			"citation_validation_enabled":         "true",
			"chat_factuality_verifier_enabled":    "false",
			"chat_factuality_verifier_always_run": "false",
			"chat_self_rag_enabled":               "false",
			"chat_factuality_gate_enabled":        "false",
		},
	},
	{
		// Trustworthiness first, cost accepted. Correction before the answer
		// (CRAG + ECoRAG + the sufficiency gate), verification after it
		// (claim-level verifier on every turn) and a rewrite when the
		// verification flags something.
		ID:    PresetHighPrecision,
		Label: "Hohe Präzision",
		Description: "Für belastbare Antworten: schlechte Treffer werden neu gesucht, " +
			"beleglose Textstellen verworfen, jede Antwort auf Belegbarkeit geprüft und " +
			"bei Bedarf neu geschrieben — deutlich mehr Modellaufrufe und spürbar längere Antwortzeit.",
		Bundle: map[string]string{
			// Ein Cache-Treffer liefert eine gespeicherte Antwort und
			// überspringt Suche UND Antwortmodell (registry Help) — damit
			// laufen auch sämtliche Prüfstufen dieser Vorlage nicht.
			"query_cache_enabled": "false",
			// Ranking soll der Belegkraft folgen, nicht dem Ingest-Datum;
			// docs/retrieval.md nennt den Boost auf statischen Korpora einen
			// Antipattern.
			"recency_boost_enabled": "false",
			// Inert auf dem Streaming-Pfad (siehe Kopfkommentar); die
			// Antwortqualität hängt hier an Korrektur und Prüfung.
			"query_decompose_enabled": "false",
			// Die Korrekturstufe: bewerten (1 Aufruf) und bei schlechter
			// Bewertung umformulieren + erneut suchen (1 Aufruf).
			"crag_enabled": "true",
			// Ohne dieses false überspringt Adaptive Routing CRAG auf genau
			// den beiden Lanes, auf denen CRAG überhaupt läuft.
			"adaptive_routing_enabled": "false",
			// ECoRAG verwirft Textstellen ohne direkte Belegkraft (1
			// Fast-Tier-Aufruf) — weniger Distraktoren im Antwortkontext.
			"chat_context_compression_enabled": "true",
			// Lieber keine Antwort als eine unbelegte: die Prüfung verweigert
			// die Antwort, wenn das gesammelte Material nicht ausreicht.
			"chat_sufficient_context_enabled": "true",
			"chat_drift_enabled":              "false",
			// Der Supervisor ist der einzige Orchestrator, der die
			// Kontext-Prüfung auf der komplexen Lane selbst mitführt
			// (supervisor_chat.go:161) — sonst gälte sie dort nicht.
			"chat_supervisor_enabled":      "true",
			"chat_plan_execute_enabled":    "false",
			"chat_plan_execute_dag":        "false",
			"chat_plan_execute_tool_aware": "false",
			"chat_agentic_enabled":         "false",
			"chat_corpus_table_enabled":    "false",
			// Werkzeug-Ergebnisse stehen in keiner Belegstelle der KB und
			// sind damit weder zitier- noch prüfbar.
			"chat_answer_tools_enabled":   "false",
			"factcheck_in_chat":           "true",
			"citation_validation_enabled": "true",
			// Der Verifier ist die Voraussetzung der Korrektur-Schleife
			// (docs/feature-recipes.md: "must run before the gate").
			"chat_factuality_verifier_enabled": "true",
			// Ohne always_run läuft die Prüfung nur bei einem Verdachtsfall
			// aus der Zitatprüfung (post_response.go:288).
			"chat_factuality_verifier_always_run": "true",
			// Muss stehen: Self-RAG schließt den Verifier aus, und ein in der
			// KB verbliebenes true würde diese Vorlage unanwendbar machen
			// (siteconfig.ValidateConflicts prüft gegen den Bestand).
			"chat_self_rag_enabled":        "false",
			"chat_factuality_gate_enabled": "true",
		},
	},
	{
		// Cheapest coherent position: nothing that costs an LLM call beyond
		// the answer itself. Node weights from nodes.go — CRAG 2 calls,
		// compression 1, sufficiency 1, factcheck 1, verifier 1, refine 2.
		ID:    PresetFast,
		Label: "Schnell",
		Description: "Für kurze Antwortzeiten und geringe Kosten: alle zusätzlichen Prüf- und " +
			"Korrekturstufen bleiben aus, nur die Zitatprüfung läuft weiter — sie braucht " +
			"kein Sprachmodell. Wiederholungsfragen kommen zusätzlich aus dem Cache, " +
			"sofern der Anfragen-Cache in dieser Installation nutzbar ist.",
		Bundle: map[string]string{
			// Ein Treffer spart Suche und Antwortaufruf komplett (registry
			// Help). Passt die Embedder-Dimension nicht zur query_cache-Spalte,
			// schaltet sich der Cache still ab (vector/query_cache.go:75) — er
			// wirkt dann nicht, schadet aber auch nicht.
			"query_cache_enabled":   "true",
			"recency_boost_enabled": "false",
			// 1 Aufruf auf den Nicht-Streaming-Pfaden.
			"query_decompose_enabled": "false",
			// 2 Aufrufe (bewerten + umformulieren), ~1500 ms.
			"crag_enabled": "false",
			// Ohne CRAG ist Adaptive Routing wirkungslos
			// (chat/service.go:859), steht hier aber als Rückstellwert.
			"adaptive_routing_enabled": "false",
			// 1 Aufruf, ~700 ms.
			"chat_context_compression_enabled": "false",
			// 1 Aufruf, ~600 ms.
			"chat_sufficient_context_enabled": "false",
			// Orchestrator-Zeile: alle aus ⇒ der Standardpfad antwortet.
			"chat_drift_enabled":           "false",
			"chat_supervisor_enabled":      "false",
			"chat_plan_execute_enabled":    "false",
			"chat_plan_execute_dag":        "false",
			"chat_plan_execute_tool_aware": "false",
			"chat_agentic_enabled":         "false",
			// 1 Fast-Tier-Aufruf pro betroffener Datei — der teuerste
			// Einzelposten überhaupt.
			"chat_corpus_table_enabled": "false",
			// Vervielfacht die Antwortkosten (bis zu 5 Runden).
			"chat_answer_tools_enabled": "false",
			// 1 Aufruf pro Antwort, auch wenn er die Ausgabe nicht aufhält.
			"factcheck_in_chat": "false",
			// Bleibt an: kein LLM-Aufruf, ~120 ms, und ohne sie gäbe es keine
			// Markierung unbelegter Zitate mehr.
			"citation_validation_enabled":         "true",
			"chat_factuality_verifier_enabled":    "false",
			"chat_factuality_verifier_always_run": "false",
			"chat_self_rag_enabled":               "false",
			// 2 Aufrufe, ~1500 ms.
			"chat_factuality_gate_enabled": "false",
		},
	},
	{
		// Mehrstufig: geplante Teilfragen mit Abhängigkeiten, dazu die
		// Korrekturschleife der Suche auf allen Lanes, die sie erreichen.
		ID:    PresetResearch,
		Label: "Recherche",
		Description: "Für vielschichtige Fragen: die Frage wird in Teilfragen mit Abhängigkeiten " +
			"zerlegt und schrittweise abgearbeitet, schwache Treffer werden neu gesucht — " +
			"die langsamste und teuerste Vorlage.",
		Bundle: map[string]string{
			// Recherchefragen wiederholen sich kaum; ein Cache-Treffer würde
			// die mehrstufige Bearbeitung überspringen.
			"query_cache_enabled":   "false",
			"recency_boost_enabled": "false",
			// Deckt die Pfade ab, auf denen der Orchestrator NICHT greift
			// (öffentliche API, MCP, Auswertung, Orchestrator-Ausfall). Auf
			// dem Streaming-Pfad zerlegt der Planer die Frage selbst
			// (metrics.go: "skipped_existing … Plan-Execute orchestrator").
			"query_decompose_enabled": "true",
			// Erreicht die Lookup- und Aufzählungs-Lane sowie alle
			// Nicht-Streaming-Pfade: schlechte Treffer werden umformuliert
			// und erneut gesucht.
			"crag_enabled":                     "true",
			"adaptive_routing_enabled":         "false",
			"chat_context_compression_enabled": "false",
			"chat_sufficient_context_enabled":  "false",
			"chat_drift_enabled":               "false",
			// Plan-and-Execute steht unter dem Supervisor in der
			// Reihenfolge — der muss aus sein, sonst plant nichts.
			"chat_supervisor_enabled":   "false",
			"chat_plan_execute_enabled": "true",
			// Plan mit Abhängigkeiten statt flacher Liste; laut registry Help
			// kein zusätzlicher Modellaufruf gegenüber dem flachen Plan.
			"chat_plan_execute_dag": "true",
			// Muss stehen, gerade WEIL der DAG-Modus an ist: der Schalter
			// wird als `DAG && tool_aware` geprüft (http_send.go:755). Ein in
			// der KB liegengebliebenes true würde hier still auf den
			// werkzeugbewussten Planer umschalten — freie Argumentlisten je
			// Werkzeug, der einzige Planungspfad ohne strikte
			// Structured-Output-Prüfung. Der Planer soll Suchanfragen planen.
			"chat_plan_execute_tool_aware": "false",
			"chat_agentic_enabled":         "false",
			"chat_corpus_table_enabled":    "false",
			"chat_answer_tools_enabled":    "false",
			"factcheck_in_chat":            "true",
			// Ohne Zitatprüfung startet die Aussagenprüfung nie
			// (post_response.go:234).
			"citation_validation_enabled":         "true",
			"chat_factuality_verifier_enabled":    "false",
			"chat_factuality_verifier_always_run": "false",
			"chat_self_rag_enabled":               "false",
			// Die Korrektur-Schleife braucht markierte Aussagen aus dem
			// Verifier bzw. Self-RAG; ohne den einen wäre sie wirkungslos.
			"chat_factuality_gate_enabled": "false",
		},
	},
	{
		// Frische zuerst. Alles andere bleibt Standard — die übrige
		// Aktualitäts-Maschinerie (chat_recency_listing_enabled,
		// chat_date_awareness_enabled, query_cache_ttl_hours) ist
		// deployment-weit und steht per KB nicht zur Verfügung.
		ID:    PresetNews,
		Label: "Aktuelle Meldungen",
		Description: "Für laufend ergänzte Sammlungen wie RSS-Feeds oder CERT-Meldungen: " +
			"neuere Dokumente werden im Ranking bevorzugt und keine Antwort kommt aus dem " +
			"Zwischenspeicher. Kosten wie im Standard.",
		Bundle: map[string]string{
			// Der Cache hält Antworten 24 Stunden (registry Help) und die
			// Haltedauer ist nicht per KB einstellbar — auf einem stündlich
			// wachsenden Korpus wäre das eine veraltete Antwort.
			"query_cache_enabled": "false",
			// Der Kern der Vorlage: exponentieller Abfall ab files.created_at,
			// Standardgewicht 0,1 bei 14 Tagen Halbwertszeit
			// (vector/config.go, arXiv 2509.19376). Kein Modellaufruf.
			"recency_boost_enabled":               "true",
			"query_decompose_enabled":             "false",
			"crag_enabled":                        "false",
			"adaptive_routing_enabled":            "false",
			"chat_context_compression_enabled":    "false",
			"chat_sufficient_context_enabled":     "false",
			"chat_drift_enabled":                  "false",
			"chat_supervisor_enabled":             "false",
			"chat_plan_execute_enabled":           "false",
			"chat_plan_execute_dag":               "false",
			"chat_plan_execute_tool_aware":        "false",
			"chat_agentic_enabled":                "false",
			"chat_corpus_table_enabled":           "false",
			"chat_answer_tools_enabled":           "false",
			"factcheck_in_chat":                   "true",
			"citation_validation_enabled":         "true",
			"chat_factuality_verifier_enabled":    "false",
			"chat_factuality_verifier_always_run": "false",
			"chat_self_rag_enabled":               "false",
			"chat_factuality_gate_enabled":        "false",
		},
	},
}

// clone deep-copies a preset so callers cannot mutate the package state that
// every later apply reads from.
func (p Preset) clone() Preset {
	out := p
	out.Bundle = make(map[string]string, len(p.Bundle))
	for k, v := range p.Bundle {
		out.Bundle[k] = v
	}
	return out
}

// Presets returns the curated presets in display order.
func Presets() []Preset {
	out := make([]Preset, 0, len(presets))
	for _, p := range presets {
		out = append(out, p.clone())
	}
	return out
}

// PresetByID looks up a single preset. The second return is false for an
// unknown id.
func PresetByID(id string) (Preset, bool) {
	for _, p := range presets {
		if p.ID == id {
			return p.clone(), true
		}
	}
	return Preset{}, false
}

// Deviations reports the bundle keys whose current value differs from what the
// preset states, sorted. It is what lets the canvas say "Vorlage: Schnell (2
// Abweichungen)" after an admin has adjusted a node by hand.
//
// overrides is the KB's resolved configuration in the shape
// siteconfig.BatchReader returns it: a missing entry, or a nil value, means the
// key is set nowhere and the code default applies. For booleans that default is
// resolved through defaultOn — the same table the projection uses, kept honest
// against the real read call sites by TestDefaultOnMatchesReadBoolDefaults. A
// non-boolean key that is unset counts as a deviation, because this package has
// no default table for those.
//
// Keys OUTSIDE the bundle are never reported: a preset states values for its
// own vocabulary only, and the rest of the 51-key surface is the admin's to
// tune without the UI calling it a deviation.
func Deviations(bundle map[string]string, overrides map[string]*string) []string {
	out := []string{}
	for key, want := range bundle {
		if !matchesBundleValue(key, want, overrides[key]) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func matchesBundleValue(key, want string, got *string) bool {
	wantBool, wantIsBool := looseBool(want)
	if got == nil {
		return wantIsBool && wantBool == defaultOn[key]
	}
	if gotBool, gotIsBool := looseBool(*got); wantIsBool && gotIsBool {
		return wantBool == gotBool
	}
	return strings.TrimSpace(*got) == strings.TrimSpace(want)
}

// looseBool mirrors project.go's boolVal parsing so a bundle's "false" and a
// stored "0" compare equal.
func looseBool(s string) (bool, bool) {
	b, err := strconv.ParseBool(strings.TrimSpace(s))
	return b, err == nil
}
