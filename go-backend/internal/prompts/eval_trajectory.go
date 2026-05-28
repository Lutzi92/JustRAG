package prompts

import (
	"fmt"
	"strings"
)

// TrajectoryDecompositionSystemPrompt returns the strict-JSON judge prompt
// for the decomposition-coverage metric. The judge sees the original
// question and the planner's sub-query list, and decides whether the union
// of sub-queries covers everything the question asks.
func TrajectoryDecompositionSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du bewertest die Qualität einer Frage-Zerlegung in Teilfragen.

Aufgabe:
- Lies die Originalfrage und die Liste der Teilfragen, die ein Agent generiert hat.
- Beurteile, ob die Vereinigung der Teilfragen alle Aspekte der Originalfrage abdeckt.
- Bewerte auf einer Skala 0..1: 1.0 = vollständige Abdeckung, 0.5 = wesentliche Aspekte fehlen, 0.0 = die Teilfragen treffen das Ziel nicht.

Antworte ausschließlich mit validem JSON im Schema:
{"score":0..1,"rationale":"<knappe Begründung>"}

Keine zusätzliche Ausgabe, kein Markdown.`
	}
	return `You evaluate the quality of a question decomposition into sub-queries.

Task:
- Read the original question and the list of sub-queries an agent produced.
- Decide whether the union of the sub-queries covers every aspect the original question asks about.
- Rate on a 0..1 scale: 1.0 = full coverage, 0.5 = significant aspects missing, 0.0 = sub-queries miss the goal.

Respond ONLY with valid JSON matching:
{"score":0..1,"rationale":"<short rationale>"}

No extra output, no markdown.`
}

// TrajectoryDecompositionUserPrompt formats the prompt input for the
// decomposition judge.
func TrajectoryDecompositionUserPrompt(question string, subQueries []string) string {
	var bullets strings.Builder
	for _, q := range subQueries {
		bullets.WriteString("- ")
		bullets.WriteString(q)
		bullets.WriteByte('\n')
	}
	return fmt.Sprintf("Original question:\n%s\n\nSub-queries:\n%s", question, bullets.String())
}

// TrajectoryDecisionSystemPrompt returns the strict-JSON judge prompt for
// the decision-correctness metric. The judge sees the question, the gold
// answer, and the agent's final decision (`abstain`, `answer`, or
// `rewrite_then_answer`) and decides whether the decision was correct.
func TrajectoryDecisionSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du bewertest, ob die Endentscheidung eines Agenten zur Originalfrage korrekt war.

Eingabe enthält:
- Die Originalfrage.
- Die ideale (Gold-)Antwort.
- Die Entscheidung des Agenten (eine der Token-Klassen: "answered" / "abstained" / "rewrote_then_answered").

Aufgabe:
- Bewerte die Korrektheit der Entscheidung auf einer Skala 0..1:
  1.0 = die Entscheidung war eindeutig die richtige.
  0.5 = die Entscheidung war vertretbar, aber nicht optimal.
  0.0 = die Entscheidung war falsch (z. B. abgelehnt, obwohl die Antwort möglich war).

Antworte ausschließlich mit validem JSON im Schema:
{"score":0..1,"rationale":"<knappe Begründung>"}`
	}
	return `You judge whether the agent's final decision on the original question was correct.

Inputs:
- The original question.
- The ideal (gold) answer.
- The agent's decision (one token class: "answered" / "abstained" / "rewrote_then_answered").

Task:
- Rate decision correctness on a 0..1 scale:
  1.0 = the decision was clearly correct.
  0.5 = defensible but not optimal.
  0.0 = wrong (e.g. abstained when the answer was reachable).

Respond ONLY with valid JSON matching:
{"score":0..1,"rationale":"<short rationale>"}`
}

// TrajectoryDecisionUserPrompt formats the prompt input for the decision
// judge.
func TrajectoryDecisionUserPrompt(question, goldAnswer, decision string) string {
	return fmt.Sprintf("Question:\n%s\n\nGold answer:\n%s\n\nAgent decision:\n%s", question, goldAnswer, decision)
}

// TrajectoryRewriteSystemPrompt returns the strict-JSON judge prompt for
// the rewrite-utility metric. The judge sees the original query and the
// rewritten query the agent produced, and rates whether the rewrite is
// likely to improve retrieval. Only relevant when the trajectory contains
// a rewrite step; the runner short-circuits to a nil score when there
// was no rewrite.
func TrajectoryRewriteSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du bewertest, ob das Umschreiben einer Suchanfrage durch einen Agenten die Trefferqualität voraussichtlich verbessert.

Eingabe enthält:
- Die Original-Suchanfrage.
- Die umgeschriebene Suchanfrage des Agenten.

Aufgabe:
- Bewerte den Nutzen des Rewrites auf einer Skala 0..1:
  1.0 = der Rewrite trifft die Intention klar besser.
  0.5 = neutraler Rewrite (Wortlaut anders, Bedeutung gleich).
  0.0 = der Rewrite verschlechtert oder verfälscht die Intention (z. B. Entitätsverlust).

