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
	// "superseded_by:self_rag" | "requires:citation_validation" |
	// "no_binding" | "binding_unknown" | "binding_disabled" |
	// "binding_empty_team".
	//
	// The four binding_* / no_binding values are NodeAgentBinding's only
	// reasons and the only ones that are not about a site_config key — see
	// applyAgentBinding. They are kept distinct because each asks the admin for
	// different work: nothing to do, retry, flip a switch, staff a team.
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

	// PresetBase is the id recorded in the KB's workflow_preset marker — the
	// curated preset this configuration was last applied from — or "" when the
	// KB was assembled freehand. Project itself never fills it: it has no KB
	// and no raw-override map (see Deviations below). The handler sets it from
	// PresetBaseFor over the KB overlay.
	PresetBase string `json:"presetBase"`

	// PresetBaseKnown is PresetBaseFor's third state on the wire: false only
	// when PresetBase names a preset that no longer exists (renamed, removed,
	// or hand-edited into the row). It is deliberately NOT folded into an
	// empty PresetBase — the KB genuinely was set up from a preset once, and
	// reporting that as "freeform" would misstate its history.
	//
	// It also tells the canvas whether Deviations means anything: with an
	// unresolvable base there is no bundle to compare against, so Deviations
	// is empty for a reason that has nothing to do with the KB conforming.
	// Rendering „0 Abweichungen" there would be a number with nothing behind
	// it. true for a live preset AND for "no base at all" (both are states the
	// UI can render honestly).
	PresetBaseKnown bool `json:"presetBaseKnown"`

	// Deviations lists the bundle keys whose per-KB override no longer matches
	// the base preset, sorted — what the canvas renders as
	// „Basis: Hohe Präzision · 3 Abweichungen".
	//
	// Computed against the KB's OWN overrides, not the effective values: the
	// sentence is "you started here and changed three things", a statement
	// about what this KB sets. See the handler for the full argument. Empty
	// (never nil) whenever there is no resolvable base.
	Deviations []string `json:"deviations"`

	// AgentBinding is what the KB has bound as its default agent/team, plus
	// every agent/team it COULD bind — see AgentBindingInfo for why the
	// attachable set is a top-level field and not part of the node.
	//
	// Project fills Kind and Name (it is handed both) and leaves Options
	// empty; only the handler can enumerate the attachable set, and it fills
	// ID from the same resolution, exactly as it fills PresetBase/Deviations.
	AgentBinding AgentBindingInfo `json:"agentBinding"`

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
	//
	// ProjectedNode.Editable is derived from Keys[0] alone, so it says
	// nothing about the node's other keys — resolve each key in
	// ProjectedNode.Keys against Fields independently, regardless of the
	// node's own Editable flag: NodeQueryCache is Editable:true
	// (query_cache_enabled is registered) yet its sibling key
	// query_cache_similarity_threshold has no registry row and therefore no
	// Fields entry.
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
	chat.OrchComparison: "Dokumentenvergleich",
	// "Agent oder Team", not "Agenten-Team": chat.OrchTeam is the orchestrator
	// for BOTH kinds of binding — a lone agent runs through it as a one-member
	// team (tryDeepChat builds params.Members from the single agent). Naming it
	// after the team half told a librarian who had bound one AGENT that their
	// question is answered by a team they never created.
	chat.OrchTeam:        "Agent oder Team",
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

