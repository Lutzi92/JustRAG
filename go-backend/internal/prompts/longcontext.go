package prompts

// LongContextFindingsPrompt is the system prompt for the map stage of the
// long-context map-reduce consumer. One call per chunk GROUP: the model sees
// a handful of numbered passages ("[3] [Source: …]") and must return the
// claims those passages support, each tagged with the passage number it came
// from and a short verbatim quote. Keeping the original `[N]` numbering is
// what lets the reduce stage — and the citation validator downstream — map a
// finding back to the exact chunk it came from.
func LongContextFindingsPrompt(lang string) string {
	if lang == "de" {
		return `Du extrahierst Belege aus nummerierten Textausschnitten einer Wissensdatenbank für eine übergreifende Synthesefrage.

Regeln:
- Nutze AUSSCHLIESSLICH den Inhalt der gezeigten Ausschnitte. Erfinde nichts.
- Gib pro Aussage genau einen Eintrag zurück: "source_idx" ist die Nummer [N] des Ausschnitts, aus dem die Aussage stammt, "claim" eine knappe eigenständige Aussage (ein Satz), "quote" ein wörtliches Zitat von höchstens 240 Zeichen aus genau diesem Ausschnitt.
- Nur Aussagen, die für die Frage relevant sind. Enthält ein Ausschnitt nichts Relevantes, erzeuge dafür keinen Eintrag.
- Höchstens 3 Einträge pro Ausschnitt.
- Wenn kein Ausschnitt etwas Relevantes enthält, gib eine leere Liste zurück.

Antworte ausschließlich mit JSON der Form {"findings":[{"source_idx":1,"claim":"...","quote":"..."}]}.`
	}
	return `You extract evidence from numbered knowledge-base passages for a broad synthesis question.

Rules:
- Use ONLY the content of the passages shown. Invent nothing.
- Return one entry per claim: "source_idx" is the [N] number of the passage the claim comes from, "claim" is a concise standalone statement (one sentence), "quote" is a verbatim excerpt of at most 240 characters from that same passage.
- Only claims relevant to the question. If a passage holds nothing relevant, emit no entry for it.
- At most 3 entries per passage.
- If no passage holds anything relevant, return an empty list.

Respond ONLY with JSON of the form {"findings":[{"source_idx":1,"claim":"...","quote":"..."}]}.`
}

// LongContextSynthesisSystem is the reduce-stage instruction appended to the
// answer system prompt in map_reduce mode. The answer LLM sees FINDINGS (claim
// + quote per source) rather than the raw chunk bodies, so it must be told
// explicitly that the findings are the whole evidence base and that the `[N]`
// markers are still the citation contract.
func LongContextSynthesisSystem(lang string) string {
	if lang == "de" {
		return `

SYNTHESE-MODUS: Die untenstehenden BEFUNDE sind aus sehr vielen Dokumenten extrahiert worden und bilden deine vollständige Belegbasis. Stütze dich AUSSCHLIESSLICH auf diese Befunde — die vollständigen Originaltexte liegen dir nicht vor. Belege jede Aussage mit der Quellennummer in eckigen Klammern, z. B. [3]. Widersprechen sich Befunde, benenne den Widerspruch ausdrücklich und nenne die betroffenen Quellen. Ist die Beleglage für einen Teil der Frage dünn, sage das, statt zu spekulieren.`
	}
	return `

SYNTHESIS MODE: the FINDINGS below were extracted from a large number of documents and are your complete evidence base. Rely ONLY on these findings — you do not have the full source texts. Cite every statement with its source number in square brackets, e.g. [3]. Where findings conflict, say so explicitly and name the sources involved. Where the evidence is thin for part of the question, say so rather than speculating.`
}
