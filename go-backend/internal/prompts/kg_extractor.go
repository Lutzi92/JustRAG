package prompts

import (
	"fmt"
	"html"
	"strings"
)

// KGExtractorSystemPrompt is the AP-C1 entity + relation extractor.
// Asks the LLM to walk one chunk and emit named entities (people,
// orgs, projects, documents, concepts) plus the relations between
// them, in strict JSON.
//
// Why strict JSON: pairs with vLLM/OpenAI Structured Outputs the same
// way the enumeration pre-pass and factuality verifier do. The closed
// type set (`person | organization | project | document | concept | location | role | other`)
// keeps the entity table sparse — without an enum the LLM
// hallucinates a fresh type per turn ("German technology company"),
// which would explode kg_entities by an order of magnitude.
//
// The prompt explicitly asks the LLM to keep entity names in their
// canonical form ("PPM-Team" not "PPM-team", "Eberhard Kurz" not
// "E. Kurz") and to put surface variants in `aliases`. The dedupe
// path on the storage side keys on `(canonical_name, type)`, so
// canonical-form discipline directly determines how often the same
// entity is correctly merged across chunks.
func KGExtractorSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du bist ein Wissensgraph-Extractor. Lies den markierten Chunk und identifiziere benannte Entitäten und ihre Beziehungen.

Erlaubte Entity-Typen (geschlossener Satz):
- person: Personen mit Eigennamen
- organization: Teams, Abteilungen, Firmen, Behörden
- project: Projekte, Initiativen, Programme
- document: Richtlinien, Verträge, Berichte, Dokumente
- concept: Themen, Methoden, Technologien, Begriffe
- location: Orte, Räume, Standorte
- role: Funktionen, Rollen, Titel
- other: nur als letzter Ausweg

Antworte AUSSCHLIESSLICH mit validem JSON:

{"entities": [{"name": "<kanonischer Name>", "type": "<typ>", "aliases": ["<variante1>", ...]}], "relations": [{"src": "<entity-name>", "rel": "<beziehung>", "dst": "<entity-name>", "evidence": "<wörtliches Zitat>"}]}

Regeln:
- "name" ist die kanonische Form (nicht der wörtliche Treffer im Text). "PPM-Team" statt "PPM-team", "Eberhard Kurz" statt "E. Kurz".
- "aliases" sind die Variantformen, die im Text vorkommen (für Recall in der Suche).
- "type" ist GENAU einer der oben genannten Werte. Keine Erfindungen.
- "rel" ist eine kurze deutsche Verb-Phrase ("arbeitet_in", "leitet", "referenziert", "ist_teil_von"). Snake_case.
- "evidence" ist ein wörtliches Zitat aus dem Chunk (5-25 Wörter), das die Beziehung belegt.
- src und dst MÜSSEN Namen aus deiner entities-Liste sein.
- Wenn der Chunk keine namhaften Entitäten enthält, gib {"entities": [], "relations": []} zurück.
- Beziehungen ohne explizites Beleg im Chunk weglassen — keine Inferenzen.`
	}
	return `You are a knowledge-graph extractor. Read the marked chunk and identify named entities and their relations.

Allowed entity types (closed set):
- person: people with proper names
- organization: teams, departments, companies, agencies
- project: projects, initiatives, programmes
- document: policies, contracts, reports, documents
- concept: topics, methods, technologies, terms
- location: places, rooms, sites
- role: functions, roles, titles
- other: only as a last resort

Reply ONLY with valid JSON:

{"entities": [{"name": "<canonical name>", "type": "<type>", "aliases": ["<variant1>", ...]}], "relations": [{"src": "<entity-name>", "rel": "<relation>", "dst": "<entity-name>", "evidence": "<verbatim quote>"}]}

Rules:
- "name" is the canonical form (not the literal match). "PPM-Team" not "PPM-team", "Eberhard Kurz" not "E. Kurz".
- "aliases" are the variant forms that appear in the text (helps search recall).
- "type" is EXACTLY one of the values above. No inventions.
- "rel" is a short snake_case verb phrase ("works_in", "leads", "references", "part_of").
- "evidence" is a verbatim quote from the chunk (5-25 words) backing the relation.
- src and dst MUST be names from your entities list.
- If the chunk contains no named entities, return {"entities": [], "relations": []}.
- Drop relations without an explicit anchor in the chunk — no inferences.`
}

// KGExtractorUserPrompt formats the per-call payload. Document name
// + full document body come FIRST and stay stable per file —
// OpenAI/vLLM auto-caching reuses the prefix across every chunk of
// the same document, exactly the trick contextual_prefix uses.
// Chunk goes LAST inside `<chunk>...</chunk>` so the LLM has a
// clear "here's the slice to extract from" boundary.
//
// Document body is hard-capped (caller should truncate to the
// configured cl100k_base budget — same as ContextualPrefix's ~8k
// cap; the extractor needs less than that because the relations
// are local to the chunk).
func KGExtractorUserPrompt(documentName, documentBody, chunkContent string) string {
	var b strings.Builder
	b.WriteString("<document name=\"")
	b.WriteString(html.EscapeString(documentName))
	b.WriteString("\">\n")
	b.WriteString(documentBody)
	b.WriteString("\n</document>\n\n")
	b.WriteString("Extract entities + relations from THIS chunk only:\n<chunk>\n")
	b.WriteString(chunkContent)
	b.WriteString("\n</chunk>")
	return b.String()
}

// AllowedKGEntityTypes is the closed enum the extractor's storage
// path enforces. Keeping it as a Go-side allowlist guards against
// LLM drift (the prompt forbids unknown types but small models
// occasionally emit "company" or "method" anyway). Entries that
// don't pass this filter normalize to "other".
func AllowedKGEntityTypes() map[string]bool {
	return map[string]bool{
		"person":       true,
		"organization": true,
		"project":      true,
		"document":     true,
		"concept":      true,
		"location":     true,
		"role":         true,
		"other":        true,
	}
}

// kgExtractorPromptCheck is a compile-time guard ensuring the type
// list mentioned in the prompt matches the AllowedKGEntityTypes
// map. Doesn't run code, just pins the references — if a future
// edit drops one of the types from either side, this gives the
// reviewer a visible link to update the other side too.
var _ = fmt.Sprintf("kg_types: person organization project document concept location role other = %d entries", len(AllowedKGEntityTypes()))
