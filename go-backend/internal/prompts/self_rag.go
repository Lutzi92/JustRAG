package prompts

// SelfRAGSystemPrompt is the AP-D2 unified verifier. Replaces the
// AP-A1 factuality verifier when `chat_self_rag_enabled` is on,
// producing three orthogonal verdicts in a single LLM call:
//
//   - ISREL  per-chunk relevance to the question (post-hoc against
//     the actual answer, not against the query alone like CRAG)
//   - ISSUP  per-claim support: which answer claims are NOT backed
//     by the cited sources (same shape as the legacy
//     FactualityVerifier — feeds the existing refine gate)
//   - ISUSE  whole-answer relevance: does the answer actually
//     address the user's question?
//
// The three verdicts overlap intentionally: an answer can have
// fully-supported claims (ISSUP=clean) yet still not address the
// question (ISUSE=no), or have valid sources (ISREL=relevant) but
// hallucinated claims (ISSUP=flagged). Capturing all three lets
// the refine gate decide whether to edit specific claims (ISSUP
// flags) or rewrite the whole answer (ISUSE=no → holistic).
//
// Strict-JSON output pairs with vLLM/OpenAI Structured Outputs.
// Parser fails closed on malformed output — the chat handler
// surfaces the error label and proceeds without verification
// rather than blocking the user-visible answer.
func SelfRAGSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du bist ein Self-RAG-Verifier. Bewerte die Assistenten-Antwort gegen die Quellen aus drei Perspektiven gleichzeitig:

1. ISREL — Per-Quelle-Relevanz: Ist Quelle [N] für die GESTELLTE FRAGE relevant?
   - "relevant": die Quelle enthält Information, die die Frage direkt beantwortet
   - "partially_relevant": die Quelle berührt das Thema, beantwortet die Frage aber nicht vollständig
   - "irrelevant": die Quelle steht nicht in Bezug zur Frage

2. ISSUP — Pro-Aussage-Belegung: Welche Aussagen der Antwort sind NICHT durch die Quellen gestützt?
   - "unsupported": Aussage in Antwort steht so nicht in den Quellen
   - "contradicted": Quellen widersprechen der Aussage
   - "out_of_scope": Aussage ist Allgemeinwissen ohne Quellenbeleg
   Korrekt belegte Aussagen werden NICHT zurückgegeben.

3. ISUSE — Gesamt-Antwort-Brauchbarkeit: Beantwortet die Antwort die Frage des Nutzers?
   - "yes": die Antwort adressiert die Frage direkt und liefert das Verlangte
   - "partial": die Antwort adressiert teilweise, lässt aber Aspekte aus
   - "no": die Antwort verfehlt die Frage (falsches Thema, ausweichend, abstrakt statt konkret)

Antworte AUSSCHLIESSLICH mit validem JSON:

{"isrel": [{"n": 1, "verdict": "relevant|partially_relevant|irrelevant"}], "issup": [{"claim_text": "<wörtliches Zitat>", "reason": "unsupported|contradicted|out_of_scope", "cited_n": 0}], "isuse": {"verdict": "yes|partial|no", "reason": "<ein Satz max 200 Zeichen>"}}

Regeln:
- isrel: ein Eintrag pro Quelle [N] in der Source-Liste; Reihenfolge der Quellen einhalten.
- issup: nur die problematischen Aussagen, leer wenn die Antwort sauber belegt ist.
- isuse.reason ist Pflicht — kurze Begründung warum yes/partial/no.
- cited_n ist die [N] Quellnummer, die die Antwort zitiert (0 wenn keine).`
	}
	return `You are a Self-RAG verifier. Evaluate the assistant's answer against the sources from three perspectives at once:

1. ISREL — per-source relevance: is source [N] relevant to the QUESTION asked?
   - "relevant": the source carries information that directly answers the question
   - "partially_relevant": the source touches the topic but doesn't fully answer
   - "irrelevant": the source is unrelated to the question

2. ISSUP — per-claim support: which claims in the answer are NOT backed by the sources?
   - "unsupported": claim in answer not present in sources
   - "contradicted": sources contradict the claim
   - "out_of_scope": claim is general knowledge with no source backing
   Correctly-supported claims are NOT returned.

3. ISUSE — overall-answer usefulness: does the answer address the user's question?
   - "yes": answer addresses the question directly and delivers what was asked
   - "partial": answer addresses partially, leaves some aspects out
   - "no": answer misses the question (wrong topic, evasive, abstract instead of concrete)

Reply ONLY with valid JSON:

{"isrel": [{"n": 1, "verdict": "relevant|partially_relevant|irrelevant"}], "issup": [{"claim_text": "<verbatim quote>", "reason": "unsupported|contradicted|out_of_scope", "cited_n": 0}], "isuse": {"verdict": "yes|partial|no", "reason": "<one sentence max 200 chars>"}}

Rules:
- isrel: one entry per source [N] in the source list; preserve the source order.
- issup: only the problematic claims; empty when the answer is cleanly supported.
- isuse.reason is required — short justification of yes/partial/no.
- cited_n is the [N] source number the answer cited (0 if none).`
}

// SelfRAGUserPrompt formats the per-call payload — same shape as
// the legacy FactualityVerifierUserPrompt so source-block formatting
// stays identical and traces are diff-readable across the cutover.
func SelfRAGUserPrompt(question, answer, sourcesBlock string) string {
	return "Question:\n" + question + "\n\nAnswer:\n" + answer + "\n\nSources:\n" + sourcesBlock
}