// boundBypassCondition explains, in German, why a PrepareChatContext-owned
// stage is only conditional on a lane the standard path WOULD otherwise reach —
// lookup and enumeration — once the KB has a default agent/team bound.
//
// It names the bound AGENT or TEAM rather than the orchestrator. Internally a
// lone agent runs as a one-member team and the orchestrator is called
// chat.OrchTeam, which is why the text used to read „der Orchestrator
// „Agenten-Team“ beantwortet die Frage“ on a KB whose default is a single
// agent — true of the code, and unrecognisable to the librarian who bound it.
//
// http_send.go:299 enters the deep-chat path when `isComplex || runCompare ||
// teamSel != nil`, and the web client sends the KB default with every message of
// a chat that started from it (web/src/hooks/useChatStream.ts puts
// agentSelection.teamId into the body; the chat row then carries the selection
// for the rest of the session). So a bound default takes the SAME stages out of
// the streaming chat on lookup and enumeration that the complex lane loses to
// its orchestrator — which the canvas was still drawing as plainly „aktiv“.
func boundBypassCondition(b AgentBinding) string {
	return fmt.Sprintf(
		"Läuft im Web-Chat nicht: neue Chats starten mit %s, und %s beantwortet die "+
			"Frage dann direkt — diese Stufe des Standard-Ablaufs wird dabei "+
			"übersprungen. Sie greift nur, %s.",
		bindingActor(b), bindingKindNominative(b), bindingBypassClause)
}

