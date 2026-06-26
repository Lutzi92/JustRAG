package prompts

// DriftFollowupsPrompt is the system prompt for the DRIFT follow-up
// generator. Given a global-synthesis question and a community-level
// primer, it asks the model for specific, independently-retrievable
// follow-up sub-questions that drill into the themes the primer surfaces.
func DriftFollowupsPrompt(lang string) string {
	if lang == "de" {
		return `Du unterstützt eine wissensbasierte Recherche. Gegeben sind eine übergreifende Nutzerfrage und eine Zusammenfassung thematischer Cluster ("Community-Primer") aus der Wissensdatenbank.

Erzeuge 2 bis 6 konkrete Folgefragen, die jeweils einzeln per Suche beantwortbar sind und die in der Frage bzw. im Primer angelegten Themen, Entitäten und Zusammenhänge vertiefen. Jede Folgefrage muss eigenständig verständlich sein (keine Pronomen ohne Bezug). Wenn kein Primer vorhanden ist, zerlege die ursprüngliche Frage selbst.

Antworte ausschließlich mit einem JSON-Array von Strings, z. B. ["Frage 1","Frage 2"]. Keine weiteren Erläuterungen.`
	}
	return `You support a knowledge-base research task. You are given a broad user question and a summary of thematic clusters ("community primer") drawn from the knowledge base.

Produce 2 to 6 specific follow-up questions, each independently answerable by a single search, that drill into the themes, entities, and relationships implied by the question and the primer. Each follow-up must stand on its own (no dangling pronouns). If no primer is available, decompose the original question itself.

Respond ONLY with a JSON array of strings, e.g. ["Question 1","Question 2"]. No other commentary.`
}
