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
	Activation Activation `json:"activation"`
	// Reason is the machine-readable "why not plainly active" tag:
	// "flag_off" | "lane_skipped" | "orchestrator_bypass" |
	// "superseded_by:self_rag" | "requires:citation_validation".
	Reason string `json:"reason,omitempty"`
	// Condition is the German explanation shown to the operator. Set
	// whenever Activation is conditional, and also on the inactive states
	// whose Reason alone would not tell an operator what to change.
	Condition string `json:"condition,omitempty"`
	// Values maps only the keys that are EXPLICITLY set somewhere in the
	// overlay chain (KB or global) to their resolved value. A key that is
	// unset everywhere — the code default applies — is absent from this map
	// and appears in Origins as "default". The UI must therefore fall back
	// to the registry's default when rendering an editor, not assume a
	// missing key means an empty value.
	Values   map[string]string `json:"values"`
	Editable bool              `json:"editable"` // on/off key is in the per-KB registry

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

	// Fields carries the registry metadata for every config key any node
	// references AND that has a per-KB registry row — type, range, enum,
	// German label and help. The canvas needs it to render an input at all:
	// ProjectedNode.Keys is only strings.
	//
	// Keys a node references but that have NO registry row (e.g.
	// min_similarity_threshold, chat_kb_router_enabled — see
	// TestProjectKBRouterStaysNotEditable) are deliberately OMITTED rather
	// than synthesised. siteconfig.KBConfigField.Type is not optional: it is
	// what tells the canvas whether to draw a checkbox, a number input, or a
	// dropdown. There is no source of truth for that outside the registry,
	// so guessing it would ship metadata that actively lies about a field's
	// domain — worse than shipping no metadata, and the same class of lie
	// Phase 2 exists to eliminate (see the ProjectedNode.Editable doc
	// comment and TestProjectKBRouterStaysNotEditable). A humanised
	// key-name-as-label would not be a "real" label either — the frontend
	// can derive that fallback itself from the bare key already on the wire
	// in ProjectedNode.Keys, and from the owning node's own Label/Help/Group
	// (NodeSpec, also already on the wire).
	Fields map[string]siteconfig.KBConfigField `json:"fields"`
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

