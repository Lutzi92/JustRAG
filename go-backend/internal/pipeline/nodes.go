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
		Help:  "Bestimmt den Anfragetyp (lookup / enumeration / complex_reasoning). Steuert Top-N, Reranker-α und welche Orchestratoren überhaupt greifen.",
		Keys:  nil,
		LLMCalls: 0, LatencyMs: 20,
	},
	{
		ID: NodeKBRouter, Label: "KB-Router", Group: "Analyse",
		Help: "Wählt bei ?route=auto die passende Wissensbasis.",
		Keys: []string{"chat_kb_router_enabled"},
		LLMCalls: 1, LatencyMs: 300,
	},
	{
		ID: NodeQueryCache, Label: "Query-Cache", Group: "Analyse",
		Help: "Beantwortet semantisch ähnliche Wiederholungsfragen aus dem Cache.",
		Keys: []string{"query_cache_enabled"},
		LLMCalls: 0, LatencyMs: 30,
	},
	{
		ID: NodeStepBack, Label: "Step-Back-Prompting", Group: "Anfrage-Aufbereitung",
		Help: "Erzeugt eine allgemeinere Zusatzfrage für komplexe Anfragen.",
		Keys: []string{"step_back_enabled"},
		LLMCalls: 1, LatencyMs: 400,
	},
	{
		ID: NodeDecompose, Label: "Teilfragen-Zerlegung", Group: "Anfrage-Aufbereitung",
		Help: "Zerlegt komplexe Anfragen in 2–4 eigenständige Teilfragen (DecomposeRAG).",
		Keys: []string{"query_decompose_enabled"},
		LLMCalls: 1, LatencyMs: 500,
	},
	{
		ID: NodeGraphRouting, Label: "Graph-Routing", Group: "Anfrage-Aufbereitung",
		Help: "Nutzt den Wissensgraphen der KB, um verwandte Textstellen mitzuziehen. Setzt Graph-Extraktion beim Ingest voraus.",
		Keys: []string{"chat_graph_routing_enabled", "chat_graph_routing_inject_chunks", "chat_graph_routing_path_mode"},
		LLMCalls: 0, LatencyMs: 250,
	},
	{
		ID: NodeRetrieve, Label: "Retrieval", Group: "Retrieval",
		Help: "Hybride Suche: BM25 + Vektorsuche über die Chunks der KB.",
		Keys: []string{},
		LLMCalls: 0, LatencyMs: 400,
	},
	{
		ID: NodeCRAGGrade, Label: "CRAG-Bewertung", Group: "Korrektur",
		Help: "Bewertet die gefundenen Textstellen und entscheidet, ob nachgesucht werden muss.",
		Keys: []string{"crag_enabled", "adaptive_routing_enabled"},
		LLMCalls: 1, LatencyMs: 600,
	},
	{
		ID: NodeCRAGRewrite, Label: "Anfrage umformulieren", Group: "Korrektur",
		Help: "Formuliert die Suchanfrage neu und sucht erneut, wenn die Bewertung unzureichend ausfiel.",
		Keys: []string{"crag_enabled"},
		LLMCalls: 1, LatencyMs: 900,
	},
	{
		ID: NodeRerank, Label: "Reranking", Group: "Retrieval",
		Help: "Cross-Encoder sortiert die Kandidaten neu; α mischt Reranker- und RRF-Score.",
		Keys: []string{},
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
		Help: "Bevorzugt neuere Dokumente (exponentieller Abfall). Für RSS-/Confluence-KBs.",
		Keys: []string{"recency_boost_enabled"},
		LLMCalls: 0, LatencyMs: 5,
	},
	{
		ID: NodeFeedbackBoost, Label: "Feedback-Boost", Group: "Retrieval",
		Help: "Gewichtet Textstellen hoch, die in positiv bewerteten Antworten zitiert wurden.",
		Keys: []string{"chat_feedback_boost_enabled"},
		LLMCalls: 0, LatencyMs: 10,
	},
	{
		ID: NodeCompression, Label: "Kontext-Kompression", Group: "Korrektur",
		Help: "Verwirft Textstellen ohne direkte Belegkraft (ECoRAG).",
		Keys: []string{"chat_context_compression_enabled", "chat_context_compression_threshold"},
		LLMCalls: 1, LatencyMs: 700,
	},
	{
		ID: NodeSufficientCtx, Label: "Kontext-Prüfung", Group: "Korrektur",
		Help: "Prüft vor dem Antworten, ob das gesammelte Material überhaupt ausreicht — sonst wird die Antwort verweigert.",
		Keys: []string{"chat_sufficient_context_enabled"},
		LLMCalls: 1, LatencyMs: 600,
	},
	{
		ID: NodeOrchestrator, Label: "Orchestrator", Group: "Antwort",
		Help: "Entscheidet, wie die Antwort erarbeitet wird — direkt, geplant, mehrstufig oder über ein Team.",
		Keys: []string{"chat_supervisor_enabled", "chat_plan_execute_enabled", "chat_plan_execute_dag", "chat_plan_execute_tool_aware", "chat_agentic_enabled", "chat_agentic_max_hops", "chat_drift_enabled", "chat_corpus_table_enabled"},
		LLMCalls: 2, LatencyMs: 1500,
	},
	{
		ID: NodeAnswerTools, Label: "Werkzeuge zur Antwortzeit", Group: "Antwort",
		Help: "Erlaubt dem Antwortmodell, während des Schreibens Werkzeuge aufzurufen (Suche, Rechner, …).",
		Keys: []string{"chat_answer_tools_enabled"},
		LLMCalls: 2, LatencyMs: 1200,
	},
	{
		ID: NodeAnswer, Label: "Antwortgenerierung", Group: "Antwort",
		Help: "Das Antwortmodell erzeugt die Antwort aus den ausgewählten Textstellen.",
		Keys: []string{},
		LLMCalls: 1, LatencyMs: 2000,
	},
	{
		ID: NodeFactuality, Label: "Faktencheck", Group: "Prüfung",
		Help: "Prüft die fertige Antwort auf nicht belegte oder widersprochene Aussagen.",
		Keys: []string{"chat_factuality_verifier_enabled"},
		LLMCalls: 1, LatencyMs: 800,
	},
	{
		ID: NodeSelfRAG, Label: "Selbstprüfung (Self-RAG)", Group: "Prüfung",
		Help: "Vereinheitlichte Prüfung (Relevanz + Beleglage + Nützlichkeit). ERSETZT den Faktencheck.",
		Keys: []string{"chat_self_rag_enabled"},
		LLMCalls: 1, LatencyMs: 900,
	},
	{
		ID: NodeRefine, Label: "Korrektur-Schleife", Group: "Prüfung",
		Help: "Schreibt die Antwort neu, wenn der Faktencheck nicht belegte Aussagen gefunden hat.",
		Keys: []string{"chat_factuality_gate_enabled"},
		LLMCalls: 2, LatencyMs: 1500,
	},
	{
		ID: NodeCitationCheck, Label: "Zitatprüfung", Group: "Prüfung",
		Help: "Prüft jede Quellenangabe gegen den zitierten Text und markiert unbelegte Zitate.",
		Keys: []string{"citation_validation_enabled"},
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
