// Package pipeline describes the JustRAG answering pipeline as data — a static
// node/edge vocabulary plus a projection that resolves it against one KB's
// configuration.
//
// The graph is a PROJECTION of resolved config, not an execution spec: the
// chat package keeps executing exactly as before. The topology here is authored
// by hand and fixed; users toggle nodes, which writes kb_site_configs.
//
// This package is a leaf. It may import internal/chat, internal/siteconfig and
// internal/vector; nothing in internal/chat may import it.
package pipeline

// NodeID is the canonical, stable identifier for one pipeline stage.
//
// These strings are a wire contract: they appear in the workflow API payload
// and (from v2) in TrajectoryEvent.NodeID for turn replay. Never rename one
// without a migration note.
type NodeID string

const (
	NodeClassify      NodeID = "classify"
	NodeKBRouter      NodeID = "kb_router"
	NodeQueryCache    NodeID = "query_cache"
	NodeStepBack      NodeID = "step_back"
	NodeDecompose     NodeID = "decompose"
	NodeGraphRouting  NodeID = "graph_routing"
	NodeRetrieve      NodeID = "retrieve"
	NodeCRAGGrade     NodeID = "crag_grade"
	NodeCRAGRewrite   NodeID = "crag_rewrite"
	NodeRerank        NodeID = "rerank"
	NodeMMR           NodeID = "mmr"
	NodeRecencyBoost  NodeID = "recency_boost"
	NodeFeedbackBoost NodeID = "feedback_boost"
	NodeCompression   NodeID = "context_compression"
	NodeSufficientCtx NodeID = "sufficient_context"
	NodeOrchestrator  NodeID = "orchestrator"
	NodeAnswerTools   NodeID = "answer_tools"
	NodeAnswer        NodeID = "answer"
	NodeFactuality    NodeID = "factuality"
	NodeSelfRAG       NodeID = "self_rag"
	NodeRefine        NodeID = "refine"
	NodeCitationCheck NodeID = "citation_validate"
)

// NodeSpec is the static description of one pipeline stage.
//
// Keys lists the site_config keys this node owns. By convention Keys[0] is the
// node's on/off key; the projection reads it to decide activation. An EMPTY
// Keys slice means the stage is unconditional (retrieval, answer generation)
// and always renders active.
//
// AlwaysOn marks a stage that runs unconditionally but still owns tuning keys
// — MMR is the case that forced this field: it has no boolean gate, only
// mmr_lambda (λ=1 makes it a no-op). Without AlwaysOn the projection would try
// to parse "0.5" as a bool, fail, and silently mark the node inactive.
//
// LLMCalls and LatencyMs are hand-tuned worst-case ESTIMATES used for the
// cost badge. They are not measurements and the UI must label them as such.
type NodeSpec struct {
	ID        NodeID
	Label     string
	Group     string
	Help      string
	Keys      []string
	AlwaysOn  bool
	LLMCalls  int
	LatencyMs int
}

