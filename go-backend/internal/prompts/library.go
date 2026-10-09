package prompts

// LibraryNotice is appended to the system prompt of a KB-less library chat
// turn: the only evidence is the user's selected files.
func LibraryNotice(lang string) string {
	if lang == "de" {
		return "BIBLIOTHEKS-MODUS: Antworte ausschließlich auf Grundlage der unten bereitgestellten, vom Nutzer ausgewählten Dateien. Belege jede Aussage mit der Quellennummer in eckigen Klammern, z. B. [2]. Enthalten die Dateien keine Antwort, sage das ausdrücklich."
	}
	return "LIBRARY MODE: Answer only from the user's selected files below. Cite every statement with its source number in square brackets, e.g. [2]. If the files do not contain the answer, say so explicitly."
}
