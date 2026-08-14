package siteconfig

import (
	"fmt"
	"strconv"
	"strings"
)

// FieldType is the value domain of a per-KB config field.
type FieldType string

const (
	FieldBool   FieldType = "bool"
	FieldInt    FieldType = "int"
	FieldFloat  FieldType = "float"
	FieldString FieldType = "string"
	FieldEnum   FieldType = "enum"
)

// KBConfigField describes one per-KB-overridable site_config key. It drives the
// overlay-reader membership check, save-time validation, and the settings UI.
type KBConfigField struct {
	Key              string    `json:"key"`
	Type             FieldType `json:"type"`
	Group            string    `json:"group"`
	Label            string    `json:"label"`
	Help             string    `json:"help"`
	Min              *float64  `json:"min,omitempty"`
	Max              *float64  `json:"max,omitempty"`
	Enum             []string  `json:"enum,omitempty"`
	RequiresReingest bool      `json:"requiresReingest,omitempty"`
}

func f(v float64) *float64 { return &v }

// kbConfigRegistry is the curated, ordered set of per-KB-overridable keys.
// Expanding the surface = adding a row here (and, for keys read by the eval
// snapshot, ensuring admineval.snapshotConfigKeys also lists them — there is a
// cross-check test in Plan 2). Operational/security keys are deliberately
// absent so an api-user can never set them per-KB.
var kbConfigRegistry = []KBConfigField{
	// --- Retrieval ---
	{Key: "query_cache_enabled", Type: FieldBool, Group: "Retrieval", Label: "Anfragen-Cache", Help: "Beantwortet inhaltlich sehr ähnliche Wiederholungsfragen direkt aus dem Cache (Kosinus-Ähnlichkeit ≥ 0,96, 24 Stunden gültig) statt neu zu suchen und zu antworten — spart bei einem Treffer die komplette Suche samt Antwort-Modellaufruf. Prüft die Cache-Trefferquote über die ohnehin berechnete Anfrage-Einbettung, kein zusätzlicher Modellaufruf beim Prüfen selbst. Standardmäßig aus."},
	{Key: "rerank_blend_alpha", Type: FieldFloat, Group: "Retrieval", Label: "Reranker blend α", Help: "Weight of reranker vs RRF (0=pure RRF, 1=pure reranker).", Min: f(0), Max: f(1)},
	{Key: "rerank_blend_alpha_lookup", Type: FieldFloat, Group: "Retrieval", Label: "Reranker α (lookup)", Help: "Per-route override for lookup queries. Empty inherits the base α.", Min: f(0), Max: f(1)},
	{Key: "rerank_blend_alpha_enumeration", Type: FieldFloat, Group: "Retrieval", Label: "Reranker α (enumeration)", Help: "Per-route override for enumeration queries.", Min: f(0), Max: f(1)},
	{Key: "rerank_blend_alpha_complex_reasoning", Type: FieldFloat, Group: "Retrieval", Label: "Reranker α (complex)", Help: "Per-route override for complex-reasoning queries.", Min: f(0), Max: f(1)},
	{Key: "mmr_lambda", Type: FieldFloat, Group: "Retrieval", Label: "MMR λ", Help: "Top-k diversity vs relevance (1=pure relevance).", Min: f(0), Max: f(1)},
	{Key: "top_n_lookup", Type: FieldInt, Group: "Retrieval", Label: "Top-N (lookup)", Help: "Candidate pool size for lookup queries.", Min: f(1), Max: f(200)},
	{Key: "top_n_enumeration", Type: FieldInt, Group: "Retrieval", Label: "Top-N (enumeration)", Help: "Candidate pool size for enumeration queries.", Min: f(1), Max: f(200)},
	{Key: "top_n_complex_reasoning", Type: FieldInt, Group: "Retrieval", Label: "Top-N (complex)", Help: "Candidate pool size for complex-reasoning queries.", Min: f(1), Max: f(200)},
	{Key: "bm25_simple_arm_enabled", Type: FieldBool, Group: "Retrieval", Label: "BM25 simple arm", Help: "Second unstemmed tsvector arm; recovers terms the stemmer destroys."},
	{Key: "bm25_tiered_boost_enabled", Type: FieldBool, Group: "Retrieval", Label: "BM25 tiered boost", Help: "ts_rank ×100 strict / ×10 OR-floor."},
	{Key: "hybrid_dynamic_alpha_enabled", Type: FieldBool, Group: "Retrieval", Label: "Dynamic α", Help: "Per-query α shift from BPE-token rarity."},
	{Key: "hybrid_dynamic_alpha_sensitivity", Type: FieldFloat, Group: "Retrieval", Label: "Dynamic α sensitivity", Help: "Caps shift magnitude; 0 disables.", Min: f(0), Max: f(1)},
	{Key: "hype_search_enabled", Type: FieldBool, Group: "Retrieval", Label: "HyPE-Suche", Help: "Zusätzlicher Such-Kanal über hypothetische Fragen, die beim Ingest pro Textabschnitt generiert wurden (HyPE). Wirkungslos (liefert einfach nichts zusätzlich, kein Fehler), wenn beim Ingest keine HyPE-Fragen erzeugt wurden — dafür ist der separate Ingest-Schalter hype_enabled nötig (hier nicht einstellbar) und anschließend ein erneuter Ingest. Kein Modellaufruf zur Anfragezeit. Standardmäßig aus."},
	{Key: "step_back_enabled", Type: FieldBool, Group: "Retrieval", Label: "Step-back prompting", Help: "LLM-generated broader query for complex queries."},
	{Key: "query_decompose_enabled", Type: FieldBool, Group: "Retrieval", Label: "Sub-question decomposition", Help: "Splits complex queries into 2–4 distinct sub-questions (DecomposeRAG)."},
	{Key: "recency_boost_enabled", Type: FieldBool, Group: "Retrieval", Label: "Aktualitäts-Boost", Help: "Bevorzugt neuere Dateien im Ranking (exponentieller Abfall ab Ingest-Datum, Standard-Halbwertszeit 14 Tage). Sinnvoll für RSS-/Confluence-KBs mit laufenden Updates; bei statischen Dokumentensammlungen bringt es nichts und sorgt bei erneutem Hochladen einer Datei für Ranking-Schwankungen, weil das Datum ab dem Ingest-Zeitpunkt neu zählt. Kein Modellaufruf. Standardmäßig aus."},
	{Key: "chat_feedback_boost_enabled", Type: FieldBool, Group: "Retrieval", Label: "Feedback-Boost", Help: "Gewichtet Textstellen hoch, die bereits in positiv bewerteten Antworten zitiert wurden (Daumen-hoch/-runter-Signale). Kein Modellaufruf. Standardmäßig aus."},

	// --- Corrective / compression ---
	{Key: "crag_enabled", Type: FieldBool, Group: "Corrective", Label: "Corrective RAG", Help: "Grade retrieved chunks and optionally rewrite."},
	{Key: "adaptive_routing_enabled", Type: FieldBool, Group: "Corrective", Label: "Adaptive routing", Help: "Skip CRAG for lookup queries."},
	{Key: "chat_context_compression_enabled", Type: FieldBool, Group: "Corrective", Label: "ECoRAG compression", Help: "Drop chunks lacking direct evidence (1 fast-tier LLM call)."},
	{Key: "chat_context_compression_threshold", Type: FieldFloat, Group: "Corrective", Label: "Compression threshold", Help: "Drop chunks scoring below this.", Min: f(0), Max: f(1)},
	{Key: "chat_sufficient_context_enabled", Type: FieldBool, Group: "Corrective", Label: "Kontext-Prüfung", Help: "Prüft vor der Antwortgenerierung per LLM-Aufruf, ob die gesammelten Textstellen als Ganzes überhaupt ausreichen — sonst wird die Antwort verweigert. Standardmäßig aus."},

	// --- Orchestrator ---
	{Key: "chat_drift_enabled", Type: FieldBool, Group: "Orchestrator", Label: "DRIFT (globale Synthese)", Help: "Für globale Synthesefragen: liest die KG-Themen-Zusammenfassungen als Grundlage, lässt ein Fast-Tier-Modell dazu passende Teilfragen erzeugen (1 zusätzlicher Modellaufruf) und durchsucht die KB leicht pro Teilfrage (Standard 4, bis zu 8 — je Teilfrage keine weiteren Modellaufrufe). Setzt vorab gebaute KG-Themen-Zusammenfassungen voraus (kg_communities_enabled + ein admin-seitiger Build-Lauf, hier nicht einstellbar) — ohne das läuft es ohne Grundlage weiter (primerless), aber nicht mit Fehler. Sticht bei globalen Synthesefragen vor allen anderen Orchestratoren. Standardmäßig aus."},
	{Key: "chat_supervisor_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Supervisor orchestrator", Help: "One classification → specialist → answer."},
	{Key: "chat_plan_execute_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Plan-and-Execute", Help: "Plan → iterate → generate."},
	{Key: "chat_plan_execute_dag", Type: FieldBool, Group: "Orchestrator", Label: "Plan-and-Execute: DAG-Planung", Help: "Wechselt bei aktivem Plan-and-Execute vom flachen Teilfragen-Plan zu einem Plan mit Abhängigkeiten zwischen den Schritten (DAG). Ersetzt den flachen Plan, kein zusätzlicher Modellaufruf gegenüber ihm — beide sind ein einzelner Planungsaufruf. Wirkt nur, wenn Plan-and-Execute aktiv ist. Standardmäßig aus."},
	{Key: "chat_plan_execute_tool_aware", Type: FieldBool, Group: "Orchestrator", Label: "Plan-and-Execute: Werkzeug-Planer", Help: "Der Planer wählt beim Planen zusätzlich passende Werkzeuge (MCP, z. B. Rechner, SQL) statt nur KB-Suchanfragen zu formulieren — ersetzt den regulären Plan, kein zusätzlicher Modellaufruf. Ohne Aktivierung bleibt es beim reinen Such-Verhalten. Wirkt nur, wenn Plan-and-Execute UND der DAG-Modus (chat_plan_execute_dag) aktiv sind — ohne DAG-Modus bleibt dieser Schalter ohne jede Wirkung. Standardmäßig aus."},
	{Key: "chat_agentic_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Agentic orchestrator", Help: "Hop-1 → critique → optional follow-up hops."},
	{Key: "chat_agentic_max_hops", Type: FieldInt, Group: "Orchestrator", Label: "Agentic: max. Hops", Help: "Obergrenze für Suchrunden im Agentic-Orchestrator: 1 erste Suche plus bis zu (Wert−1) weitere, vom Kritik-Modell ausgelöste Nachsuchen. Standard 3 (1 Suche + bis zu 2 Kritik-Modellaufrufe mit Nachsuche), gültiger Bereich 1–5. Bei 1 entfällt die Kritik komplett (kein zusätzlicher Modellaufruf); jeder weitere Hop kostet 1 zusätzlichen Fast-Tier-Modellaufruf für die Kritik. Wirkt nur, wenn der Agentic-Orchestrator aktiv ist.", Min: f(1), Max: f(5)},
	{Key: "chat_longcontext_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Long-context (System 2)", Help: "Wide-retrieval routing for global-synthesis queries. CAUTION: high per-turn cost."},
	// chat_kb_router_enabled is deliberately NOT registered here: the router
	// picks WHICH KB answers the turn, so it is structurally global, not
	// per-KB. maybeRouteKB (internal/chat/http_send.go:198) runs BEFORE
	// h.forKB installs the KB overlay (:203), so a per-KB override could
	// never be read — and reading it after forKB would mean "KB A's override
	// decides whether we may route away from KB A", which is incoherent.
	// Registering it also flipped internal/pipeline's NodeKBRouter (whose
	// Keys[0] this is) to Editable:true, making the workflow canvas advertise
	// a per-KB control that cannot exist. Set it globally in the admin panel.
	{Key: "chat_corpus_table_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Corpus comparison tables", Help: "Auto-detect 'compare/list all X across these documents' queries and answer with a map-reduce structured table. CAUTION: one fast-tier LLM call per in-scope file."},
	{Key: "chat_corpus_table_max_files", Type: FieldInt, Group: "Orchestrator", Label: "Corpus table max files", Help: "Hard cap on files processed per corpus-table query (default 50). Excess files are dropped and the table is flagged truncated.", Min: f(1), Max: f(500)},
	{Key: "chat_corpus_table_concurrency", Type: FieldInt, Group: "Orchestrator", Label: "Corpus table concurrency", Help: "Max parallel per-file extraction calls (default 6).", Min: f(1), Max: f(50)},
	{Key: "chat_corpus_table_model", Type: FieldString, Group: "Orchestrator", Label: "Corpus table model", Help: "Per-task fast-tier model override for column planning + per-file extraction. Falls through model_tier_fast, then the KB chat model."},
	{Key: "chat_corpus_table_router_llm_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Corpus table LLM router", Help: "After the keyword router fires, confirm with a fast-tier yes/no LLM call before running the expensive map-reduce (default on). Turn off to audit the keyword firing rate."},

	// --- Antwort ---
	{Key: "chat_answer_tools_enabled", Type: FieldBool, Group: "Antwort", Label: "Werkzeuge zur Antwortzeit", Help: "Erlaubt dem Antwortmodell, während der Antwortformulierung Werkzeuge aufzurufen (z. B. Rechner, Stichwortsuche, SQL) und mit dem Ergebnis weiterzuschreiben. Statt eines einzelnen Antwort-Modellaufrufs entstehen so bis zu chat_answer_tools_max_rounds Modellaufrufe (Standard 5, Bereich 1–10) — pro Werkzeugaufruf eine weitere Runde. CAUTION: kann die Antwortkosten je nach Werkzeugnutzung vervielfachen. Standardmäßig aus."},
	{Key: "chat_answer_tools_max_rounds", Type: FieldInt, Group: "Antwort", Label: "Werkzeuge zur Antwortzeit: max. Runden", Help: "Obergrenze für Werkzeug-Runden des Antwortmodells — danach wird ohne weiteren Werkzeugaufruf abgeschlossen. Standard 5, gültiger Bereich 1–10; jede Runde ist ein eigener Modellaufruf. Wirkt nur, wenn Werkzeuge zur Antwortzeit (chat_answer_tools_enabled) aktiv sind.", Min: f(1), Max: f(10)},

	// --- Knowledge graph ---
	{Key: "chat_graph_routing_enabled", Type: FieldBool, Group: "Knowledge graph", Label: "Graph routing", Help: "Emit graph-traversal decision; needs kg_extraction on the KB."},
	{Key: "chat_graph_routing_inject_chunks", Type: FieldBool, Group: "Knowledge graph", Label: "Graph chunk injection", Help: "Fold subgraph chunks into RRF."},
	{Key: "chat_graph_routing_path_mode", Type: FieldEnum, Group: "Knowledge graph", Label: "Graph traversal mode", Help: "neighbors | ppr | paths.", Enum: []string{"neighbors", "ppr", "paths"}},

	// --- Prüfung (Group matches the workflow canvas's own "Prüfung" node
	// group in internal/pipeline/nodes.go, so the settings form and the
	// diagram name the same cluster the same way) ---
	{Key: "factcheck_in_chat", Type: FieldBool, Group: "Prüfung", Label: "Faktencheck", Help: "Prüft die fertige Antwort als Ganzes auf unbelegte oder widersprochene Aussagen (1 LLM-Aufruf pro Antwort). Standardmäßig aktiv."},
	{Key: "chat_factuality_verifier_enabled", Type: FieldBool, Group: "Prüfung", Label: "Vertiefte Aussagenprüfung", Help: "Prüft einzelne Aussagen der Antwort gegen die Belegstellen, wenn die Zitatprüfung ein Zitat nicht bestätigen konnte (1 zusätzlicher LLM-Aufruf). Schließt sich mit Self-RAG gegenseitig aus. Standardmäßig aus."},
	{Key: "chat_factuality_verifier_always_run", Type: FieldBool, Group: "Prüfung", Label: "Aussagenprüfung immer ausführen", Help: "Lässt die Prüfung bei jeder Antwort laufen, auch ohne Verdachtsfall aus der Zitatprüfung (höherer LLM-Kostenaufwand). Wirkt, wenn die vertiefte Aussagenprüfung ODER Self-RAG aktiv ist — bei Self-RAG ist das genau der Schalter, mit dem die Selbstprüfung in jeder Antwort läuft. Setzt weiterhin voraus, dass die Prüfung überhaupt angestoßen wird, also Zitatprüfung oder vertiefte Aussagenprüfung aktiv ist. Standardmäßig aus."},
	{Key: "chat_self_rag_enabled", Type: FieldBool, Group: "Prüfung", Label: "Selbstprüfung (Self-RAG)", Help: "Vereinheitlichte Prüfung (Relevanz + Beleglage + Nützlichkeit) in einem LLM-Aufruf; ERSETZT die vertiefte Aussagenprüfung und schließt sich mit ihr gegenseitig aus. Kann nicht laufen, solange die Zitatprüfung aus ist: diese Prüfung wird nur zusammen mit der Zitatprüfung angestoßen — auch „Aussagenprüfung immer ausführen“ hilft dann nicht. Mit aktiver Zitatprüfung läuft sie, sobald eine Quellenangabe nicht belegt werden konnte, oder bei jeder Antwort, wenn „Aussagenprüfung immer ausführen“ gesetzt ist. Standardmäßig aus."},
	{Key: "chat_factuality_gate_enabled", Type: FieldBool, Group: "Prüfung", Label: "Korrektur-Schleife", Help: "Schreibt die Antwort neu und prüft sie danach erneut (2 zusätzliche LLM-Aufrufe, ca. 1500 ms länger), wenn die vertiefte Aussagenprüfung oder Self-RAG nicht belegte Aussagen gefunden hat. Standardmäßig aus."},
	{Key: "citation_validation_enabled", Type: FieldBool, Group: "Prüfung", Label: "Zitatprüfung", Help: "Prüft jede Quellenangabe zuerst deterministisch gegen den zitierten Text (Wortfolgen-Abgleich, kein Modellaufruf). Bleibt ein Zitat dabei unbelegt, folgt ein Ähnlichkeitsvergleich per Einbettung: bis zu zwei Einbettungs-Aufrufe je unbelegtem Zitat (Antwortsatz + zitierter Abschnitt, innerhalb einer Antwort zwischengespeichert). Das ist ein Modell-API-Aufruf, aber kein LLM-Antwortaufruf; er ist ab Werk aktiv (Schwelle citation_validation_semantic_threshold, Standard 0,85; 0 schaltet ihn ab). Markiert unbelegte Zitate; ein Verdachtsfall löst die vertiefte Aussagenprüfung bzw. Self-RAG aus — beide werden ausschließlich hierüber angestoßen. Standardmäßig aktiv."},

	// --- Ingestion (re-ingest required) ---
	{Key: "raptor_enabled", Type: FieldBool, Group: "Ingestion", Label: "RAPTOR indexing", Help: "Hierarchical summary trees. Mutually exclusive with parent-child.", RequiresReingest: true},
	{Key: "parent_child_enabled", Type: FieldBool, Group: "Ingestion", Label: "Parent-child chunking", Help: "Small-to-big retrieval. Mutually exclusive with RAPTOR.", RequiresReingest: true},
	{Key: "contextual_enrichment", Type: FieldBool, Group: "Ingestion", Label: "Contextual enrichment", Help: "Anthropic-style 1-sentence chunk prefix at ingest.", RequiresReingest: true},
	{Key: "kg_extraction_enabled", Type: FieldBool, Group: "Ingestion", Label: "Knowledge-graph extraction (graphrag)", Help: "Extract entities + relations at ingest to build the per-KB knowledge graph. Required before graph routing can use this KB.", RequiresReingest: true},
}

