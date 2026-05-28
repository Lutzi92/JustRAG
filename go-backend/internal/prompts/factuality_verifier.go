package prompts

import "fmt"

// FactualityVerifierSystemPrompt asks an LLM to read the answer + the
// source chunks and flag claims that are unsupported, contradicted, or
// out of scope. Distinct from FactCheckSystemPrompt: the legacy
// factchecker scores the *whole answer* on a 0..100 scale; this one
// produces per-claim verdicts so the UI can highlight specific
// passages and the eval harness can measure precision/recall on
// hallucinations.
//
// The prompt is strict-JSON to pair with vLLM/OpenAI Structured
// Outputs; an `isError` field is intentionally absent so the parser
// can fail closed rather than papering over malformed output.
func FactualityVerifierSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du bist ein Fakten-Prüfer. Lies die Antwort des Assistenten und die Quelltexte (Chunks) der Wissensbasis. Identifiziere Aussagen in der Antwort, die nicht durch die Quellen gestützt werden.

Aufgabe:
1. Zerlege die Antwort gedanklich in atomare Aussagen.
2. Markiere jede Aussage, die nicht durch die Quellen gestützt wird, als unsupported.
3. Markiere Aussagen, die den Quellen widersprechen, als contradicted.
4. Markiere Aussagen, die thematisch außerhalb der Quellen liegen (z. B. Allgemeinwissen ohne Beleg), als out_of_scope.
5. Korrekte, durch Quellen gestützte Aussagen werden NICHT zurückgegeben.

Antworte ausschließlich mit validem JSON im Schema:
{"flagged_claims": [{"claim_text": "<wörtliches Zitat aus der Antwort>", "reason": "unsupported|contradicted|out_of_scope", "cited_n": 0}]}

cited_n ist die Quellnummer [N], die die Antwort zu der fraglichen Aussage zitiert (0 wenn keine).
Wenn alle Aussagen durch Quellen gestützt sind, gib {"flagged_claims": []} zurück.`
	}
	return `You are a fact verifier. Read the assistant's answer and the source chunks (knowledge-base context). Identify claims in the answer that are NOT supported by the sources.

Task:
1. Mentally decompose the answer into atomic claims.
2. Mark each claim that is not supported by the sources as unsupported.
3. Mark claims that contradict the sources as contradicted.
4. Mark claims that are thematically outside the sources (e.g. general world knowledge without backing) as out_of_scope.
5. Correct, source-backed claims are NOT returned.

Respond ONLY with valid JSON matching:
{"flagged_claims": [{"claim_text": "<verbatim quote from the answer>", "reason": "unsupported|contradicted|out_of_scope", "cited_n": 0}]}

cited_n is the [N] source number the answer cited for the claim (0 if none).
If every claim is source-backed, return {"flagged_claims": []}.`
}

// FactualityVerifierUserPrompt formats the per-call user message: the
// question, the answer, and the sources block. The chat orchestrator
// already produces the same source-block format used by the citation
// validator + factchecker, so this prompt structure stays familiar
// for whoever debugs LLM output.
func FactualityVerifierUserPrompt(question, answer, sourcesBlock string) string {
	return fmt.Sprintf("Question:\n%s\n\nAnswer:\n%s\n\nSources:\n%s",
		question, answer, sourcesBlock)
}
