// Package chatpolicy holds the two operator-authored routing documents the
// chat layer consults per turn: the orchestrator policy table
// (`chat_orchestrator_policy`) and the per-route answer-tool allowlist
// (`chat_answer_tools_by_route`).
//
// It is a LEAF package on purpose (W6-R13): the save-time validator lives in
// internal/siteconfig, which internal/chat imports, so the parser has to sit
// below both. It therefore depends on the standard library only — never on
// chat, siteconfig, eval or mcp.
package chatpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Mode values for a policy rule.
//
//   - ModeForce  selects the rule's orchestrator regardless of its feature
//     flag. It is the operator overriding the ladder, deliberately.
//   - ModePrefer selects it only when that orchestrator's flag is on;
//     otherwise the turn falls through to the ordinary ladder.
const (
	ModeForce  = "force"
	ModePrefer = "prefer"
)

// OrchestratorStandard is the always-constructible fallback route. A "prefer"
// rule naming it always applies, because it has no feature flag to be off.
const OrchestratorStandard = "standard"

// Orchestrators is the closed set of routes a rule may name (W6-R16).
// "plan_execute_dag" is plan-execute with DAG forced for the turn.
var Orchestrators = []string{
	"drift", "longcontext", "supervisor", "plan_execute", "plan_execute_dag", "agentic", OrchestratorStandard,
}

// QueryTypes is the closed set of values `when.query_type` may contain — the
// production query classifier's own labels.
var QueryTypes = []string{"lookup", "enumeration", "complex_reasoning"}

// MaxRules bounds a stored policy document. The table is walked per turn on
// the answer path; 32 rules is far past any legible operator policy and keeps
// the walk trivially cheap.
const MaxRules = 32

// When is a rule's condition. Every field is optional and a field that is not
// set is not tested — so a `When` with no fields set matches every turn. The
// booleans are pointers precisely so "absent" and "must be false" stay
// distinguishable.
type When struct {
	QueryType        []string `json:"query_type,omitempty"`
	GlobalSynthesis  *bool    `json:"global_synthesis,omitempty"`
	Enumeration      *bool    `json:"enumeration,omitempty"`
	RecencyListing   *bool    `json:"recency_listing,omitempty"`
	HasFileSelection *bool    `json:"has_file_selection,omitempty"`
	HistoryTurnsGTE  *int     `json:"history_turns_gte,omitempty"`
	KBIDs            []string `json:"kb_ids,omitempty"`
}

// Rule is one entry of the policy table.
type Rule struct {
	When         When   `json:"when"`
	Orchestrator string `json:"orchestrator"`
	Mode         string `json:"mode"`
}

// OrchestratorPolicy is the ordered rule list. Order is the operator's
// priority statement: the FIRST matching rule wins.
type OrchestratorPolicy []Rule

// Signals is everything a rule may test, resolved by the caller. The caller
// owns the resolution (it knows the classifier's verdict, the KB, the history
// depth); this package only compares.
type Signals struct {
	QueryType        string
	GlobalSynthesis  bool
	Enumeration      bool
	RecencyListing   bool
	HasFileSelection bool
	HistoryTurns     int
	KBID             string
}

// Decision is the outcome of Decide.
//
// Matched=false means no rule matched — RuleIndex is -1 and the caller runs
// the ordinary flag ladder. Applied=false with Matched=true means a "prefer"
// rule matched but its orchestrator's flag is off, which is also a
// fall-through — the distinction exists so the trajectory event can say which
// of the two happened.
type Decision struct {
	RuleIndex    int
	Orchestrator string
	Mode         string
	Matched      bool
	Applied      bool
}

// ParseOrchestratorPolicy decodes and validates a stored policy document.
// An empty or whitespace-only value is the documented default ("no policy")
// and yields (nil, nil) — clearing the admin field must never be an error.
//
// Decoding rejects unknown fields anywhere in the document: a typo'd key would
// otherwise be silently dropped, and a rule that quietly loses its condition
// is a routing change the operator did not ask for.
func ParseOrchestratorPolicy(raw string) (OrchestratorPolicy, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if trimmed[0] != '[' {
		return nil, fmt.Errorf("chat_orchestrator_policy must be a JSON array of rules")
	}

	dec := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	dec.DisallowUnknownFields()
	var p OrchestratorPolicy
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("invalid policy JSON: %w", err)
	}
	if err := ensureEOF(dec); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// ValidateOrchestratorPolicyJSON is the save-time entry point used by