// byKey indexes the registry for O(1) lookup.
var byKey = func() map[string]KBConfigField {
	m := make(map[string]KBConfigField, len(kbConfigRegistry))
	for _, fld := range kbConfigRegistry {
		m[fld.Key] = fld
	}
	return m
}()

// All returns the ordered registry (safe to expose as JSON to the UI).
func All() []KBConfigField { return kbConfigRegistry }

// Field returns the registry entry for key, if present.
func Field(key string) (KBConfigField, bool) {
	fld, ok := byKey[key]
	return fld, ok
}

// IsPerKB reports whether key may be overridden per-KB.
func IsPerKB(key string) bool {
	_, ok := byKey[key]
	return ok
}

// IsPerAgent reports whether key may be overridden per-agent (user-created
// agents). The per-agent surface is the per-KB registry minus ingestion-time
// keys — RequiresReingest knobs act at ingest, so overriding them per chat
// turn would be a silent no-op that confuses users.
func IsPerAgent(key string) bool {
	fld, ok := byKey[key]
	return ok && !fld.RequiresReingest
}

// AgentFields returns the ordered per-agent-overridable registry subset
// (safe to expose as JSON to the agent-form UI).
func AgentFields() []KBConfigField {
	out := make([]KBConfigField, 0, len(kbConfigRegistry))
	for _, fld := range kbConfigRegistry {
		if !fld.RequiresReingest {
			out = append(out, fld)
		}
	}
	return out
}

