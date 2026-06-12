package ai

import "strings"

// BuildAnswerMessages assembles the messages array for an answer-generation
// call: optional system prompt, then the recent conversation history, then
// the current user message. History roles are normalized to the OpenAI wire
// roles — the chat store persists assistant turns as role "ai", which
// providers reject. Blank history entries are dropped. The o1 family does
// not accept the system role (see modelRejectsSystemRole), matching the
// behaviour of the history-less builders.
func BuildAnswerMessages(systemPrompt, model string, history []ChatHistoryEntry, prompt string) []ChatMessage {
	messages := make([]ChatMessage, 0, len(history)+2)
	if systemPrompt != "" && !modelRejectsSystemRole(model) {
		messages = append(messages, ChatMessage{Role: "system", Content: systemPrompt})
	}
	for _, h := range history {
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		role := "user"
		if h.Role == "ai" || h.Role == "assistant" {
			role = "assistant"
		}
		messages = append(messages, ChatMessage{Role: role, Content: h.Content})
	}
	return append(messages, ChatMessage{Role: "user", Content: prompt})
}