Antworte ausschließlich mit validem JSON im Schema:
{"score":0..1,"rationale":"<knappe Begründung>"}`
	}
	return `You judge whether an agent's query rewrite is likely to improve retrieval.

Inputs:
- The original search query.
- The agent's rewritten search query.

Task:
- Rate rewrite utility on a 0..1 scale:
  1.0 = the rewrite captures the intent more precisely.
  0.5 = neutral rewrite (different wording, same meaning).
  0.0 = the rewrite worsens or distorts the intent (e.g. drops the named entity).

Respond ONLY with valid JSON matching:
{"score":0..1,"rationale":"<short rationale>"}`
}

// TrajectoryRewriteUserPrompt formats the prompt input for the rewrite-
// utility judge.
func TrajectoryRewriteUserPrompt(originalQuery, rewrittenQuery string) string {
	return fmt.Sprintf("Original query:\n%s\n\nRewritten query:\n%s", originalQuery, rewrittenQuery)
}

// TrajectoryToolCallSystemPrompt returns the strict-JSON judge prompt
// for the tool-call-correctness metric (Phase 2 §2.2). The judge sees
// the question, an optional list of expected tool names, and the full
// tool-call sequence the agent issued. It rates whether each call was
// (a) the right tool for the sub-task, (b) well-formed semantically,
// (c) productive (returned usable evidence). The output is a single
// 0..1 score plus a one-line rationale.
//
// When `expected_tools` is empty the judge degrades to "did the agent
// use the available tools defensibly" — useful as a sanity check on
// the trajectory-aware loop but with weaker discriminative power.
func TrajectoryToolCallSystemPrompt(lang string) string {
	if lang == "de" {
		return `Du bewertest, ob ein Agent die richtigen Tools mit sinnvollen Argumenten aufgerufen hat.

Eingabe enthält:
- Die Originalfrage.
- Optional: eine Liste der erwarteten Tools (z. B. ["kb_search","web_search"]).
- Die tatsächliche Tool-Aufrufssequenz des Agenten (Liste von Objekten mit "tool" und "args_summary").

Aufgabe:
- Bewerte die Tool-Auswahl und Argumentqualität auf einer Skala 0..1:
  1.0 = ausschließlich richtige Tools mit präzisen, frage-relevanten Argumenten.
  0.5 = teilweise korrekt (z. B. richtiges Tool, aber unscharfe Args, oder ein passendes Tool fehlt).
  0.0 = grundsätzlich falsch (z. B. Web-Suche statt KB-Suche, oder Args ohne Bezug zur Frage).
- Wenn keine Tools aufgerufen wurden, gib 0.5 zurück, falls die Frage trivial ohne Tools beantwortbar war, sonst 0.0.

Antworte ausschließlich mit validem JSON im Schema:
{"score":0..1,"rationale":"<knappe Begründung>"}`
	}
	return `You judge whether the agent invoked the right tools with sensible arguments.

Inputs:
- The original question.
- Optional: a list of expected tools (e.g. ["kb_search","web_search"]).
- The agent's actual tool-call sequence (list of {tool, args_summary} objects).

Task:
- Rate tool selection + argument quality on a 0..1 scale:
  1.0 = exclusively correct tools with precise, question-relevant args.
  0.5 = partially correct (right tool but vague args, or a sensible tool was missed).
  0.0 = fundamentally wrong (e.g. web search instead of KB search, or args unrelated to the question).
- If no tools were called, return 0.5 when the question is trivially answerable without tools, else 0.0.

Respond ONLY with valid JSON matching:
{"score":0..1,"rationale":"<short rationale>"}`
}

// TrajectoryToolCallUserPrompt formats the prompt input for the
// tool-call-correctness judge. `calls` is a slice of (tool, args_summary)
// pairs the runner extracted from the trajectory.
func TrajectoryToolCallUserPrompt(question string, expectedTools []string, calls []ToolCallSummary) string {
	expected := "(none specified)"
	if len(expectedTools) > 0 {
		var b strings.Builder
		for i, t := range expectedTools {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(t)
		}
		expected = b.String()
	}
	var callBuf strings.Builder
	if len(calls) == 0 {
		callBuf.WriteString("(none)")
	} else {
		for _, c := range calls {
			callBuf.WriteString("- ")
			callBuf.WriteString(c.Tool)
			if c.ArgsSummary != "" {
				callBuf.WriteString(": ")
				callBuf.WriteString(c.ArgsSummary)
			}
			callBuf.WriteByte('\n')
		}
	}
	return fmt.Sprintf("Question:\n%s\n\nExpected tools: %s\n\nActual tool calls:\n%s",
		question, expected, callBuf.String())
}

// ToolCallSummary is the minimal summary the judge needs for one tool
// call: the tool name and a short stringified args description. Keeping
// args opaque avoids feeding the judge raw JSON it would have to parse.
type ToolCallSummary struct {
	Tool        string
	ArgsSummary string
}
