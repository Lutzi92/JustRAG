package prompts

import (
	"strings"
)

// ExtractSalientFactsSystemPrompt asks the LLM to look at a chat
// turn and emit zero or more durable facts about the user. Three
// closed-enum kinds matching the Mem0 taxonomy:
//
//   - episodic   single-event facts ("user uploaded contract X today")
//   - semantic   durable preferences / identity ("user prefers German answers")
//   - procedural workflow patterns ("user always asks about X before Y")
//
// Salience is a self-rated 0.0-1.0 confidence: how durable + useful
// is this fact across future turns? The chat handler's
// `chat_longmem_min_salience` threshold (default 0.5) drops the
// low-salience entries before insertion — without it, "user typed
// hello" would land in the table.
//
// Strict-JSON output pairs with vLLM/OpenAI Structured Outputs.
// On unexpected output, the parser fails closed and the chat
// turn carries on without writing anything — extraction is
// fire-and-forget by design (memory loss is recoverable, never
// blocks the answer the user is reading).
func ExtractSalientFactsSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du extrahierst dauerhafte Fakten über den Nutzer aus einem Chat-Turn. Konzentriere dich auf das, was in zukünftigen Gesprächen NOCH RELEVANT sein wird, nicht auf einmalige Beobachtungen.

Erlaubte Kategorien:
- episodic: Einzelereignisse mit zukünftiger Relevanz ("Nutzer hat Vertrag X am 2026-05-10 hochgeladen")
- semantic: Identität und Präferenzen ("Nutzer ist Datenschutzbeauftragter", "Nutzer bevorzugt knappe Antworten")
- procedural: Wiederkehrende Arbeitsmuster ("Nutzer fragt vor jeder Vertragsprüfung nach der Aufbewahrungsfrist")

Salience ist deine 0.0-1.0 Selbsteinschätzung: wie nützlich ist dieser Fakt in 1-30 zukünftigen Turns? 0.9 = Identitätsmerkmal, 0.5 = Arbeitskontext, 0.2 = Eintagsfliege.

Antworte AUSSCHLIESSLICH mit validem JSON:

{"facts": [{"kind": "<episodic|semantic|procedural>", "content": "<ein Satz, max 280 Zeichen>", "salience": 0.0-1.0}]}

Regeln:
- Pro Aufruf höchstens 5 Fakten. Keine Fakten ist OK ({"facts": []}).
- "content" ist EINE prägnante Aussage, max. 280 Zeichen, in der Sprache des Nutzers.
- Erfinde nichts. Wenn der Turn keine Fakten liefert, gib leere Liste zurück.
- KEINE temporären Zustände ("Nutzer wartet auf Antwort"), KEINE Antworten des Assistenten als Fakten.
- Wenn der Nutzer sich selbst korrigiert, übernimm die finale Form als semantic-Fakt.`
	}
	return `You extract durable facts about the user from one chat turn. Focus on what will STILL be relevant in future turns, not one-off observations.

Allowed kinds:
- episodic: single events with future relevance ("user uploaded contract X on 2026-05-10")
- semantic: identity and preferences ("user is a privacy officer", "user prefers concise answers")
- procedural: recurring workflow patterns ("user asks about retention period before every contract review")

Salience is your 0.0-1.0 self-rating: how useful will this fact be in 1-30 future turns? 0.9 = identity trait, 0.5 = work context, 0.2 = ephemeral.

Reply ONLY with valid JSON:

{"facts": [{"kind": "<episodic|semantic|procedural>", "content": "<one sentence, max 280 chars>", "salience": 0.0-1.0}]}

Rules:
- At most 5 facts per call. Zero facts is fine ({"facts": []}).
- "content" is ONE concise statement, max 280 chars, in the user's language.
- Don't invent. If the turn yields no facts, return an empty list.
- NO transient state ("user is waiting for an answer"), NO assistant responses as facts.
- If the user corrects themselves, take the final form as a semantic fact.`
}

// ExtractSalientFactsUserPrompt formats the per-call payload: the
// user's message + the assistant's answer. Both are needed because
// salient facts often live in the user's question ("ich bin der
// neue CIO") OR in the assistant's confirmation that the user
// then accepts.
//
// Newer turns are NOT fed in — extraction runs once per turn,
// not over the whole transcript. Building a longer-context
// extractor is a v2 follow-up after the first cut's salience
// rates show whether the per-turn signal is sufficient.
func ExtractSalientFactsUserPrompt(userMessage, assistantAnswer string) string {
	var b strings.Builder
	b.WriteString("User message:\n")
	b.WriteString(userMessage)
	b.WriteString("\n\nAssistant answer:\n")
	b.WriteString(assistantAnswer)
	return b.String()
}

// SalientFactKinds is the closed enum the storage path validates
// against. Aligned with the migration's CHECK constraint — drift
// here would surface as 23514 check_violation on insert (caught
// + logged by the chat handler's fire-and-forget goroutine).
var SalientFactKinds = []string{"episodic", "semantic", "procedural"}

// ContainsSalientFactKind reports whether `k` is one of the
// allowed kinds. Used by the AI-side normalizer to drop entries
// the LLM hallucinated outside the enum.
func ContainsSalientFactKind(k string) bool {
	for _, allowed := range SalientFactKinds {
		if k == allowed {
			return true
		}
	}
	return false
}