// projectFields resolves keys (the deduplicated node vocabulary, from
// allKeys) against the per-KB registry. Keys with no registry row are
// omitted — see the doc comment on ProjectedGraph.Fields for why.
func projectFields(keys []string) map[string]siteconfig.KBConfigField {
	out := map[string]siteconfig.KBConfigField{}
	for _, k := range keys {
		if fld, ok := siteconfig.Field(k); ok {
			out[k] = fld
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
//	chat_factuality_verifier_enabled  false (siteconfig.go:373)
//	chat_self_rag_enabled             false (siteconfig.go:484)
//	chat_factuality_gate_enabled      false (siteconfig.go:408)
//	citation_validation_enabled       TRUE  (siteconfig.go:235)
//
// The list above is no longer maintained by hand alone:
// TestDefaultOnMatchesReadBoolDefaults (defaults_test.go) re-derives each of
// these defaults from the AST of the real call site and fails when this map
// and the code disagree in either direction.
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

// prepareChatContextOwned lists the nodes whose stage is implemented ONLY
// inside chat.PrepareChatContext (internal/chat/service.go:833):
//
//	step_back        service.go:877  (opts.StepBack)
//	decompose        service.go:968  (maybeDecomposeQuery)
//	crag_grade       service.go:878  (opts.Grade)
//	crag_rewrite     service.go:976  (runCRAG → cragRewrite branch)
//	compression      service.go:1004 (applyEvidentialityCompression)
//	sufficient_ctx   service.go:1048 (ai.JudgeContextSufficiency)
//
// This matters because SendMessage routes every STREAMING complex_reasoning
// turn into tryDeepChat (http_send.go:299), and every branch of that
// orchestrator switch bypasses PrepareChatContext — including OrchStandard,
// which dispatches RunDeepChat (deep_chat.go:74), a two-step search that sets
// neither Grade nor StepBack and calls neither the compression nor the
// sufficiency helper. Verified: PrepareChatContext has exactly one chat-path
// call site, http_send.go:355, inside SendMessage's NON-complex branch.
//
// So on the complex lane these stages do not run for the streaming chat a KB
// admin is looking at, no matter how their flag is set. They are not dead,
// though — see complexBypassCondition.
var prepareChatContextOwned = map[NodeID]bool{
	NodeStepBack:      true,
	NodeDecompose:     true,
	NodeCRAGGrade:     true,
	NodeCRAGRewrite:   true,
	NodeCompression:   true,
	NodeSufficientCtx: true,
}

// complexReasoningOnly lists nodes whose stage additionally refuses to run for
// anything but a complex_reasoning query, INSIDE PrepareChatContext:
//
//   - step_back: vector.stepBackOutcome (search_helpers.go:199) returns
//     "skipped_route" for every query type but complex_reasoning.
//   - decompose: maybeDecomposeQuery (service.go:769) returns early with
//     "skipped_route" for every query type but complex_reasoning.
//
// Combined with prepareChatContextOwned this means both stages are inert on
// the streaming chat path on EVERY lane — bypassed on complex, route-skipped
// on lookup and enumeration. Drawing either as plainly "aktiv" anywhere was
// the same class of lie as the complex-lane bypass.
var complexReasoningOnly = map[NodeID]bool{
	NodeStepBack:  true,
	NodeDecompose: true,
}

// orchestratorLabels are German display names for the condition texts. Kept
// here rather than on OrchestratorCandidate so the wire contract stays the
// bare chat.Orchestrator string.
var orchestratorLabels = map[chat.Orchestrator]string{
	chat.OrchComparison:  "Dokumentenvergleich",
	chat.OrchTeam:        "Agenten-Team",
	chat.OrchCorpusTable: "Korpus-Vergleichstabelle",
	chat.OrchDrift:       "DRIFT",
	chat.OrchSupervisor:  "Supervisor",
	chat.OrchPlanExecute: "Plan-and-Execute",
	chat.OrchAgentic:     "Agentische Suche",
	chat.OrchStandard:    "Zwei-Schritt-Recherche",
}

func orchestratorLabel(o chat.Orchestrator) string {
	if l, ok := orchestratorLabels[o]; ok {
		return l
	}
	return string(o)
}

// complexBypassCondition explains, in German, why a PrepareChatContext-owned
// stage is only conditional on the complex lane.
//
// ActivationConditional rather than ActivationInactive is deliberate: the
// stage is dead for the streaming chat, but it is NOT universally dead. It
// still runs for the same KB when the turn arrives without ?stream=true
// (http_send.go:166 + :299), when an orchestrator errors and SendMessage
// falls through to the standard path (http_send.go:302), and on the
// non-streaming surfaces that call PrepareChatContext directly —
// internal/mcpserver and the eval harness with a site-config reader.
// "Inaktiv" would be an equally wrong diagram in the other direction.
func complexBypassCondition(fallback chat.Orchestrator) string {
	return fmt.Sprintf(
		"Läuft bei komplexen Fragen im Chat nicht: dort beantwortet der Orchestrator "+
			"„%s“ die Frage direkt und überspringt diese Stufe des Standard-Ablaufs. "+
			"Sie greift hier nur, wenn die Frage ohne Streaming gestellt wird "+
			"(API-, MCP- und Auswertungspfade) oder der Orchestrator ausfällt.",
		orchestratorLabel(fallback))
}

// applyVerifierGate resolves the launch + cost gate that the claim-level
// verifier and Self-RAG BOTH sit behind (internal/chat/post_response.go):
//
//   - :234 the goroutine starts only if citation_validation_enabled OR
//     chat_factuality_verifier_enabled. chat_self_rag_enabled alone starts
//     nothing.
//   - :288 inside it, the call is skipped unless the citation validator
//     raised at least one unverified marker, OR
//     chat_factuality_verifier_always_run is set.
//
// Note that siteconfig.ValidateConflicts (conflicts.go:23) rejects
// chat_self_rag_enabled together with chat_factuality_verifier_enabled, so on
// a config saved through the settings endpoint the only launch path for
// Self-RAG is the citation validator. Legacy rows may still carry both, which
// is why the verifier flag is honoured here rather than assumed off.
func applyVerifierGate(pn *ProjectedNode, vals map[string]*string) {
	validator := boolVal(vals, "citation_validation_enabled")
	verifier := boolVal(vals, "chat_factuality_verifier_enabled")
	alwaysRun := boolVal(vals, "chat_factuality_verifier_always_run")

	// Nothing launches the goroutine at all.
	if !validator && !verifier {
		pn.Activation = ActivationInactive
		pn.Reason = "requires:citation_validation"
		pn.Condition = "Kann nicht laufen: diese Prüfung wird nur zusammen mit der " +
			"Zitatprüfung angestoßen. Zitatprüfung einschalten."
		return
	}
	if alwaysRun {
		// The cost gate is switched off — the check runs on every turn.
		return
	}
	if !validator {
		// The goroutine starts (the verifier flag is on), but the suspect
		// signal it gates on can never appear without the validator.
		pn.Activation = ActivationInactive
		pn.Reason = "requires:citation_validation"
		pn.Condition = "Kann nicht laufen: ohne aktive Zitatprüfung gibt es keinen " +
			"Verdachtsfall, der diese Prüfung auslöst. Zitatprüfung einschalten " +
			"oder „immer prüfen“ aktivieren."
		return
	}
	pn.Activation = ActivationConditional
	pn.Condition = "Läuft nur, wenn die Zitatprüfung mindestens eine Quellenangabe " +
		"nicht belegen konnte. „Immer prüfen“ schaltet diese Bedingung ab."
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

	g := &ProjectedGraph{Lane: lane, Edges: Edges(), Fields: projectFields(keys)}

	// The orchestrator candidates are derived FIRST because the node rules
	// below need the fallback winner (the orchestrator that takes the turn
	// when no content predicate fires). Nothing re-derives precedence: it is
	// computed once, in chat.SelectOrchestrator, via orchestratorCandidates.
	candidates, fallback := orchestratorCandidates(vals, qt)
	g.Orchestrators = candidates

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

		// ---------------------------------------------------------------
		// Lane rules. Everything below only ever DOWNGRADES a node that the
		// flags said was active — a node whose own flag is off stays
		// "flag_off", because a stage that is switched off runs on no lane.
		//
		// The two lane rules are disjoint by construction and must stay so:
		// the adaptive-routing skip fires only on lookup/enumeration (the
		// lanes that actually reach PrepareChatContext), the orchestrator
		// bypass only on complex (the lane that never does). Neither can
		// contradict the other on the same lane.
		// ---------------------------------------------------------------
		if pn.Activation == ActivationActive {
			switch {
			// Adaptive routing disables CRAG for lookup and enumeration
			// lanes even when crag_enabled is true. This is the single most
			// surprising behaviour in the pipeline and one of the two
			// reasons the lane view exists.
			case (spec.ID == NodeCRAGGrade || spec.ID == NodeCRAGRewrite) &&
				(lane == LaneLookup || lane == LaneEnumeration) &&
				boolVal(vals, "adaptive_routing_enabled"):
				pn.Activation = ActivationInactive
				pn.Reason = "lane_skipped"
				pn.Condition = "Adaptive Routing überspringt CRAG bei Lookup- und Aufzählungsfragen."

			// Stages that refuse anything but a complex_reasoning query,
			// inside PrepareChatContext itself.
			case complexReasoningOnly[spec.ID] && lane != LaneComplex:
				pn.Activation = ActivationInactive
				pn.Reason = "lane_skipped"
				pn.Condition = "Greift nur bei komplexen Fragen; bei Lookup- und " +
					"Aufzählungsfragen überspringt die Suche diese Stufe."

			// The big one: on the complex lane the orchestrator answers
			// directly and PrepareChatContext — which owns this stage — is
			// never reached by the streaming chat.
			//
			// One exception, the trailing clause: the supervisor carries the
			// sufficient-context gate itself (http_send.go:720 passes
			// SufficientContextEnabled into SupervisorChatParams;
			// supervisor_chat.go:161 runs it). When the supervisor is the
			// fallback winner that gate really does run on this lane.
			case prepareChatContextOwned[spec.ID] && lane == LaneComplex &&
				!(spec.ID == NodeSufficientCtx && fallback == chat.OrchSupervisor):
				pn.Activation = ActivationConditional
				pn.Reason = "orchestrator_bypass"
				pn.Condition = complexBypassCondition(fallback)
			}
		}

		// Post-answer verification gates. Self-RAG REPLACES the claim-level
		// verifier (post_response.go:301 picks ai.VerifySelfRAG over
		// ai.VerifyFactuality inside one branch); it does NOT replace the
		// factchecker gated by factcheck_in_chat, which runs in its own
		// goroutine at post_response.go:134-140 regardless. Marking
		// NodeFactuality inactive here was wrong on both counts — the
		// factchecker kept firing and EstLLMCalls under-counted by one.
		switch spec.ID {
		case NodeFactVerifier:
			if pn.Activation == ActivationActive {
				if boolVal(vals, "chat_self_rag_enabled") {
					pn.Activation = ActivationInactive
					pn.Reason = "superseded_by:self_rag"
					pn.Condition = "Wird von der Selbstprüfung (Self-RAG) ersetzt."
				} else {
					applyVerifierGate(&pn, vals)
				}
			}
		case NodeSelfRAG:
			if pn.Activation == ActivationActive {
				applyVerifierGate(&pn, vals)
			}
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

	return g, nil
}

// contentPredicate is a query-content signal that can change which
// orchestrator wins, plus the German text explaining when it fires.
type contentPredicate struct {
	apply     func(*chat.OrchestratorInputs)
	condition string
}

// orchestratorCandidates derives, in precedence order, the orchestrators that
// can win on this lane, and returns the fallback winner separately — the
// orchestrator that takes the turn when no content predicate fires. The node
// rules in Project consume that winner; they never re-derive it.
//
// It does NOT re-encode precedence — it calls chat.SelectOrchestrator once with
// every content predicate false (the "otherwise" winner) and once per content
// predicate. Precedence therefore has exactly one implementation, and this
// function cannot drift from what actually runs.
func orchestratorCandidates(vals map[string]*string, queryType string) ([]OrchestratorCandidate, chat.Orchestrator) {
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
	// The projection cannot make an LLM call, so the corpus-table
	// confirmation is answered optimistically: "yes, the LLM router would
	// confirm". That keeps corpus_table visible as a CANDIDATE rather than
	// silently dropping it. Named for what it returns.
	alwaysConfirm := func() bool { return true }

	fallback := chat.SelectOrchestrator(base, alwaysConfirm)

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
		got := chat.SelectOrchestrator(in, alwaysConfirm)
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
	return out, fallback
}
