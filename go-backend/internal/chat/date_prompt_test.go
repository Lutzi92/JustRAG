package chat

import (
	"context"
	"testing"
)

func TestSystemPromptDateLine(t *testing.T) {
	ctx := context.Background()

	// Default reader (nil) → awareness on → non-empty line.
	if got := SystemPromptDateLine(ctx, nil, "en"); got == "" {
		t.Error("expected a date line when awareness is on (default)")
	}

	// Disabled → empty.
	off := &fakeSiteConfigReader{values: map[string]*string{"chat_date_awareness_enabled": strPtr("false")}}
	if got := SystemPromptDateLine(ctx, off, "en"); got != "" {
		t.Errorf("awareness off must yield empty, got %q", got)
	}

	// Bad timezone → still returns a line (falls back to UTC, no panic).
	bad := &fakeSiteConfigReader{values: map[string]*string{"chat_date_timezone": strPtr("Not/AZone")}}
	if got := SystemPromptDateLine(ctx, bad, "en"); got == "" {
		t.Error("bad timezone must fall back to UTC and still return a line")
	}
}
