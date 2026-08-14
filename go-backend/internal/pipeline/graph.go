package pipeline

// EdgeSpec is one directed connection in the superset topology.
//
// Loop marks a correction back-edge — the UI renders these animated and
// labelled with MaxIterations, which is the whole point of drawing the graph:
// a reader can see that CRAG re-searches at most once and that the refine gate
// rewrites at most once.
type EdgeSpec struct {
	From          NodeID
	To            NodeID
	Label         string // German edge condition, "" for unconditional
	Loop          bool
	MaxIterations int // required when Loop is true
}

// edges is the hand-authored superset topology. Every edge that CAN exist is
// listed; the projection dims the ones whose endpoints are inactive.
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
	{From: NodeOrchestrator, To: NodeAnswerTools},
	{From: NodeAnswerTools, To: NodeAnswer},

	// Multi-hop orchestrators loop back into retrieval. The bound is
	// chat_agentic_max_hops (default 3) — the projection overwrites
	// MaxIterations with the KB's resolved value.
	{From: NodeOrchestrator, To: NodeRetrieve, Label: "weitere Suchrunde", Loop: true, MaxIterations: 3},

	{From: NodeAnswer, To: NodeFactuality},
	{From: NodeAnswer, To: NodeSelfRAG},
	{From: NodeFactuality, To: NodeRefine, Label: "Aussagen ohne Beleg"},
	// The refine loop: rewrite the answer, at most once.
	{From: NodeRefine, To: NodeAnswer, Label: "Antwort korrigieren", Loop: true, MaxIterations: 1},
	{From: NodeFactuality, To: NodeCitationCheck, Label: "sauber"},
	{From: NodeSelfRAG, To: NodeCitationCheck},
}

// Edges returns the superset topology.
func Edges() []EdgeSpec { return edges }
