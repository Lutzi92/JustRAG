package prompts

import "fmt"

// SufficientContextSystem is the system prompt for the holistic
// sufficient-context abstention gate (Q2; Google's ICLR 2025 "sufficient
// context" finding: models hallucinate most when context is *partially*
// relevant but insufficient, and a cheap answer-vs-abstain judgment call
// recovers 2-10% correct-answer fraction). Distinct from the CRAG grader,
// which scores each chunk's relevance independently — this asks whether
// the assembled set as a whole suffices to answer.
//
// The output contract is strict JSON: {"sufficient": true|false}.
// Downstream parsing is tolerant; the structured-outputs contract
// (sufficientContextSpec in internal/ai) enforces the shape on backends
// that support it.
func SufficientContextSystem(lang string) string {
	if lang == "de" {
		return `Du beurteilst, ob bereitgestellter Kontext AUSREICHT, um eine Frage zu beantworten.
"Ausreichend" heißt: Der Kontext enthält alle Informationen für eine definitive, vollständige Antwort auf die Frage.
NICHT ausreichend: thematische Überschneidung ohne die eigentliche Antwort; nur Teilaspekte abgedeckt; benötigte Fakten, Zahlen oder Namen fehlen; die Antwort müsste über den Kontext hinaus ergänzt werden.
Beurteile NUR die Abdeckung — nicht, ob die Frage sinnvoll ist oder der Kontext gut geschrieben ist.

Antworte strikt mit:
{"sufficient": true} oder {"sufficient": false}

Nur das JSON-Objekt — keine Markdown-Zäune, kein Kommentar, keine Erklärung.`
	}
	return `You judge whether provided context is SUFFICIENT to answer a question.
"Sufficient" means: the context contains all the information needed for a definitive, complete answer to the question.
NOT sufficient: topical overlap without the actual answer; only partial aspects covered; required facts, numbers, or names missing; the answer would need to be supplemented beyond the context.
Judge ONLY coverage — not whether the question makes sense or the context is well written.

Output strictly:
{"sufficient": true} or {"sufficient": false}

Reply with ONLY the JSON object — no markdown fences, no commentary, no explanation.`
}

// SufficientContextUser builds the user message for the sufficient-context
// gate. Question before context so the (long) context sits at the end of
// the prompt, matching the other fast-tier judgment calls.
func SufficientContextUser(question, contextText string) string {
	return fmt.Sprintf("Question:\n%s\n\nContext:\n%s", question, contextText)
}