// ValidateAgentConfig checks every key/value in an agent's config map:
// membership in the per-agent surface plus the registry's type/range rules.
func ValidateAgentConfig(cfg map[string]string) error {
	for k, v := range cfg {
		if !IsPerAgent(k) {
			return fmt.Errorf("%q is not a per-agent configurable key", k)
		}
		if err := Validate(k, v); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks that key is a registry key and value parses to its type and
// satisfies any Min/Max/Enum constraint. Returns nil when valid.
func Validate(key, value string) error {
	fld, ok := byKey[key]
	if !ok {
		return fmt.Errorf("%q is not a per-KB configurable key", key)
	}
	v := strings.TrimSpace(value)
	switch fld.Type {
	case FieldBool:
		switch strings.ToLower(v) {
		case "true", "false", "1", "0":
			return nil
		}
		return fmt.Errorf("%q must be a boolean", key)
	case FieldInt:
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%q must be an integer", key)
		}
		return checkRange(key, float64(n), fld)
	case FieldFloat:
		fv, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("%q must be a number", key)
		}
		return checkRange(key, fv, fld)
	case FieldEnum:
		for _, e := range fld.Enum {
			if v == e {
				return nil
			}
		}
		return fmt.Errorf("%q must be one of %v", key, fld.Enum)
	case FieldString:
		return nil
	}
	return fmt.Errorf("%q has unknown field type %q", key, fld.Type)
}

func checkRange(key string, v float64, fld KBConfigField) error {
	if fld.Min != nil && v < *fld.Min {
		return fmt.Errorf("%q must be >= %v", key, *fld.Min)
	}
	if fld.Max != nil && v > *fld.Max {
		return fmt.Errorf("%q must be <= %v", key, *fld.Max)
	}
	return nil
}
