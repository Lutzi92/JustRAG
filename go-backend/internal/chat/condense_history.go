package chat

import (
	"context"

	"github.com/justrag/go-backend/internal/ai"
)

// ---------------------------------------------------------------------------
// CondenseFromHistory
// ---------------------------------------------------------------------------

// CondenseFromHistory is CondenseFollowUp minus the store lookup: history is
// the conversation so far (oldest first), supplied directly by the caller
// instead of being loaded from a chat's message rows. This is what lets the
// eval multi-turn replay (Wave 2 Task 3) drive condensation from a fixture's
// history with no DB-backed chat, no throwaway user, and no cleanup — see
// ruling W2-R1.
//
// Same rules as CondenseFollowUp's post-load behaviour: fewer than 2 history
// entries, or a nil aiResolver, return the message unchanged without calling
// the LLM; otherwise the last 6 entries are kept, each entry's content is
// capped at 500 bytes, and an LLM error also returns the message unchanged
// (fail-open).
func CondenseFromHistory(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	history []ai.ChatHistoryEntry,
	message, kbID, language string,
) (string, error) {
	if aiResolver == nil || len(history) < 2 {
		return message, nil
	}

	condensed, err := ai.CondenseQuestion(ctx, aiResolver, historyWindow(history), message, kbID, language)
	if err != nil {
		// Fail open.
		return message, nil
	}
	return condensed, nil
}

// historyWindow returns the last 6 entries of history (oldest of those kept
// first), with each entry's Content capped at 500 bytes. history shorter
// than 6 entries is returned unchanged in length, still content-capped.
func historyWindow(history []ai.ChatHistoryEntry) []ai.ChatHistoryEntry {
	if len(history) > 6 {
		history = history[len(history)-6:]
	}

	windowed := make([]ai.ChatHistoryEntry, len(history))
	for i, m := range history {
		content := m.Content
		if len(content) > 500 {
			content = content[:500]
		}
		windowed[i] = ai.ChatHistoryEntry{
			Role:    m.Role,
			Content: content,
		}
	}
	return windowed
}