// applyStandardPathBypass downgrades a PrepareChatContext-owned stage on a lane
// where the streaming chat does not reach PrepareChatContext at all: the complex
// lane always, and every lane once a default agent/team is bound.
//
// It carries the ONE exception and that exception's own exception:
//
//   - The supervisor runs the sufficient-context gate itself (http_send.go:720
//     passes SufficientContextEnabled into SupervisorChatParams;
//     supervisor_chat.go:162 calls ai.JudgeContextSufficiency). So when the
//     supervisor is the fallback winner, NodeSufficientCtx really does run and
//     stays active.
//   - …unless the KB has a default agent/team bound. Then the fallback is not
//     what the default web chat does: the team gate in chat.SelectOrchestrator
//     sits above the complexity check, so a turn carrying the binding goes to
//     chat.OrchTeam, and RunTeamChat never runs the gate (its only two call
//     sites are supervisor_chat.go:162 and service.go:1048, inside PrepareChatContext).
//     The supervisor — and with it the gate — is then reachable only when the
//     binding does not apply, which is exactly ActivationConditional.
//
// On the complex lane the ordinary text keeps naming the FALLBACK orchestrator
// even on a bound KB: there the stage is skipped either way (by the team on the
// default path, by the fallback orchestrator otherwise), so complexBypassCondition
// stays true — and it is the stricter statement, because it also says the stage
// does not come back when the binding is switched off in the chat. The bound
// text is used only where the standard path would otherwise have run.
func applyStandardPathBypass(pn *ProjectedNode, lane Lane, fallback chat.Orchestrator, binding AgentBinding) {
	supervisorGate := lane == LaneComplex &&
		pn.ID == NodeSufficientCtx && fallback == chat.OrchSupervisor
	switch {
	case supervisorGate && !binding.Bound():
		// Runs for real: leave it active.
	case supervisorGate:
		pn.Activation = ActivationConditional
		pn.Reason = "orchestrator_bypass"
		pn.Condition = fmt.Sprintf(
			"Läuft bei komplexen Fragen nur, %s: dann übernimmt der Orchestrator "+
				"„%s“, der diese Prüfung selbst durchführt. Neue Chats im Web starten "+
				"dagegen mit %s, und dort wird nicht geprüft, ob das Material ausreicht.",
			bindingBypassClause, orchestratorLabel(chat.OrchSupervisor), bindingActor(binding))
	case lane == LaneComplex:
		pn.Activation = ActivationConditional
		pn.Reason = "orchestrator_bypass"
		pn.Condition = complexBypassCondition(fallback)
	default:
		// Lookup or enumeration, reached only because a default is bound —
		// Project's case guard does not call this otherwise.
		pn.Activation = ActivationConditional
		pn.Reason = "orchestrator_bypass"
		pn.Condition = boundBypassCondition(binding)
	}
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

// bindingName renders a bound agent's or team's name for the German condition
// text. An empty name means the caller resolved a binding but lost its name —
// a caller bug — and „“ would render as an empty pair of quotation marks that
// looks like a UI fault rather than a data one. Say so instead.
func bindingName(name string) string {
	if name == "" {
		return "(ohne Namen)"
	}
	return "„" + name + "“"
}

// bindingActor names the bound default in the dative, for sentences of the form
// „Neue Chats starten mit …“.
func bindingActor(b AgentBinding) string {
	if b.Kind == BindingAgent {
		return "dem Agenten " + bindingName(b.Name)
	}
	return "dem Team " + bindingName(b.Name)
}

// bindingSubject names the bound default in the nominative („der Agent „X““),
// for sentences that make it the subject rather than the instrument.
func bindingSubject(b AgentBinding) string {
	if b.Kind == BindingAgent {
		return "der Agent " + bindingName(b.Name)
	}
	return "das Team " + bindingName(b.Name)
}

// bindingKindNominative names the KIND of the bound default in the nominative,
// without the name — for „…, und der Agent beantwortet die Frage dann direkt“,
// where the name has already been given one clause earlier.
func bindingKindNominative(b AgentBinding) string {
	if b.Kind == BindingAgent {
		return "der Agent"
	}
	return "das Team"
}

// bindingObject names the KIND of the bound default in the accusative, without
// the name — for „Schalte den Agenten wieder ein“, where repeating the name
// would only lengthen a sentence that already carries it.
func bindingObject(b AgentBinding) string {
	if b.Kind == BindingAgent {
		return "den Agenten"
	}
	return "das Team"
}

// bindingBypassClause names, in German, the ONLY circumstances in which a KB
// with a bound default runs anything other than chat.OrchTeam.
//
// The team gate in chat.SelectOrchestrator sits above the complexity check and
// above every other orchestrator gate, so on a bound KB a turn that carries the
// binding goes to OrchTeam on EVERY lane. Everything else — the supervisor, the
// corpus-comparison table, DRIFT, the two-step fallback — runs only when the
// binding does not reach the turn at all: the user picked something else in the
// chat, the chat is an old one that restored its own selection, or the request
// came from a surface that sends no selection (public API, OpenAI-compat, MCP;
// internal/chat/http_send.go's resolveTeamSelection reads only body.TeamID /
// body.AgentID).
//
// Phrased as a lowercase subordinate clause because the canvas renders an
// orchestrator condition directly after the activation label („bedingt …“).
const bindingBypassClause = "wenn der Chat die Vorgabe dieser KB nicht nutzt " +
	"(im Chat umgestellt, oder Frage über API, OpenAI-Schnittstelle oder MCP)"

// demoteForBinding rewrites every candidate that is NOT the team so it stops
// claiming to simply run.
//
// Without this, a KB with a bound default and the supervisor switched on lists
// „Supervisor — aktiv“, while the default web chat the binding exists for goes
// to OrchTeam and never touches the supervisor. Conditional rather than
// inactive is the point: the user really can switch the agent off inside a
// chat, and the non-browser surfaces really do ignore the binding, so the
// supervisor is genuinely reachable. „Inaktiv“ would be the same lie mirrored.
//
// The team candidate keeps its own condition (it is already conditional, for
// the complementary reason) and is deliberately not promoted to active: it wins
// only when the request carries the selection, which the server never fills in
// by itself.
func demoteForBinding(out []OrchestratorCandidate) {
	for i := range out {
		if out[i].Orchestrator == chat.OrchTeam {
			continue
		}
		out[i].Activation = ActivationConditional
		if out[i].Condition == "" {
			out[i].Condition = bindingBypassClause
			continue
		}
		out[i].Condition += " — und nur, " + bindingBypassClause
	}
}

// applyAgentBinding resolves the KB's default agent/team binding onto its node.
//
// This is the ONLY node whose activation does not come from site_config, which
// is why it needs an explicit branch in Project's activation switch: the
// keyless shortcut there (`len(spec.Keys) == 0` → active) is right for
// NodeClassify and NodeAnswer, which are architecturally unconditional stages,
// and exactly wrong here — "no default bound" must render inactive.
//
// Two-state where the binding is KNOWN, and that is the deliberate choice: the
// binding either exists or it does not. What varies is its EFFECT — it applies
// to new web chats only, a user can override it per chat, and the public API,
// OpenAI-compat and MCP surfaces ignore it entirely — and that belongs in the
// node's Help text (nodes.go), not in an activation the operator would then
// have to decode.
//
// BindingUnknown is the third case, and it is not a softening of that rule: it
// says the caller could not read the link tables at all. „Aktiv" and „inaktiv"
// are both claims about the KB; the only honest tri-state value left is
// conditional — this node may or may not apply, and we cannot currently say
// which. Reason "binding_unknown" is what lets the canvas render it as a fault
// rather than as a configuration state.
func applyAgentBinding(pn *ProjectedNode, b AgentBinding) {
	// The disabled case is checked BEFORE Kind, and it renders INACTIVE.
	// „Aktiv" would be the lie this node exists to prevent: chat-time
	// resolution filters is_enabled, so nothing routes through a switched-off
	// agent and the KB really does answer with the standard path — which is
	// also why AgentBinding.Bound() is false here, keeping OrchTeam out of the
	// candidate list. What is NOT inactive is the row: it still exists, it
	// still names something, and it comes back the moment anyone re-enables
	// the agent. That is what the Condition and the distinct Reason carry, so
	// the canvas can show it as something to deal with rather than as the same
	// „nichts hinterlegt" a fresh KB shows.
	named := b.Kind == BindingAgent || b.Kind == BindingTeam
	// EmptyTeam is teams-only by construction (an agent has no membership), and
	// every text below assumes that. Re-checking Kind here rather than trusting
	// the field keeps a mis-mapped agent from being told about its members.
	emptyTeam := b.EmptyTeam && b.Kind == BindingTeam
	if b.Disabled && named {
		// A team that is BOTH switched off and unstaffed gets the combined
		// instruction. „sonst greift sie wieder, sobald jemand das Team
		// einschaltet" would otherwise be false: switching it back on alone
		// still leaves it with nobody to answer.
		fix := "Schalte " + bindingObject(b) + " in der Agenten-Verwaltung wieder ein, " +
			"oder nimm die Vorgabe hier heraus: sonst greift sie wieder, sobald jemand " +
			bindingObject(b) + " einschaltet."
		if emptyTeam {
			fix = "Schalte das Team in der Agenten-Verwaltung wieder ein und gib ihm " +
				"mindestens ein aktives Mitglied — beides muss stimmen —, oder nimm die " +
				"Vorgabe hier heraus."
		}
		pn.Activation = ActivationInactive
		pn.Reason = "binding_disabled"
		pn.Condition = "Als Vorgabe ist " + bindingSubject(b) + " hinterlegt, aber " +
			"abgeschaltet — und abgeschaltet übernimmt die Vorgabe nichts. Neue Chats " +
			"starten deshalb mit dem normalen Ablauf. " + fix
		return
	}
	// A team with no enabled member: the link is real, the team is switched ON,
	// and it still cannot answer anything — LoadTeamForChat returns zero members
	// and the selection is dropped. Its own reason rather than "binding_disabled"
	// because „das Team hat keine aktiven Mitglieder" tells an admin what to do
	// and „abgeschaltet" would send them to a switch that is already flipped.
	if emptyTeam {
		pn.Activation = ActivationInactive
		pn.Reason = "binding_empty_team"
		pn.Condition = "Als Vorgabe ist " + bindingSubject(b) + " hinterlegt, aber " +
			"in diesem Team ist kein einziges Mitglied aktiv — und ohne Mitglied kann " +
			"das Team nichts beantworten. Neue Chats starten deshalb mit dem normalen " +
			"Ablauf. Gib dem Team in der Agenten-Verwaltung mindestens ein aktives " +
			"Mitglied, oder nimm die Vorgabe hier heraus: sonst greift sie wieder, " +
			"sobald das Team wieder besetzt ist."
		return
	}
	switch b.Kind {
	case BindingUnknown:
		pn.Activation = ActivationConditional
		pn.Reason = "binding_unknown"
		pn.Condition = "Konnte nicht geladen werden: ob für diese Wissensbasis ein " +
			"Standard-Agent oder ein Standard-Team hinterlegt ist, ist gerade " +
			"unbekannt. Lade die Seite neu; bleibt es dabei, stimmt etwas mit der " +
			"Agenten-Verwaltung nicht. Der übrige Ablauf ist hier so dargestellt, " +
			"als gäbe es keine Vorgabe."
	case BindingTeam:
		pn.Activation = ActivationActive
		pn.Condition = "Neue Chats im Web starten mit dem Team " + bindingName(b.Name) + "."
	case BindingAgent:
		pn.Activation = ActivationActive
		pn.Condition = "Neue Chats im Web starten mit dem Agenten " + bindingName(b.Name) + "."
	default:
		pn.Activation = ActivationInactive
		pn.Reason = "no_binding"
		pn.Condition = "Für diese Wissensbasis ist kein Standard-Agent und kein " +
			"Standard-Team hinterlegt. Neue Chats starten mit dem normalen Ablauf."
	}
}

// Project resolves the static vocabulary against one KB's configuration.
//
// r is expected to be a siteconfig.KBOverlayReader so per-KB overrides are
// visible; a bare global reader also works and yields the deployment defaults.
// global is the deployment-wide (non-overlaid) reader, read separately so each
// resolved key's Origins can be attributed to "kb", "global", or "default".
//
// binding is the KB's default agent/team binding, ALREADY RESOLVED by the
// caller. It is a parameter rather than something Project looks up because
// this package must stay a leaf (no internal/agentteams import) and because
// Project must stay a pure function of its inputs — PricePresets projects
// deployment-wide preset bundles through it with no KB at all. The zero
// AgentBinding means "nothing bound", so a caller that has no binding to offer
// states that explicitly instead of getting a wrong one by default.
func Project(ctx context.Context, r, global siteconfig.BatchReader, lane Lane, binding AgentBinding) (*ProjectedGraph, error) {
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

	// Deviations starts as an empty slice, not nil, so a graph that never
	// passes through the handler still marshals `"deviations": []` rather than
	// `null` — the frontend types it as string[]. PresetBaseKnown starts true:
	// "no base" is a known state (see the field's doc comment), and only the
	// handler can discover the one case that is not.
	g := &ProjectedGraph{
		Lane: lane, Edges: Edges(), Fields: projectFields(keys),
		PresetBaseKnown: true, Deviations: []string{},
		// Kind and Name mirror the binding Project was handed, so a graph that
		// never passes through the handler still states on the wire what it
		// projected the node from. Options is empty (never nil, for the same
		// `[]`-not-`null` reason as Deviations): enumerating the attachable
		// agents needs a store this package must not have.
		AgentBinding: AgentBindingInfo{
			Kind: binding.Kind, Name: binding.Name, Disabled: binding.Disabled,
			EmptyTeam: binding.EmptyTeam,
			Options:   []BindingOption{},
		},
	}

	// The orchestrator candidates are derived FIRST because the node rules
	// below need the fallback winner (the orchestrator that takes the turn
	// when no content predicate fires). Nothing re-derives precedence: it is
	// computed once, in chat.SelectOrchestrator, via orchestratorCandidates.
	candidates, fallback := orchestratorCandidates(vals, qt, binding)
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
		// MUST precede the keyless shortcut below: NodeAgentBinding is keyless
		// but NOT unconditional. See applyAgentBinding.
		case spec.ID == NodeAgentBinding:
			applyAgentBinding(&pn, binding)
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
			// never reached by the streaming chat. A bound default does the
			// same thing to the OTHER two lanes: http_send.go:299 takes the
			// deep-chat path whenever the request carries a team/agent
			// selection, and the web client sends the KB default with every
			// message of a chat that started from it. The one stage that
			// survives on the complex lane (the supervisor's own
			// sufficient-context gate) is resolved inside
			// applyStandardPathBypass, which also knows what a bound default
			// does to that exception.
			case prepareChatContextOwned[spec.ID] && (lane == LaneComplex || binding.Bound()):
				applyStandardPathBypass(&pn, lane, fallback, binding)
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

// candidatePredicate is one signal that is NOT part of the flag baseline and
// that can change which orchestrator wins, plus the German text explaining
// when it fires.
//
// Two of the three are query-content signals (the corpus-comparison keyword
// classifier, the global-synthesis check). The third is the KB's default
// agent/team binding, which is not query content at all — it is a per-turn
// request property the web client fills in for new chats. Same mechanism
// either way: flip one field, ask SelectOrchestrator again.
type candidatePredicate struct {
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
func orchestratorCandidates(vals map[string]*string, queryType string, binding AgentBinding) ([]OrchestratorCandidate, chat.Orchestrator) {
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

	// Predicates, in the same order as the precedence ladder. The team gate
	// sits above the corpus-table gate in SelectOrchestrator, so it goes first
	// here too — the order of `out` is the order the canvas lists candidates
	// in, and listing them out of precedence order would misstate which one
	// actually wins when two could fire.
	predicates := []candidatePredicate{}

	// The KB's default agent/team binding. Deliberately a PREDICATE and not a
	// field on `base`, for two independent reasons:
	//
	//  1. It is genuinely conditional. The binding is applied CLIENT-SIDE, for
	//     new chats only (web/src/components/ChatView.tsx: the effect returns
	//     early when chat.activeChatId is set), and the server never consults
	//     the link tables — resolveTeamSelection reads only body.TeamID /
	//     body.AgentID (internal/chat/http_send.go). A reopened chat restores
	//     its own selection, the user can change it mid-session, and the
	//     public API / OpenAI-compat / MCP surfaces send no selection at all.
	//     "Active" would claim it always wins; it does not.
	//
	//  2. Putting it in `base` would move the FALLBACK to OrchTeam on every
	//     lane, and Project feeds that fallback into complexBypassCondition —
	//     so every PrepareChatContext-owned node would suddenly explain itself
	//     with "der Orchestrator „Agent oder Team“ beantwortet die Frage direkt"
	//     even for the turns that never reach the team. The fallback must
	//     stay "what runs when the request carries nothing special".
	//
	// A bound AGENT sets TeamSelected exactly as a bound team does, and that
	// is not an approximation of the real code — it IS the real code:
	// http_send.go builds OrchestratorInputs with `TeamSelected: teamSel !=
	// nil`, and resolveTeamSelection returns a non-nil *teamSelection for
	// body.AgentID just as it does for body.TeamID. tryDeepChat's OrchTeam
	// branch then runs the lone agent as a one-member team
	// (params.Members = []agentteams.AgentRecord{*teamSel.agent}). So an
	// agent-only default really does route the turn through OrchTeam; there is
	// no separate agent branch to project.
	if binding.Bound() {
		predicates = append(predicates, candidatePredicate{
			apply: func(in *chat.OrchestratorInputs) { in.TeamSelected = true },
			condition: "wenn ein neuer Chat im Web die Vorgabe dieser KB übernimmt " +
				"(im Chat umstellbar; API, OpenAI-Schnittstelle und MCP ignorieren sie)",
		})
	}

	predicates = append(predicates,
		candidatePredicate{
			apply:     func(in *chat.OrchestratorInputs) { in.IsCorpusQuery = true },
			condition: "wenn die Frage einen Vergleich über mehrere Dokumente verlangt",
		},
		candidatePredicate{
			apply:     func(in *chat.OrchestratorInputs) { in.IsGlobalSynthesis = true },
			condition: "wenn die Frage eine Gesamtschau über die ganze KB verlangt",
		},
	)

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

	// On a KB with a bound default, nothing below the team gate simply runs —
	// including the fallback that was just appended as active. See
	// demoteForBinding.
	if binding.Bound() {
		demoteForBinding(out)
	}
	return out, fallback
}
