package chat

import (
	"context"
	"strings"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
)

// Answer-time conversation history.
//
// Until 2026-06 the answer LLM was single-turn on every path — only the
// CondenseFollowUp search-query rewrite ever saw prior messages, so
// follow-ups referencing the previous answer ("kannst du das als Tabelle
// erstellen?") had nothing to refer to and the model truthfully claimed it
// had no previous conversation. These helpers load the recent turns once per
// send and thread them into every answer completion (standard stream/JSON,
// orchestrators, answer-tools) via ai.BuildAnswerMessages.
//
// The per-message char cap keeps the token cost bounded (default 6 × 4000
// chars ≈ a few thousand tokens worst case); the transform-follow-up route
// embeds the previous answer UNCAPPED in its system prompt instead, because
// reformatting needs the full text.

// loadConversationRows returns the prior turns of this chat (ancestor chain
// when the client supplied a parent message ID — branched chats — otherwise
// the full chat, oldest first). Fail-open: a store error returns nil so the
// turn proceeds without history, mirroring CondenseFollowUp.
func (h *Handler) loadConversationRows(ctx context.Context, chatID string, parentMessageID *string) []MessageRow {
	var rows []MessageRow
	var err error
	if parentMessageID != nil && *parentMessageID != "" {
		rows, err = h.store.GetMessageAncestors(ctx, *parentMessageID, chatID)
	} else {
		rows, err = h.store.GetChatMessages(ctx, chatID)
	}
	if err != nil {
		logctx.From(ctx).Warn("chat.history: load conversation failed; proceeding without history",
			"chat_id", chatID, "error", err)
		return nil
	}
	return rows
}

// answerHistory converts the loaded rows into the capped history block for
// the answer LLM, honoring the chat_answer_history_* gates. Returns nil when
// the feature is off or there is nothing to carry.
func (h *Handler) answerHistory(ctx context.Context, rows []MessageRow) []ai.ChatHistoryEntry {
	if len(rows) == 0 || !ChatAnswerHistoryEnabled(ctx, h.siteConfigReader) {
		return nil
	}
	return answerHistoryFromMessages(rows,
		ChatAnswerHistoryMessages(ctx, h.siteConfigReader),
		ChatAnswerHistoryMaxChars(ctx, h.siteConfigReader))
}

// answerHistoryFromMessages maps the last maxMessages rows to history
// entries, truncating each to maxChars runes and dropping blank rows.
func answerHistoryFromMessages(rows []MessageRow, maxMessages, maxChars int) []ai.ChatHistoryEntry {
	if maxMessages <= 0 || len(rows) == 0 {
		return nil
	}
	if len(rows) > maxMessages {
		rows = rows[len(rows)-maxMessages:]
	}
	out := make([]ai.ChatHistoryEntry, 0, len(rows))
	for _, m := range rows {
		content := truncateRunes(m.Content, maxChars)
		if strings.TrimSpace(content) == "" {
			continue
		}
		out = append(out, ai.ChatHistoryEntry{Role: m.Role, Content: content})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// truncateRunes caps s at max runes (UTF-8 safe — the chat corpus is German,
// byte slicing would split umlauts). max <= 0 means no cap.
func truncateRunes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		// len(s) <= max in bytes implies <= max runes.
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
