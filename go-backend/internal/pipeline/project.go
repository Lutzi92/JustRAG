package pipeline

import (
	"context"
	"fmt"
	"strconv"

	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/siteconfig"
	"github.com/justrag/go-backend/internal/vector"
)

// Lane is the query-type view being projected. The pipeline behaves
// differently per lane (adaptive routing skips CRAG for lookups; four
// orchestrators require complex_reasoning), so "what runs?" has no single
// answer without one.
type Lane string

const (
	LaneLookup      Lane = "lookup"
	LaneEnumeration Lane = "enumeration"
	LaneComplex     Lane = "complex_reasoning"
)

// queryType maps a Lane onto the vector package's classifier constants.
func (l Lane) queryType() (string, bool) {
	switch l {
	case LaneLookup:
		return vector.QueryTypeLookup, true
	case LaneEnumeration:
		return vector.QueryTypeEnumeration, true
	case LaneComplex:
		return vector.QueryTypeComplexReasoning, true
	default:
		return "", false
	}
}

// Activation is a node's three-state status on a lane.
//
// A plain bool is not enough: some gates key on query CONTENT, not query type
// (corpus-table's keyword classifier, DRIFT's global-synthesis check). Those
// are ActivationConditional — configured and eligible, but only firing for
// certain questions.
type Activation string

const (
	ActivationActive      Activation = "active"
	ActivationConditional Activation = "conditional"
	ActivationInactive    Activation = "inactive"
)

// ProjectedNode is one node resolved against a KB's configuration.
type ProjectedNode struct {
	NodeSpec
	Activation Activation        `json:"activation"`
	Reason     string            `json:"reason,omitempty"`    // "flag_off" | "lane_skipped"
	Condition  string            `json:"condition,omitempty"` // German, when Activation is conditional
	Values     map[string]string `json:"values"`              // resolved key -> value
	Editable   bool              `json:"editable"`            // on/off key is in the per-KB registry

	// Origins maps each resolved key to where its value came from:
	// "kb" (a kb_site_configs override), "global" (the deployment default row),
	// or "default" (unset everywhere; the code default applies).
	//
	// Known limitation: when a KB override happens to equal the global value,
	// this reports "global" — it cannot tell a redundant override from an
	// inherited value without reading kb_site_configs directly rather than
	// through the overlay. The UI consequence is benign (a redundant override
	// displays as inherited), so it is not worth a second store dependency here.
	Origins map[string]string `json:"origins"`
}

// OrchestratorCandidate is one orchestrator that can win on this lane.
type OrchestratorCandidate struct {
	Orchestrator chat.Orchestrator `json:"orchestrator"`
	Activation   Activation        `json:"activation"`
	Condition    string            `json:"condition,omitempty"`
}

// ProjectedGraph is the full answer to "what runs for this KB on this lane?".
type ProjectedGraph struct {
	Lane          Lane                    `json:"lane"`
	Nodes         []ProjectedNode         `json:"nodes"`
	Edges         []EdgeSpec              `json:"edges"`
	Orchestrators []OrchestratorCandidate `json:"orchestrators"`
	EstLLMCalls   int                     `json:"estLlmCalls"`
	EstLatencyMs  int                     `json:"estLatencyMs"`
}

