package prompts

import (
	"fmt"
	"strings"
)

// KBRouterCandidate is one KB the AP-A4 router LLM gets to choose from.
// Description is the operator-edited free-text blurb stored in
// knowledge_bases.description; it's the primary signal — if no
// description is set, the router falls back to the KB name only.
type KBRouterCandidate struct {
	ID          string
	Name        string
	Description string
}

// KBRouterSystemPrompt instructs the LLM to score query→KB fit in
// strict JSON. Bilingual scoring: the prompt itself is in English to
// keep the few-shot reasoning consistent, but the LLM is told to
// match query intent regardless of query language.
//
// Why a stable system prompt: KB descriptions change rarely (operator
// edits in admin UI). When the router sees enough traffic to make
// caching worthwhile, this layout lets us move the candidate list
// into the system prompt and earn provider-side prompt-cache hits;
// for AP-A4 we keep the candidate list in the user prompt so the
// list stays fresh on every call (no cache invalidation logic).
func KBRouterSystemPrompt() string {
	return `You are a routing classifier. Given a user query and a list of available knowledge bases, return a ranked list of which KBs likely contain the answer.

Rules:
1. Match query intent (entities, topics, document types) against each KB's name + description.
2. Output ONLY KBs whose description plausibly contains the answer. Do NOT include every KB.
3. Confidence is 0.0–1.0: 1.0 = obvious single match, 0.5 = plausible but uncertain, < 0.3 = weak match (omit instead of returning low confidence).
4. If NO KB looks plausible, return an empty list — the caller falls back to "search all".
5. The query may be in any language; the descriptions may be in any language; match by meaning, not by token overlap.

Respond ONLY with valid JSON matching:
{"matches": [{"kb_id": "<uuid>", "confidence": 0.85, "reason": "brief why"}]}

Order matches by confidence descending. Empty list = {"matches": []}.`
}

// KBRouterUserPrompt formats the per-call payload: the query first,
// then the candidate KB list. Descriptions are escaped only for
// double-quote characters in the name (the description goes in a
// "Description: …" prose block where free-text quotes are fine).
func KBRouterUserPrompt(query string, candidates []KBRouterCandidate) string {
	var b strings.Builder
	b.WriteString("Query:\n")
	b.WriteString(query)
	b.WriteString("\n\nAvailable knowledge bases:\n")
	for _, c := range candidates {
		desc := strings.TrimSpace(c.Description)
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&b, "- kb_id: %s\n  Name: %s\n  Description: %s\n",
			c.ID, c.Name, desc)
	}
	return b.String()
}