// internal/siteconfig. It is exactly the parser's verdict, so a value that
// saves is a value the reader can parse.
func ValidateOrchestratorPolicyJSON(raw string) error {
	_, err := ParseOrchestratorPolicy(raw)
	return err
}

func (p OrchestratorPolicy) validate() error {
	if len(p) > MaxRules {
		return fmt.Errorf("policy has %d rules, at most %d rules are allowed", len(p), MaxRules)
	}
	for i, r := range p {
		if !slices.Contains(Orchestrators, r.Orchestrator) {
			return fmt.Errorf("rule %d: unknown orchestrator %q (known: %s)", i, r.Orchestrator, strings.Join(Orchestrators, ", "))
		}
		if r.Mode != ModeForce && r.Mode != ModePrefer {
			return fmt.Errorf("rule %d: unknown mode %q (known: %s, %s)", i, r.Mode, ModeForce, ModePrefer)
		}
		for _, qt := range r.When.QueryType {
			if !slices.Contains(QueryTypes, qt) {
				return fmt.Errorf("rule %d: unknown query_type %q (known: %s)", i, qt, strings.Join(QueryTypes, ", "))
			}
		}
		for j, id := range r.When.KBIDs {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("rule %d: kb_ids[%d] must not be empty", i, j)
			}
		}
	}
	return nil
}

// Match returns the index and the first rule whose condition holds for sig.
// The returned pointer aliases the policy slice — callers must not mutate it.
// No match returns (-1, nil, false).
func (p OrchestratorPolicy) Match(sig Signals) (int, *Rule, bool) {
	for i := range p {
		if p[i].When.matches(sig) {
			return i, &p[i], true
		}
	}
	return -1, nil, false
}

func (w When) matches(sig Signals) bool {
	if len(w.QueryType) > 0 && !slices.Contains(w.QueryType, sig.QueryType) {
		return false
	}
	if w.GlobalSynthesis != nil && *w.GlobalSynthesis != sig.GlobalSynthesis {
		return false
	}
	if w.Enumeration != nil && *w.Enumeration != sig.Enumeration {
		return false
	}
	if w.RecencyListing != nil && *w.RecencyListing != sig.RecencyListing {
		return false
	}
	if w.HasFileSelection != nil && *w.HasFileSelection != sig.HasFileSelection {
		return false
	}
	if w.HistoryTurnsGTE != nil && sig.HistoryTurns < *w.HistoryTurnsGTE {
		return false
	}
	if len(w.KBIDs) > 0 && !slices.Contains(w.KBIDs, sig.KBID) {
		return false
	}
	return true
}

// Decide runs Match and then applies the force/prefer semantics. `enabled`
// reports which orchestrators' feature flags are on, keyed by the
// Orchestrators values; a missing key reads as off. "standard" is always
// enabled — it is the fallback route and has no flag.
func Decide(p OrchestratorPolicy, sig Signals, enabled map[string]bool) Decision {
	idx, rule, ok := p.Match(sig)
	if !ok {
		return Decision{RuleIndex: -1}
	}
	d := Decision{
		RuleIndex:    idx,
		Orchestrator: rule.Orchestrator,
		Mode:         rule.Mode,
		Matched:      true,
	}
	switch rule.Mode {
	case ModeForce:
		d.Applied = true
	case ModePrefer:
		d.Applied = rule.Orchestrator == OrchestratorStandard || enabled[rule.Orchestrator]
	}
	return d
}

// ensureEOF rejects trailing content after the document. Without it,
// `[] garbage` would parse as a valid empty policy and silently discard the
// rest of what the operator typed.
func ensureEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("invalid JSON: trailing data after the document")
	}
	return nil
}