// allKeys collects every site_config key the vocabulary references, so the
// projection can fetch them in a single batch round-trip.
func allKeys() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, n := range Nodes() {
		for _, k := range n.Keys {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// boolVal resolves a key to a bool, treating an unset key as its documented
// default via defaults below.
func boolVal(vals map[string]*string, key string) bool {
	if v, ok := vals[key]; ok && v != nil {
		b, err := strconv.ParseBool(*v)
		if err == nil {
			return b
		}
	}
	return defaultOn[key]
}

// defaultOn lists the keys whose documented default is ON (kill switches).
// Everything absent here defaults to off, matching readBool's usual `false`.
//
// Hand-maintained duplicate of the defaults at each key's readBool/readInt
// call site (internal/chat/siteconfig.go unless noted) — verified 2026-08-14
// against every Keys[0] the projection actually consults:
//
//	chat_kb_router_enabled            false (siteconfig.go:966)
//	query_cache_enabled               false (vector/query_cache_config.go: defaultQueryCacheConfig)
//	step_back_enabled                 false (chat/service.go:366, parseBool)
//	query_decompose_enabled           false (siteconfig.go:739)
//	chat_graph_routing_enabled        false (siteconfig.go:866)
//	crag_enabled                      false (chat/service.go:355, parseBool)
//	recency_boost_enabled             false (vector/config.go: zero-value default)
//	chat_feedback_boost_enabled       false (siteconfig.go:1283)
//	chat_context_compression_enabled  false (siteconfig.go:677)
//	chat_sufficient_context_enabled   false (siteconfig.go:691)
//	chat_answer_tools_enabled         false (siteconfig.go:1075)
//	factcheck_in_chat                 TRUE  (siteconfig.go:224)
//	chat_self_rag_enabled             false (siteconfig.go:484)
//	chat_factuality_gate_enabled      false (siteconfig.go:408)
//	citation_validation_enabled       TRUE  (siteconfig.go:235)
//
// Only factcheck_in_chat and citation_validation_enabled are actually
// default-on among the node activation keys; the remaining entries below
// (chat_answer_history_enabled, chat_date_awareness_enabled,
// chat_recency_listing_enabled, chat_corpus_table_router_llm_enabled) are not
// consulted by any Keys[0] or orchestrator predicate today (the latter is
// hardcoded false in orchestratorCandidates — the projection cannot make an
// LLM call), but are kept accurate against their real siteconfig.go defaults
// for whichever future logic reads them via boolVal.
var defaultOn = map[string]bool{
	"chat_answer_history_enabled":          true,
	"chat_date_awareness_enabled":          true,
	"chat_recency_listing_enabled":         true,
	"citation_validation_enabled":          true,
	"chat_corpus_table_router_llm_enabled": true,
	// factcheck_in_chat is the actual master toggle for post-response
	// factchecking (readBool default true, siteconfig.go:224) and is
	// NodeFactuality's Keys[0]. Missing this entry would report
	// factchecking as disabled on every deployment that has not explicitly
	// set the key, undoing the nodes.go Keys[0] fix from Task 4.
	"factcheck_in_chat": true,
}

// Project resolves the static vocabulary against one KB's configuration.
//
// r is expected to be a siteconfig.KBOverlayReader so per-KB overrides are
// visible; a bare global reader also works and yields the deployment defaults.
// global is the deployment-wide (non-overlaid) reader, read separately so each
// resolved key's Origins can be attributed to "kb", "global", or "default".
func Project(ctx context.Context, r, global siteconfig.BatchReader, lane Lane) (*ProjectedGraph, error) {
	qt, ok := lane.queryType()
	if !ok {
		return nil, fmt.Errorf("pipeline: unknown lane %q", lane)
	}

	keys := allKeys()
	vals, err := r.GetSiteConfigValues(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("pipeline: read site config: %w", err)
	}
	globals, err := global.GetSiteConfigValues(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("pipeline: read global site config: %w", err)
	}

	g := &ProjectedGraph{Lane: lane, Edges: Edges()}

	for _, spec := range Nodes() {
		pn := ProjectedNode{NodeSpec: spec, Values: map[string]string{}}

		pn.Origins = map[string]string{}
		for _, k := range spec.Keys {
			effective, hasEffective := vals[k]
			globalVal, hasGlobal := globals[k]

			switch {
			case !hasEffective || effective == nil:
				pn.Origins[k] = "default"
			case hasGlobal && globalVal != nil && *globalVal == *effective:
				pn.Values[k] = *effective
				pn.Origins[k] = "global"
			default:
				pn.Values[k] = *effective
				pn.Origins[k] = "kb"
			}
		}

		switch {
		case spec.AlwaysOn || len(spec.Keys) == 0:
			// Unconditional stage (retrieval, rerank, answer generation, MMR,
			// orchestrator dispatch).
			pn.Activation = ActivationActive
		case boolVal(vals, spec.Keys[0]):
			pn.Activation = ActivationActive
		default:
			pn.Activation = ActivationInactive
			pn.Reason = "flag_off"
		}

		// Adaptive routing disables CRAG for lookup and enumeration lanes even
		// when crag_enabled is true. This is the single most surprising
		// behaviour in the pipeline and the main reason the lane view exists.
		if spec.ID == NodeCRAGGrade || spec.ID == NodeCRAGRewrite {
			if pn.Activation == ActivationActive &&
				boolVal(vals, "adaptive_routing_enabled") &&
				(lane == LaneLookup || lane == LaneEnumeration) {
				pn.Activation = ActivationInactive
				pn.Reason = "lane_skipped"
				pn.Condition = "Adaptive Routing überspringt CRAG bei Lookup- und Aufzählungsfragen."
			}
		}

		// Self-RAG replaces the factuality verifier when both are on.
		if spec.ID == NodeFactuality && boolVal(vals, "chat_self_rag_enabled") {
			pn.Activation = ActivationInactive
			pn.Reason = "superseded_by:self_rag"
			pn.Condition = "Wird von der Selbstprüfung (Self-RAG) ersetzt."
		}

		if len(spec.Keys) > 0 {
			pn.Editable = siteconfig.IsPerKB(spec.Keys[0])
		}

		if pn.Activation == ActivationActive {
			g.EstLLMCalls += spec.LLMCalls
			g.EstLatencyMs += spec.LatencyMs
		}

		g.Nodes = append(g.Nodes, pn)
	}

	g.Orchestrators = orchestratorCandidates(vals, qt)
	return g, nil
}

// contentPredicate is a query-content signal that can change which
// orchestrator wins, plus the German text explaining when it fires.
type contentPredicate struct {
	apply     func(*chat.OrchestratorInputs)
	condition string
}

// orchestratorCandidates derives, in precedence order, the orchestrators that
// can win on this lane.
//
// It does NOT re-encode precedence — it calls chat.SelectOrchestrator once with
// every content predicate false (the "otherwise" winner) and once per content
// predicate. Precedence therefore has exactly one implementation, and this
// function cannot drift from what actually runs.
func orchestratorCandidates(vals map[string]*string, queryType string) []OrchestratorCandidate {
	base := chat.OrchestratorInputs{
		QueryType:             queryType,
		CorpusTableEnabled:    boolVal(vals, "chat_corpus_table_enabled"),
		CorpusChunksAvailable: true,
		CorpusRouterLLMOn:     false, // projection cannot make an LLM call
		DriftEnabled:          boolVal(vals, "chat_drift_enabled"),
		SupervisorEnabled:     boolVal(vals, "chat_supervisor_enabled"),
		PlanExecuteEnabled:    boolVal(vals, "chat_plan_execute_enabled"),
		AgenticEnabled:        boolVal(vals, "chat_agentic_enabled"),
	}
	neverConfirm := func() bool { return true }

	fallback := chat.SelectOrchestrator(base, neverConfirm)

	out := []OrchestratorCandidate{}
	seen := map[chat.Orchestrator]bool{}

	// Content predicates, in the same order as the precedence ladder.
	predicates := []contentPredicate{
		{
			apply:     func(in *chat.OrchestratorInputs) { in.IsCorpusQuery = true },
			condition: "wenn die Frage einen Vergleich über mehrere Dokumente verlangt",
		},
		{
			apply:     func(in *chat.OrchestratorInputs) { in.IsGlobalSynthesis = true },
			condition: "wenn die Frage eine Gesamtschau über die ganze KB verlangt",
		},
	}

	for _, p := range predicates {
		in := base
		p.apply(&in)
		got := chat.SelectOrchestrator(in, neverConfirm)
		if got == fallback || seen[got] {
			continue
		}
		seen[got] = true
		out = append(out, OrchestratorCandidate{
			Orchestrator: got,
			Activation:   ActivationConditional,
			Condition:    p.condition,
		})
	}

	out = append(out, OrchestratorCandidate{
		Orchestrator: fallback,
		Activation:   ActivationActive,
		Condition:    "",
	})
	return out
}
