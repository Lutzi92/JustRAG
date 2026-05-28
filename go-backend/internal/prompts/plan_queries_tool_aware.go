package prompts

import (
	"fmt"
	"strings"
)

// PlanToolEntry is one tool the AP-B3 tool-aware planner can choose
// from. The chat orchestrator builds the slice from the live MCP
// registry (`Registry.List(kbID)`) so admins who add a tool see it
// in the next chat turn without restart.
//
// InputSchemaJSON is the tool's JSON Schema as a string — the
// LLM is far better at producing valid JSON when it sees a
// concrete schema than when it sees a description alone.
type PlanToolEntry struct {
	Name            string
	Description     string
	InputSchemaJSON string
}

// PlanQueriesDAGToolAwareSystemPrompt is the AP-B3 variant. Same
// DAG contract as the legacy planner, plus a `tool` + `args` field
// per node. The available-tools catalog is rendered into the prompt
// inline.
//
// Why inline vs. system-prompt-cached: the catalog is small (~10
// tools max in foreseeable Phase B/C deployments) and changes when
// admins add MCP servers. Inlining keeps the implementation simple
// and the catalog accurate; if Phase D adds dozens of tools we
// move the catalog into a stable preamble for cache hits.
func PlanQueriesDAGToolAwareSystemPrompt(lang string, tools []PlanToolEntry) string {
	catalog := renderToolCatalog(tools)
	if lang == "de" {
		return `Du bist ein Such-Planer mit Werkzeug-Auswahl. Zerlege die Nutzerfrage in einen gerichteten azyklischen Graphen (DAG) aus 1 bis 6 Knoten. Wähle pro Knoten das passende Werkzeug aus dem Katalog.

Verfügbare Werkzeuge:
` + catalog + `

Antworte AUSSCHLIESSLICH mit einem JSON-Objekt:

{"nodes": [{"id": "n1", "query": "…", "tool": "<name>", "args": {…}, "depends_on": [], "reason": "…"}], "rationale": "kurze Begründung"}

Regeln:
- Nodes: maximal 6, jedes "query" 5–25 Wörter.
- Tiefe (längste Kette) maximal 3.
- IDs: kurze Kennungen ("n1", "n2", …), eindeutig im Plan.
- "tool": NAME aus dem Katalog (z.B. "kb_search", "keyword_search", "sql_query"). Wenn unsicher: "kb_search".
- "args": JSON-Objekt mit den vom Werkzeug-Schema verlangten Feldern. "kb_id" wird automatisch ergänzt — NICHT angeben.
- depends_on: IDs anderer Knoten, deren Antwort vorausgesetzt wird; bei unabhängigen Knoten leer.
- KEINE Zyklen. Selbstreferenz ist verboten.
- Wähle "calculator" für reine Arithmetik, "sql_query" für Meta-Fragen ("wie viele Nachrichten letzte Woche"), "keyword_search" für exakte Phrasen, "kb_search" für inhaltliche Suche, "document_outline" zum Navigieren in langen Dokumenten, "chunk_read" zum Erweitern eines bekannten Chunks.
- rationale: ein Satz, max. 200 Zeichen.`
	}
	return `You are a search planner that picks tools per step. Decompose the user question into a directed acyclic graph (DAG) of 1–6 nodes. For each node, pick the right tool from the catalog.

Available tools:
` + catalog + `

Reply ONLY with a JSON object:

{"nodes": [{"id": "n1", "query": "…", "tool": "<name>", "args": {…}, "depends_on": [], "reason": "…"}], "rationale": "short reason"}

Rules:
- Nodes: at most 6, each "query" 5–25 words.
- Maximum depth (longest chain) is 3.
- IDs: short identifiers ("n1", "n2", …), unique within the plan.
- "tool": NAME from the catalog (e.g. "kb_search", "keyword_search", "sql_query"). When unsure: "kb_search".
- "args": JSON object matching the tool's input schema. "kb_id" is injected automatically — do NOT include it.
- depends_on: ids whose answers this node needs; empty for independent nodes.
- NO cycles. Self-references are forbidden.
- Pick "calculator" for pure arithmetic, "sql_query" for meta-questions ("how many messages last week"), "keyword_search" for exact phrases, "kb_search" for content search, "document_outline" to navigate long documents, "chunk_read" to expand context around a known chunk.
- rationale: one sentence, max 200 characters.`
}

// renderToolCatalog formats the tool list for inline embedding in
// the system prompt. One block per tool: name + description (one
// line) + input schema (compact JSON). Schemas are rendered
// verbatim — the registry already produces compact JSON so we
// don't re-marshal.
func renderToolCatalog(tools []PlanToolEntry) string {
	if len(tools) == 0 {
		return "  (no tools available — fall back to kb_search)"
	}
	var b strings.Builder
	for _, t := range tools {
		desc := strings.TrimSpace(t.Description)
		if desc == "" {
			desc = "(no description)"
		}
		schema := strings.TrimSpace(t.InputSchemaJSON)
		if schema == "" {
			schema = "{}"
		}
		// Newlines inside the schema would break the per-tool block
		// in the prompt; collapse to a single line.
		schema = strings.ReplaceAll(schema, "\n", " ")
		fmt.Fprintf(&b, "- %s: %s\n  schema: %s\n", t.Name, desc, schema)
	}
	return b.String()
}
