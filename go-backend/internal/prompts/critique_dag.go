package prompts

import (
	"fmt"
	"strings"
)

// CritiqueDAGSystemPrompt is the AP-D3 inter-level critic. Reads
// the user's original question, the plan that was generated, the
// per-node findings collected so far, and the still-pending nodes
// — then picks one of four actions:
//
//	continue    the pending plan is fine, just run the next level
//	add_nodes   we found something that needs follow-up sub-queries
//	prune_nodes one or more pending nodes are now redundant
//	answer_now  we have enough; stop traversing and synthesise
//
// Strict-JSON output. The executor's caps (width ≤ 6, depth ≤ 3)
// are HARD constraints — when add_nodes would push past them, the
// executor rejects the addition with a metric label rather than
// failing the chat. This keeps the critic free to be aspirational
// while the executor enforces sanity.
//
// The prompt explicitly forbids "always add_nodes" — early-iteration
// LLMs treat the option as encouragement and request additions on
// every level even when the existing pending nodes would resolve
// the question. The "default to continue" instruction nudges the
// model toward the no-op outcome.
func CritiqueDAGSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du bist ein Plan-Kritiker. Nach jedem topologischen Level der Plan-Execute-DAG bewertest du, ob der Plan unverändert weitergehen soll.

Vier Aktionen:
- "continue": ausstehende Knoten reichen aus, nichts ändern (DEFAULT)
- "add_nodes": eine konkrete Lücke wurde sichtbar, neue Knoten ergänzen
- "prune_nodes": ausstehende Knoten sind redundant geworden, streichen
- "answer_now": die bisherigen Findings reichen, Beantwortung sofort starten

Antworte AUSSCHLIESSLICH mit validem JSON:

{"action": "continue|add_nodes|prune_nodes|answer_now", "new_nodes": [{"id": "n7", "query": "...", "depends_on": ["n1"], "reason": "..."}], "prune_node_ids": ["n4"], "reason": "<ein Satz, max 200 Zeichen>"}

Regeln:
- "continue" ist die DEFAULT-Wahl. Wähle die anderen Aktionen NUR wenn ein konkreter Anlass aus den Findings existiert.
- "new_nodes": pro Aufruf maximal 2 Knoten. IDs müssen neu sein (nicht im bestehenden Plan).
- depends_on bei new_nodes: IDs aus dem ORIGINAL-Plan oder aus früheren new_nodes; KEINE noch ausstehenden Knoten als Dependency.
- "prune_node_ids": nur ausstehende Knoten-IDs (bereits fertige nicht angeben).
- new_nodes / prune_node_ids leer bei "continue" / "answer_now".
- Konservativer als großzügiger sein — der Original-Plan ist meistens richtig.`
	}
	return `You are a plan critic. After each topological level of the plan-execute DAG, decide whether the plan should continue unchanged.

Four actions:
- "continue": pending nodes are sufficient, no change (DEFAULT)
- "add_nodes": a concrete gap surfaced, add follow-up nodes
- "prune_nodes": pending nodes are now redundant, drop them
- "answer_now": findings so far are enough, synthesise immediately

Reply ONLY with valid JSON:

{"action": "continue|add_nodes|prune_nodes|answer_now", "new_nodes": [{"id": "n7", "query": "...", "depends_on": ["n1"], "reason": "..."}], "prune_node_ids": ["n4"], "reason": "<one sentence, max 200 chars>"}

Rules:
- "continue" is the DEFAULT. Pick the other actions ONLY when a concrete reason from the findings exists.
- "new_nodes": at most 2 per call. IDs must be NEW (not present in the existing plan).
- depends_on on new_nodes: IDs from the ORIGINAL plan or earlier new_nodes; NEVER pending nodes as dependencies.
- "prune_node_ids": only pending node IDs (don't list already-completed ones).
- new_nodes / prune_node_ids empty on "continue" / "answer_now".
- Be conservative rather than generous — the original plan is usually right.`
}

// CritiqueDAGCompletedNode is one finished node's contribution to
// the critic's input. The TopExcerpt is the same first-300-chars
// snippet the executor already builds for downstream interpolation
// — reusing it here avoids re-loading chunks for the critic.
type CritiqueDAGCompletedNode struct {
	NodeID     string
	Query      string
	TopExcerpt string
	Err        bool // true when the node errored — the critic might prune dependents
}

// CritiqueDAGPendingNode is one node still waiting to run.
type CritiqueDAGPendingNode struct {
	NodeID string
	Query  string
}

// CritiqueDAGUserPrompt formats the per-call payload. Original
// question first, then the plan-level summary so the LLM sees
// the goal before the noisy details. Findings are bullet-list
// per completed node; pending nodes are bullet-list per id.
func CritiqueDAGUserPrompt(question string, completed []CritiqueDAGCompletedNode, pending []CritiqueDAGPendingNode) string {
	var b strings.Builder
	b.WriteString("Question:\n")
	b.WriteString(question)
	b.WriteString("\n\nFindings so far:\n")
	if len(completed) == 0 {
		b.WriteString("(none yet)\n")
	} else {
		for _, n := range completed {
			tag := "ok"
			if n.Err {
				tag = "ERROR"
			}
			fmt.Fprintf(&b, "- [%s, %s] query: %s\n  excerpt: %s\n",
				n.NodeID, tag, n.Query, firstChars(n.TopExcerpt, 300))
		}
	}
	b.WriteString("\nPending nodes:\n")
	if len(pending) == 0 {
		b.WriteString("(none — last level reached)\n")
	} else {
		for _, n := range pending {
			fmt.Fprintf(&b, "- [%s] %s\n", n.NodeID, n.Query)
		}
	}
	return b.String()
}

// firstChars trims content to a max length on a rune boundary.
// Local copy of the helper — the prompts package can't import ai
// where firstNChars lives.
func firstChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
