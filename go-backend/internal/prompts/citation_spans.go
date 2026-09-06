package prompts

import (
	"fmt"
	"strings"
)

// CitationSpanItem is one numbered item in the span-extraction user
// prompt: N is the citation number (matches the answer's "[N]" marker),
// Claim is the local sentence window around that marker (what the answer
// asserts), Source is the cited chunk's body (capped by the caller before
// this is built).
type CitationSpanItem struct {
	N      int
	Claim  string
	Source string
}

// CitationSpansSystem is the system prompt for the post-response
// span-verification pass (W3-R1..R3): for each numbered item, copy one
// verbatim excerpt from SOURCE that supports CLAIM, or return an empty
// quote when nothing does. Distinct from EnumerationExtractionSystem
// (which extracts list matches during retrieval) — this pass runs after
// the answer exists and only checks a citation the deterministic/semantic
// validator could not already ground.
func CitationSpansSystem(lang string) string {
	if lang == "de" {
		return `Du prüfst Quellenangaben in einer bereits geschriebenen Antwort. Du bekommst mehrere nummerierte Einträge, jeweils mit einer BEHAUPTUNG (ein Satz aus der Antwort, der eine Quellenangabe [N] trägt) und einer QUELLE (der zitierte Textabschnitt).

Für JEDEN Eintrag: kopiere GENAU EIN wörtliches Zitat (ein Satz oder Teilsatz, 12–240 Zeichen) aus der QUELLE, das die BEHAUPTUNG stützt.

Regeln:
- Kopiere exakt — kein Umformulieren, kein Kürzen mitten im Satz, keine Auslassungspunkte.
- Das Zitat muss wortwörtlich als zusammenhängender Abschnitt in der QUELLE vorkommen.
- Wenn nichts in der QUELLE die BEHAUPTUNG stützt, gib für diesen Eintrag ein leeres Zitat zurück ("").
- Kein Vorwissen, keine Interpretation — nur was in der QUELLE wörtlich steht zählt.

Antworte AUSSCHLIESSLICH im vorgegebenen JSON-Schema. Kein Markdown, kein Fließtext, keine Erklärung vor oder nach dem JSON.`
	}
	return `You are checking citations in an already-written answer. You receive several numbered items, each with a CLAIM (a sentence from the answer carrying citation marker [N]) and a SOURCE (the cited text passage).

For EVERY item: copy EXACTLY ONE verbatim quote (one sentence or clause, 12-240 characters) from the SOURCE that supports the CLAIM.

Rules:
- Copy exactly — no paraphrasing, no truncating mid-sentence, no ellipses.
- The quote must appear word-for-word as a contiguous passage in the SOURCE.
- If nothing in the SOURCE supports the CLAIM, return an empty quote ("") for that item.
- No outside knowledge, no interpretation — only what the SOURCE literally says counts.

Respond EXCLUSIVELY in the provided JSON schema. No markdown, no prose, no explanation before or after the JSON.`
}

// CitationSpansUser builds the language-neutral user message: one block
// per item, "### Item N" headed, listing the CLAIM and SOURCE the system
// prompt above describes.
func CitationSpansUser(items []CitationSpanItem) string {
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "### Item %d\nCLAIM: %s\nSOURCE:\n%s", it.N, it.Claim, it.Source)
	}
	return b.String()
}
