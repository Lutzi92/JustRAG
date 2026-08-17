package pipeline

// EdgeSpec is one directed connection in the superset topology.
//
// Loop marks a correction back-edge — the UI renders these animated and
// labelled with MaxIterations, which is the whole point of drawing the graph:
// a reader can see that CRAG re-searches at most once and that the refine gate
// rewrites at most once.
type EdgeSpec struct {
	From          NodeID `json:"from"`
	To            NodeID `json:"to"`
	Label         string `json:"label"` // German edge condition, "" for unconditional
	Loop          bool   `json:"loop"`
	MaxIterations int    `json:"maxIterations"` // required when Loop is true
}

// edges is the hand-authored superset topology. Every edge that CAN exist is
// listed. Project returns Edges() verbatim — it neither prunes nor annotates
// them; deciding how to render an edge whose endpoints are inactive is the
// client's job, using the endpoints' Activation from the same payload.
var edges = []EdgeSpec{
	{From: NodeClassify, To: NodeKBRouter},
	{From: NodeKBRouter, To: NodeQueryCache},
	{From: NodeQueryCache, To: NodeStepBack, Label: "kein Treffer"},
	{From: NodeStepBack, To: NodeDecompose},
	{From: NodeDecompose, To: NodeGraphRouting},
	{From: NodeGraphRouting, To: NodeRetrieve},

	{From: NodeRetrieve, To: NodeCRAGGrade},
	{From: NodeCRAGGrade, To: NodeCRAGRewrite, Label: "unzureichend"},
	// The correction loop: rewrite feeds a second retrieval, at most once.
	{From: NodeCRAGRewrite, To: NodeRetrieve, Label: "erneut suchen", Loop: true, MaxIterations: 1},

	{From: NodeCRAGGrade, To: NodeRerank, Label: "ausreichend"},
	{From: NodeRerank, To: NodeMMR},
	{From: NodeMMR, To: NodeRecencyBoost},
	{From: NodeRecencyBoost, To: NodeFeedbackBoost},
	{From: NodeFeedbackBoost, To: NodeCompression},
	{From: NodeCompression, To: NodeSufficientCtx},

	{From: NodeSufficientCtx, To: NodeOrchestrator, Label: "ausreichend"},

	// The KB's default agent/team binding is an INPUT to orchestrator
	// selection, not a stage the material flows through — hence a second
	// edge INTO the orchestrator rather than a link in the chain. Deliberately
	// unlabelled: the condition under which the binding applies is long
	// (new web chats only, overridable per chat, ignored by API/OpenAI-compat/
	// MCP) and already lives in the node's Help and Condition. When nothing is
	// bound the node projects inactive, and the client dims this edge from its
	// endpoints — which is the honest rendering without a word of edge label.
	{From: NodeAgentBinding, To: NodeOrchestrator},

	{From: NodeOrchestrator, To: NodeAnswerTools},
	{From: NodeAnswerTools, To: NodeAnswer},

	// Multi-hop orchestrators loop back into retrieval. MaxIterations is the
	// static default of chat_agentic_max_hops (3); the projection does not
	// resolve the KB's value into the edge.
	{From: NodeOrchestrator, To: NodeRetrieve, Label: "weitere Suchrunde", Loop: true, MaxIterations: 3},

	// Post-answer verification. The factchecker and the citation validator
	// start in parallel; the claim-level verifier (or Self-RAG, which
	// replaces it) runs only DOWNSTREAM of the citation validator, because
	// its cost gate is "did the validator raise a suspect?"
	// (internal/chat/post_response.go:234, 288). Only the verifier's flagged
	// claims reach the refine loop — the factchecker's result is a badge on
	// the answer and loops nowhere.
	{From: NodeAnswer, To: NodeFactuality},
	{From: NodeAnswer, To: NodeCitationCheck},
	{From: NodeCitationCheck, To: NodeFactVerifier, Label: "Zitat nicht belegbar"},
	{From: NodeCitationCheck, To: NodeSelfRAG, Label: "Zitat nicht belegbar"},
	{From: NodeFactVerifier, To: NodeRefine, Label: "Aussagen ohne Beleg"},
	{From: NodeSelfRAG, To: NodeRefine, Label: "Aussagen ohne Beleg"},
	// The refine loop: rewrite the answer, at most once.
	{From: NodeRefine, To: NodeAnswer, Label: "Antwort korrigieren", Loop: true, MaxIterations: 1},
}

// Edges returns the superset topology.
func Edges() []EdgeSpec { return edges }
