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
	{Key: "step_back_enabled", Type: FieldBool, Group: "Retrieval", Label: "Step-back prompting", Help: "LLM-generated broader query for complex queries."},
	{Key: "query_decompose_enabled", Type: FieldBool, Group: "Retrieval", Label: "Sub-question decomposition", Help: "Splits complex queries into 2–4 distinct sub-questions (DecomposeRAG)."},

	// --- Corrective / compression ---
	{Key: "crag_enabled", Type: FieldBool, Group: "Corrective", Label: "Corrective RAG", Help: "Grade retrieved chunks and optionally rewrite."},
	{Key: "adaptive_routing_enabled", Type: FieldBool, Group: "Corrective", Label: "Adaptive routing", Help: "Skip CRAG for lookup queries."},
	{Key: "chat_context_compression_enabled", Type: FieldBool, Group: "Corrective", Label: "ECoRAG compression", Help: "Drop chunks lacking direct evidence (1 fast-tier LLM call)."},
	{Key: "chat_context_compression_threshold", Type: FieldFloat, Group: "Corrective", Label: "Compression threshold", Help: "Drop chunks scoring below this.", Min: f(0), Max: f(1)},

	// --- Orchestrator ---
	{Key: "chat_supervisor_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Supervisor orchestrator", Help: "One classification → specialist → answer."},
	{Key: "chat_plan_execute_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Plan-and-Execute", Help: "Plan → iterate → generate."},
	{Key: "chat_agentic_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Agentic orchestrator", Help: "Hop-1 → critique → optional follow-up hops."},
	{Key: "chat_longcontext_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Long-context (System 2)", Help: "Wide-retrieval routing for global-synthesis queries. CAUTION: high per-turn cost."},
	{Key: "chat_corpus_table_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Corpus comparison tables", Help: "Auto-detect 'compare/list all X across these documents' queries and answer with a map-reduce structured table. CAUTION: one fast-tier LLM call per in-scope file."},
	{Key: "chat_corpus_table_max_files", Type: FieldInt, Group: "Orchestrator", Label: "Corpus table max files", Help: "Hard cap on files processed per corpus-table query (default 50). Excess files are dropped and the table is flagged truncated.", Min: f(1), Max: f(500)},
	{Key: "chat_corpus_table_concurrency", Type: FieldInt, Group: "Orchestrator", Label: "Corpus table concurrency", Help: "Max parallel per-file extraction calls (default 6).", Min: f(1), Max: f(50)},
	{Key: "chat_corpus_table_model", Type: FieldString, Group: "Orchestrator", Label: "Corpus table model", Help: "Per-task fast-tier model override for column planning + per-file extraction. Falls through model_tier_fast, then the KB chat model."},
	{Key: "chat_corpus_table_router_llm_enabled", Type: FieldBool, Group: "Orchestrator", Label: "Corpus table LLM router", Help: "After the keyword router fires, confirm with a fast-tier yes/no LLM call before running the expensive map-reduce (default on). Turn off to audit the keyword firing rate."},

	// --- Knowledge graph ---
	{Key: "chat_graph_routing_enabled", Type: FieldBool, Group: "Knowledge graph", Label: "Graph routing", Help: "Emit graph-traversal decision; needs kg_extraction on the KB."},
	{Key: "chat_graph_routing_inject_chunks", Type: FieldBool, Group: "Knowledge graph", Label: "Graph chunk injection", Help: "Fold subgraph chunks into RRF."},
	{Key: "chat_graph_routing_path_mode", Type: FieldEnum, Group: "Knowledge graph", Label: "Graph traversal mode", Help: "neighbors | ppr | paths.", Enum: []string{"neighbors", "ppr", "paths"}},

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
