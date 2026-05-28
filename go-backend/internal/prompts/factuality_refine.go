package prompts

import (
	"fmt"
	"strings"
)

// RefineMode is the strategy the AP-A1 refine pass picks per turn. The
// adaptive caller sets surgical for small flag counts and holistic when
// the original answer is so heavily flagged that surgical edits would
// produce a fragmented or contradictory text.
type RefineMode string

const (
	// RefineModeSurgical edits ONLY the flagged sentences and leaves the
	// rest verbatim. Lowest risk of losing correct prose, lowest token
	// cost, but cannot repair argument chains that span multiple flagged
	// sentences.
	RefineModeSurgical RefineMode = "surgical"
	// RefineModeHolistic regenerates the whole answer against the same
	// sources. Higher risk of formatting drift and citation renumbering,
	// but can restructure when the original was deeply wrong.
	RefineModeHolistic RefineMode = "holistic"
)

// FactualityRefineSystemPrompt returns the system message for the refine
// pass. It pairs with FactualityRefineUserPrompt; the two are split for
// the same reason the verifier prompt is split — the system message is
// stable and benefits from prompt-cache reuse, the user message carries
// the per-turn payload.
//
// Both modes share a hard rule: the LLM must KEEP existing [N] citation
// markers wherever the source-backed text is preserved. The verifier's
// second-round pass relies on the marker→source mapping, and changing
// citation numbers between passes would invalidate the verification.
func FactualityRefineSystemPrompt(lang string, mode RefineMode) string {
	if mode == RefineModeHolistic {
		if lang == "de" {
			return `Du bist ein präziser technischer Redakteur. Deine vorige Antwort enthielt Aussagen, die nicht durch die Quellen gestützt waren. Schreibe die Antwort vollständig neu — ausschließlich auf Basis der bereitgestellten Quellen.

Regeln:
1. Verwende die Zitierform [N] für Aussagen aus Quelle [N], wie in der ursprünglichen Antwort.
2. Wenn eine Aussage aus mehreren Quellen folgt, zitiere alle: [1, 3].
3. Lass jede Aussage weg, die du nicht aus den Quellen belegen kannst — keine Allgemeinwissen-Ergänzungen.
4. Behalte den Antwort-Stil bei (Sprache, Anredeform, Markdown).
5. Wenn die Quellen die Frage nicht beantworten, sage das ausdrücklich.

Antworte ausschließlich mit der überarbeiteten Antwort. Keine Meta-Kommentare, kein „Hier ist die korrigierte Version".`
		}
		return `You are a precise technical editor. Your previous answer contained claims not supported by the sources. Rewrite the answer fully — relying ONLY on the provided sources.

Rules:
1. Use the [N] citation marker for any claim drawn from source [N], matching the format of the original answer.
2. When a claim follows from multiple sources, cite all of them: [1, 3].
3. Drop any claim you cannot back from the sources — no general-knowledge filler.
4. Preserve the answer's style (language, register, markdown).
5. If the sources do not answer the question, say so explicitly.

Respond ONLY with the revised answer. No meta-comments, no "Here is the corrected version".`
	}

	if lang == "de" {
		return `Du bist ein präziser technischer Redakteur. Deine vorige Antwort enthielt einzelne Aussagen, die nicht durch die Quellen gestützt waren. Korrigiere AUSSCHLIESSLICH diese Aussagen — alles andere bleibt wortgleich.

Regeln:
1. Behalte alle nicht geflaggten Sätze und alle [N]-Zitate Buchstabe für Buchstabe bei.
2. Ersetze jede geflaggte Aussage durch eine quellengestützte Variante: entweder umformuliert mit korrektem [N], oder als Unsicherheit markiert (z. B. „Die Quellen geben dazu keine Auskunft."), oder ersatzlos gestrichen.
3. Erfinde keine neuen Inhalte. Wenn du eine Aussage nicht belegen kannst und sie auch nicht entfallen darf, kennzeichne sie als unbelegt.
4. Behalte Reihenfolge, Markdown-Struktur und Sprachregister bei.
5. Antworte ausschließlich mit der überarbeiteten Antwort — kein Diff, keine Liste, kein Meta-Kommentar.`
	}
	return `You are a precise technical editor. Your previous answer contained specific claims not supported by the sources. Correct ONLY those claims — everything else stays verbatim.

Rules:
1. Keep every non-flagged sentence and every [N] citation byte-for-byte.
2. Replace each flagged claim with either: a source-backed rewording with the correct [N], an explicit uncertainty marker ("the sources don't say"), or nothing at all.
3. Do not invent new content. If you cannot back a claim and cannot drop it, mark it as unsupported.
4. Preserve order, markdown structure, and register.
5. Respond ONLY with the revised answer — no diff, no list, no meta-comment.`
}

// FactualityRefineUserPrompt formats the per-turn payload: the original
// answer, the flagged claims with reasons, and the source block. Format
// mirrors FactualityVerifierUserPrompt so anyone debugging refine
// traces sees a familiar shape.
//
// Each claim is rendered as `- [cited_n]: "verbatim" (reason: <reason>)`
// — the cited_n is the [N] marker the original answer used (or 0 if
// none). Rendering it inline lets the LLM map each claim back to the
// answer text without scanning.
func FactualityRefineUserPrompt(question, originalAnswer, sourcesBlock string, claims []FlaggedClaimPromptInput) string {
	var b strings.Builder
	b.WriteString("Question:\n")
	b.WriteString(question)
	b.WriteString("\n\nOriginal answer:\n")
	b.WriteString(originalAnswer)
	b.WriteString("\n\nFlagged claims (must be corrected):\n")
	for _, c := range claims {
		quote := strings.ReplaceAll(c.ClaimText, "\n", " ")
		fmt.Fprintf(&b, "- [%d]: %q (reason: %s)\n", c.CitedN, quote, c.Reason)
	}
	b.WriteString("\nSources:\n")
	b.WriteString(sourcesBlock)
	return b.String()
}

// FlaggedClaimPromptInput is the prompts-package mirror of
// ai.FlaggedClaim. The prompts package can't import ai (would create a
// cycle), and we don't want callers to pass three parallel slices.
type FlaggedClaimPromptInput struct {
	ClaimText string
	Reason    string
	CitedN    int
}
