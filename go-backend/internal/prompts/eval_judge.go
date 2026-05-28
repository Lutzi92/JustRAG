package prompts

import (
	"fmt"
	"strings"
)

// FaithfulnessSystemPrompt returns the system prompt for the judge that
// scores answer faithfulness: it decomposes the answer into atomic claims
// and decides whether each is supported by the retrieved context only.
func FaithfulnessSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du bist ein strenger Fakten-Prüfer.

Aufgabe:
1. Zerlege die gegebene Antwort in atomare, für sich stehende Aussagen ("Claims").
2. Entscheide für jede Aussage, ob sie ausschließlich durch den gegebenen Kontext gestützt wird (supported=true) oder nicht (supported=false).
3. Allgemeinwissen zählt NICHT als Stütze; nur der Kontext zählt.

Antworte ausschließlich mit validem JSON im Schema:
{"claims":[{"text":"<claim>","supported":true|false}]}

Gib keinen zusätzlichen Text, keine Erklärung, kein Markdown aus.`
	}
	return `You are a strict fact checker.

Task:
1. Break the given answer into atomic, standalone claims.
2. For each claim, decide whether it is supported *solely* by the provided context (supported=true) or not (supported=false).
3. General world knowledge does NOT count as support; only the context does.

Respond ONLY with valid JSON matching:
{"claims":[{"text":"<claim>","supported":true|false}]}

Do not output any other text, explanation, or markdown.`
}

// FaithfulnessUserPrompt bundles the question, answer, and context into the
// user message for the faithfulness judge.
func FaithfulnessUserPrompt(question, answer, contextText string) string {
	return fmt.Sprintf(`QUESTION:
%s

ANSWER:
%s

CONTEXT:
%s`, question, answer, contextText)
}

// AnswerRelevanceSystemPrompt returns the system prompt for the answer-relevance
// judge. Likert 1..5, caller normalizes to [0,1].
func AnswerRelevanceSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du bewertest, wie gut eine Antwort die gestellte Frage beantwortet.

Bewertungsskala (score):
5 = Antwort beantwortet die Frage vollständig und präzise.
4 = Antwort ist überwiegend korrekt mit kleineren Lücken.
3 = Antwort ist teilweise relevant, aber lückenhaft oder tangential.
2 = Antwort ist größtenteils irrelevant.
1 = Antwort geht auf die Frage gar nicht ein (oder verweigert die Antwort komplett ohne Rechtfertigung).

Antworte ausschließlich mit validem JSON:
{"score":1..5,"reasoning":"kurze Begründung"}

Kein zusätzlicher Text.`
	}
	return `You judge how well an answer addresses the given question.

Scale (score):
5 = answer fully and precisely addresses the question.
4 = mostly correct, minor gaps.
3 = partially relevant, has gaps or is tangential.
2 = mostly irrelevant.
1 = does not address the question at all (or refuses without justification).

Respond ONLY with valid JSON:
{"score":1..5,"reasoning":"brief justification"}

No other text.`
}

// AnswerRelevanceUserPrompt bundles the question and answer.
func AnswerRelevanceUserPrompt(question, answer string) string {
	return fmt.Sprintf(`QUESTION:
%s

ANSWER:
%s`, question, answer)
}

// ContextPrecisionSystemPrompt returns the system prompt for the per-chunk
// relevance judge. Returns an array of booleans in chunk order.
func ContextPrecisionSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du bewertest die Relevanz einzelner abgerufener Textstücke ("Chunks") für eine gestellte Frage.

Aufgabe:
- Lies die Frage und jedes nummerierte Chunk.
- Entscheide für jedes Chunk, ob es Informationen enthält, die zur Beantwortung der Frage beitragen können (relevant=true) oder nicht (relevant=false).
- Reihenfolge der Ergebnisse muss der Reihenfolge der Chunks entsprechen.

Antworte ausschließlich mit validem JSON:
{"relevant":[true|false, ...]}

Keine Begründung, kein weiterer Text.`
	}
	return `You judge the relevance of individual retrieved text chunks for a question.

Task:
- Read the question and each numbered chunk.
- For each chunk, decide if it contains information that helps answer the question (relevant=true) or not (relevant=false).
- Order of results must match order of chunks.

Respond ONLY with valid JSON:
{"relevant":[true|false, ...]}

No reasoning, no other text.`
}

// ContextPrecisionUserPrompt bundles the question and numbered chunk list.
func ContextPrecisionUserPrompt(question string, chunks []string) string {
	var sb strings.Builder
	sb.WriteString("QUESTION:\n")
	sb.WriteString(question)
	sb.WriteString("\n\nCHUNKS:\n")
	for i, c := range chunks {
		fmt.Fprintf(&sb, "[%d] %s\n\n", i+1, c)
	}
	return sb.String()
}
