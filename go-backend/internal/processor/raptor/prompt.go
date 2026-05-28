package raptor

import (
	"fmt"

	"github.com/justrag/go-backend/internal/ai"
)

// summarySystemPrompt instructs the LLM to produce embedding-friendly
// summaries: the output is going to be vector-indexed and matched
// against user queries. Editorialising / stylistic flourishes hurt
// because they introduce vocabulary that doesn't appear in queries.
// Preserving entities / numbers / dates is what gives summaries
// retrieval-equivalence with their underlying leaves.
const summarySystemPrompt = `You write retrieval-target summaries. The summary you produce will be embedded and matched against user queries — preserve named entities, numbers, dates, and any enumerated lists verbatim. Do not editorialise or add interpretation. Maximum 4 sentences. Output the summary directly with no preamble.`

const summaryUserTemplate = `Summarise the following passages from file %q.
Tree level: %d.

%s`

// BuildSummaryMessages returns the chat messages for one
// cluster-level summary. Exported so the prompt is exercisable from
// tests without going through the LLM client.
func BuildSummaryMessages(fileName string, level int, concatText string) []ai.ChatMessage {
	return []ai.ChatMessage{
		{Role: "system", Content: summarySystemPrompt},
		{Role: "user", Content: fmt.Sprintf(summaryUserTemplate, fileName, level, concatText)},
	}
}
