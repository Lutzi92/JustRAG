package prompts

// ComparisonModePrompt returns the system instruction for one comparison mode.
// The model receives an uploaded-file SECTION plus retrieved KB peer passages and
// must return findings via the strict comparison_findings schema.
func ComparisonModePrompt(mode, lang string) string {
	de := lang == "de"
	switch mode {
	case "contradiction":
		if de {
			return "Du prüfst, ob ein Abschnitt eines neu hochgeladenen Dokuments den " +
				"vorhandenen KB-Passagen WIDERSPRICHT (z. B. abweichende ECTS, Prüfungsform, " +
				"Voraussetzungen). Melde nur echte Widersprüche zu konkreten KB-Passagen. " +
				"Wenn kein Widerspruch besteht, gib eine leere Liste zurück. " +
				"Zitiere die widersprechende KB-Stelle in citedQuote und die betroffenen citedFileIds. " +
				"Zitiere die betroffene Stelle des hochgeladenen Dokuments in uploadQuote."
		}
		return "You check whether a section of a newly uploaded document CONTRADICTS the " +
			"retrieved KB passages (e.g. different ECTS, exam form, prerequisites). Report only " +
			"genuine contradictions against concrete KB passages. If none, return an empty list. " +
			"Put the conflicting KB text in citedQuote and the source files in citedFileIds. " +
			"Quote the relevant uploaded passage in uploadQuote."
	case "formal":
		if de {
			return "Du prüfst die FORMALE/STRUKTURELLE Korrektheit eines Abschnitts. Leite die " +
				"erwartete Struktur aus den vorhandenen vergleichbaren KB-Passagen ab (welche " +
				"Felder/Abschnitte üblich sind) und melde Abweichungen. Erfinde keine Regeln; " +
				"wenn keine Peers vorliegen, melde nur offensichtlich fehlerhafte Struktur mit " +
				"Severity low. Leere Liste, wenn alles korrekt. " +
				"Zitiere die betroffene hochgeladene Stelle in uploadQuote."
		}
		return "You check the FORMAL/STRUCTURAL correctness of a section. Infer the expected " +
			"structure from the retrieved comparable KB passages (which fields/sections are usual) " +
			"and report deviations. Do not invent rules; if no peers are present, only flag " +
			"obviously malformed structure at severity low. Empty list if correct. " +
			"Quote the offending uploaded passage in uploadQuote."
	case "completeness":
		if de {
			return "Du prüfst die VOLLSTÄNDIGKEIT: Gibt es Inhalte, die in vergleichbaren " +
				"vorhandenen KB-Dokumenten vorkommen, im hochgeladenen Abschnitt aber fehlen? " +
				"Melde fehlende Bestandteile mit Bezug auf die KB-Passage, in der sie vorkommen. " +
				"Leere Liste, wenn nichts fehlt. " +
				"Zitiere in uploadQuote die hochgeladene Stelle, an der der fehlende Inhalt erwartet würde (leer, falls nicht zutreffend)."
		}
		return "You check COMPLETENESS: is there content present in comparable existing KB " +
			"documents but missing from the uploaded section? Report missing parts, referencing " +
			"the KB passage where they appear. Empty list if nothing is missing. " +
			"In uploadQuote, quote the uploaded text nearest to where the missing content belongs (empty if not applicable)."
	default:
		if de {
			return "Vergleiche den hochgeladenen Abschnitt mit den KB-Passagen und melde relevante " +
				"Probleme. Leere Liste, wenn keine."
		}
		return "Compare the uploaded section against the KB passages and report relevant issues. " +
			"Empty list if none."
	}
}

// ComparisonSummaryPrompt instructs the answer LLM to write the prose gist over findings.
func ComparisonSummaryPrompt(lang string) string {
	if lang == "de" {
		return "Fasse die folgenden Vergleichs-Befunde in 2–4 Sätzen zusammen: Wie gut passt das " +
			"hochgeladene Dokument zu den vorhandenen KB-Dokumenten, und was sind die wichtigsten " +
			"Probleme? Erfinde keine zusätzlichen Befunde. Schreibe eine kurze Zusammenfassung (summary)."
	}
	return "Summarize the following comparison findings in 2–4 sentences: how well does the uploaded " +
		"document fit the existing KB documents, and what are the most important issues? Do not invent " +
		"additional findings. Write a short summary."
}