// nodes is the ordered vocabulary. Order is the canonical top-to-bottom
// reading order and is preserved by Nodes().
var nodes = []NodeSpec{
	{
		ID: NodeClassify, Label: "Klassifizierung", Group: "Analyse",
		Help: "Bestimmt den Anfragetyp (lookup / enumeration / complex_reasoning). Steuert Top-N, Reranker-α und welche Orchestratoren überhaupt greifen.",
		// []string{}, NOT nil: NodeSpec is serialised as a wire contract in
		// Task 7, and a nil slice marshals to `null` while an empty slice
		// marshals to `[]`. Every keyless node must agree.
		Keys:     []string{},
		LLMCalls: 0, LatencyMs: 20,
	},
	{
		ID: NodeKBRouter, Label: "KB-Router", Group: "Analyse",
		Help:     "Wählt bei ?route=auto die passende Wissensbasis.",
		Keys:     []string{"chat_kb_router_enabled", "chat_kb_router_min_confidence"},
		LLMCalls: 1, LatencyMs: 300,
	},
	{
		ID: NodeQueryCache, Label: "Query-Cache", Group: "Analyse",
		Help: "Beantwortet semantisch ähnliche Wiederholungsfragen aus dem Cache.",
		Keys: []string{
			"query_cache_enabled",
			"query_cache_similarity_threshold",
			"query_cache_similarity_threshold_lookup",
			"query_cache_similarity_threshold_enumeration",
			"query_cache_similarity_threshold_complex_reasoning",
			"query_cache_ttl_hours",
		},
		LLMCalls: 0, LatencyMs: 30,
	},
	{
		ID: NodeStepBack, Label: "Step-Back-Prompting", Group: "Anfrage-Aufbereitung",
		Help:     "Erzeugt eine allgemeinere Zusatzfrage für komplexe Anfragen.",
		Keys:     []string{"step_back_enabled"},
		LLMCalls: 1, LatencyMs: 400,
	},
	{
		ID: NodeDecompose, Label: "Teilfragen-Zerlegung", Group: "Anfrage-Aufbereitung",
		Help:     "Zerlegt komplexe Anfragen in 2–4 eigenständige Teilfragen (DecomposeRAG).",
		Keys:     []string{"query_decompose_enabled"},
		LLMCalls: 1, LatencyMs: 500,
	},
	{
		ID: NodeGraphRouting, Label: "Graph-Routing", Group: "Anfrage-Aufbereitung",
		Help: "Nutzt den Wissensgraphen der KB, um verwandte Textstellen mitzuziehen. Setzt Graph-Extraktion beim Ingest voraus.",
		Keys: []string{
			"chat_graph_routing_enabled",
			"chat_graph_routing_inject_chunks",
			"chat_graph_routing_path_mode",
			"chat_graph_routing_max_chunks",
			"chat_graph_routing_paths_max_len",
			"chat_graph_routing_paths_max_paths",
			"chat_graph_routing_ppr_damping",
			"chat_graph_routing_ppr_max_iter",
			"chat_graph_routing_ppr_top_entities",
			"chat_graph_routing_ppr_dual_node_enabled",
			"chat_graph_routing_ppr_triple_filter_enabled",
			"chat_graph_routing_ppr_triple_filter_max_triples",
			"chat_graph_routing_bridge_rerank_enabled",
			"bridge_boost_weight",
		},
		LLMCalls: 0, LatencyMs: 250,
	},
	{
		// NOTE: no single mmr_lambda-style always-on gate — Retrieval is
		// architecturally unconditional (hybrid BM25+vector search always
		// runs) but owns a large tuning surface (fusion weights, ANN
		// parameters, per-query-type top-k, HyPE). AlwaysOn, same reasoning
		// as NodeMMR below.
		ID: NodeRetrieve, Label: "Retrieval", Group: "Retrieval",
		Help: "Hybride Suche: BM25 + Vektorsuche über die Chunks der KB.",
		Keys: []string{
			"min_similarity_threshold",
			"auto_spell_correct",
			"mrl_two_pass_enabled",
			"default_top_k",
			"score_drop_threshold",
			"top_n_lookup",
			"top_n_enumeration",
			"top_n_complex_reasoning",
			"context_window_size",
			"rrf_weight_vector",
			"rrf_weight_bm25",
			"query_instruction",
			"hnsw_ef_search",
			"rag_fusion_enabled",
			"bm25_simple_arm_enabled",
			"bm25_tiered_boost_enabled",
			"hybrid_dynamic_alpha_enabled",
			"hybrid_dynamic_alpha_sensitivity",
			"hype_search_enabled",
		},
		AlwaysOn: true,
		LLMCalls: 0, LatencyMs: 400,
	},
	{
		ID: NodeCRAGGrade, Label: "CRAG-Bewertung", Group: "Korrektur",
		Help:     "Bewertet die gefundenen Textstellen und entscheidet, ob nachgesucht werden muss.",
		Keys:     []string{"crag_enabled", "adaptive_routing_enabled", "crag_min_relevant_chunks"},
		LLMCalls: 1, LatencyMs: 600,
	},
	{
		ID: NodeCRAGRewrite, Label: "Anfrage umformulieren", Group: "Korrektur",
		Help:     "Formuliert die Suchanfrage neu und sucht erneut, wenn die Bewertung unzureichend ausfiel.",
		Keys:     []string{"crag_enabled"},
		LLMCalls: 1, LatencyMs: 900,
	},
	{
		// NOTE: same AlwaysOn shape as Retrieve — the cross-encoder reranker
		// always runs; these keys tune it (blend alpha per query type,
		// score-drop cutoff, instruction/template).
		ID: NodeRerank, Label: "Reranking", Group: "Retrieval",
		Help: "Cross-Encoder sortiert die Kandidaten neu; α mischt Reranker- und RRF-Score.",
		Keys: []string{
			"rerank_blend_alpha",
			"rerank_blend_alpha_lookup",
			"rerank_blend_alpha_enumeration",
			"rerank_blend_alpha_complex_reasoning",
			"rerank_blend_alpha_entity",
			"rerank_score_drop_enabled",
			"rerank_score_drop_threshold",
			"rerank_use_chat_template",
			"rerank_instruction",
		},
		AlwaysOn: true,
		LLMCalls: 0, LatencyMs: 350,
	},
	{
		// NOTE: there is NO mmr_enabled key — MMR is unconditional and tuned
		// only by mmr_lambda (λ=1 = pure relevance = effectively off). Hence
		// AlwaysOn. Verified 2026-08-14: `grep -r '"mmr_enabled"' internal/`
		// returns nothing.
		ID: NodeMMR, Label: "MMR-Diversität", Group: "Retrieval",
		Help: "Reduziert Redundanz in der Trefferliste (λ = Relevanz vs. Vielfalt).",
		Keys: []string{"mmr_lambda"}, AlwaysOn: true,
		LLMCalls: 0, LatencyMs: 20,
	},
	{
		ID: NodeRecencyBoost, Label: "Aktualitäts-Boost", Group: "Retrieval",
		Help:     "Bevorzugt neuere Dokumente (exponentieller Abfall). Für RSS-/Confluence-KBs.",
		Keys:     []string{"recency_boost_enabled", "recency_boost_weight", "recency_half_life_days"},
		LLMCalls: 0, LatencyMs: 5,
	},
	{
		ID: NodeFeedbackBoost, Label: "Feedback-Boost", Group: "Retrieval",
		Help:     "Gewichtet Textstellen hoch, die in positiv bewerteten Antworten zitiert wurden.",
		Keys:     []string{"chat_feedback_boost_enabled", "feedback_boost_weight"},
		LLMCalls: 0, LatencyMs: 10,
	},
	{
		ID: NodeCompression, Label: "Kontext-Kompression", Group: "Korrektur",
		Help:     "Verwirft Textstellen ohne direkte Belegkraft (ECoRAG).",
		Keys:     []string{"chat_context_compression_enabled", "chat_context_compression_threshold", "chat_context_compression_min_chunks"},
		LLMCalls: 1, LatencyMs: 700,
	},
	{
		ID: NodeSufficientCtx, Label: "Kontext-Prüfung", Group: "Korrektur",
		Help:     "Prüft vor dem Antworten, ob das gesammelte Material überhaupt ausreicht — sonst wird die Antwort verweigert.",
		Keys:     []string{"chat_sufficient_context_enabled"},
		LLMCalls: 1, LatencyMs: 600,
	},
	{
		// Keys mixes orchestrator SELECTION gates (chat_supervisor_enabled,
		// chat_plan_execute_enabled, chat_agentic_enabled, chat_drift_enabled,
		// chat_corpus_table_enabled — one per row of the priority table in
		// docs/agent-orchestration.md) with each selected orchestrator's own
		// tuning knobs (DAG shape, plateau-stop, DRIFT follow-up budget,
		// corpus-table scan limits, …). A KB admin configuring "how the
		// answer is decided" wants both in one place; splitting them into
		// per-orchestrator nodes would multiply node count without adding a
		// distinct pipeline STAGE (the orchestrator choice already IS the
		// stage).
		ID: NodeOrchestrator, Label: "Orchestrator", Group: "Antwort",
		Help: "Entscheidet, wie die Antwort erarbeitet wird — direkt, geplant, mehrstufig oder über ein Team.",
		Keys: []string{
			"chat_supervisor_enabled",
			"chat_supervisor_multi_specialist",
			"chat_plan_execute_enabled",
			"chat_plan_execute_dag",
			"chat_plan_execute_dag_iterative",
			"chat_plan_execute_tool_aware",
			"chat_plan_execute_max_sub_queries",
			"chat_plan_execute_max_iterations",
			"chat_plan_execute_token_budget",
			"chat_plan_execute_max_dag_depth",
			"chat_plan_execute_max_dag_nodes",
			"chat_agentic_enabled",
			"chat_agentic_max_hops",
			"chat_agentic_plateau_stop",
			"chat_agentic_plateau_chunks_threshold",
			"chat_agentic_plateau_score_delta",
			"chat_drift_enabled",
			"chat_drift_max_followups",
			"chat_drift_primer_top_k",
			"chat_drift_search_top_k",
			"chat_corpus_table_enabled",
			"chat_corpus_table_concurrency",
			"chat_corpus_table_max_files",
			"chat_corpus_table_router_llm_enabled",
		},
		// AlwaysOn: an orchestrator always runs — Keys[0]
		// (chat_supervisor_enabled) defaults OFF, but when every orchestrator
		// flag is off the standard PrepareChatContext path is the fallback
		// orchestrator, not "no orchestrator". Without this the projection
		// would render this node inactive on every default deployment.
		AlwaysOn: true,
		LLMCalls: 2, LatencyMs: 1500,
	},
	{
		ID: NodeAnswerTools, Label: "Werkzeuge zur Antwortzeit", Group: "Antwort",
		Help:     "Erlaubt dem Antwortmodell, während des Schreibens Werkzeuge aufzurufen (Suche, Rechner, …).",
		Keys:     []string{"chat_answer_tools_enabled", "chat_answer_tools_max_rounds", "chat_code_exec_enabled"},
		LLMCalls: 2, LatencyMs: 1200,
	},
	{
		ID: NodeAnswer, Label: "Antwortgenerierung", Group: "Antwort",
		Help:     "Das Antwortmodell erzeugt die Antwort aus den ausgewählten Textstellen.",
		Keys:     []string{},
		LLMCalls: 1, LatencyMs: 2000,
	},
	{
		// factcheck_in_chat (default TRUE) is the actual master toggle for
		// whether ANY factchecking runs post-response — it was previously
		// undrawn while a narrower, default-OFF escalation flag
		// (chat_factuality_verifier_enabled, the "agent-as-judge" verifier
		// that only fires when the citation validator raises a suspect
		// marker) sat alone in Keys[0]. Found during Task 4's coverage run:
		// a fresh KB runs factchecking by default, but the pre-existing
		// Keys[0] would have projected this node as inactive by default.
		// factcheck_in_chat is now Keys[0] so activation matches reality;
		// the verifier escalation and its always-run override are
		// additional tuning keys on the same node.
		ID: NodeFactuality, Label: "Faktencheck", Group: "Prüfung",
		Help:     "Prüft die fertige Antwort auf nicht belegte oder widersprochene Aussagen.",
		Keys:     []string{"factcheck_in_chat", "chat_factuality_verifier_enabled", "chat_factuality_verifier_always_run"},
		LLMCalls: 1, LatencyMs: 800,
	},
	{
		ID: NodeSelfRAG, Label: "Selbstprüfung (Self-RAG)", Group: "Prüfung",
		Help:     "Vereinheitlichte Prüfung (Relevanz + Beleglage + Nützlichkeit). ERSETZT den Faktencheck.",
		Keys:     []string{"chat_self_rag_enabled"},
		LLMCalls: 1, LatencyMs: 900,
	},
	{
		ID: NodeRefine, Label: "Korrektur-Schleife", Group: "Prüfung",
		Help:     "Schreibt die Antwort neu, wenn der Faktencheck nicht belegte Aussagen gefunden hat.",
		Keys:     []string{"chat_factuality_gate_enabled", "chat_factuality_gate_max_refines"},
		LLMCalls: 2, LatencyMs: 1500,
	},
	{
		ID: NodeCitationCheck, Label: "Zitatprüfung", Group: "Prüfung",
		Help:     "Prüft jede Quellenangabe gegen den zitierten Text und markiert unbelegte Zitate.",
		Keys:     []string{"citation_validation_enabled", "citation_validation_semantic_threshold"},
		LLMCalls: 0, LatencyMs: 120,
	},
}

var nodeIndex = func() map[NodeID]NodeSpec {
	m := make(map[NodeID]NodeSpec, len(nodes))
	for _, n := range nodes {
		m[n.ID] = n
	}
	return m
}()

// Nodes returns the ordered node vocabulary.
func Nodes() []NodeSpec { return nodes }

// NodeByID looks up a single node.
func NodeByID(id NodeID) (NodeSpec, bool) {
	n, ok := nodeIndex[id]
	return n, ok
}
