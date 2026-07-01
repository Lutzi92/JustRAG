package chat

import (
	"context"
	"time"

	"github.com/justrag/go-backend/internal/prompts"
)

// SystemPromptDateLine returns the localized current-date line to append
// to the answer system prompt, or "" when chat_date_awareness_enabled is
// off. "Today" is resolved in the configured timezone
// (chat_date_timezone); an unparseable timezone falls back to UTC.
//
// Computed once at dispatch (http_send.go) and threaded onto each
// orchestrator's params struct as CurrentDateLine, because five of the
// six answer orchestrators have no SiteConfigReader in scope.
func SystemPromptDateLine(ctx context.Context, reader SiteConfigReader, lang string) string {
	if !ChatDateAwarenessEnabled(ctx, reader) {
		return ""
	}
	loc, err := time.LoadLocation(ChatDateTimezone(ctx, reader))
	if err != nil {
		loc = time.UTC
	}
	return prompts.CurrentDateLine(lang, time.Now().In(loc))
}
