package openaicompat

import (
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
)

func TestCapAnswerHistory_LastNAndRuneSafeTruncation(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("ü", 5000)
	in := []ai.ChatHistoryEntry{
		{Role: "user", Content: "drop me"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: long},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	got := capAnswerHistory(in)
	if len(got) != 6 {
		t.Fatalf("len=%d, want 6 (last N)", len(got))
	}
	if got[0].Content != "q1" {
		t.Errorf("got[0]=%q, want q1", got[0].Content)
	}
	if n := len([]rune(got[1].Content)); n != 4000 {
		t.Errorf("long entry not capped to 4000 runes: %d", n)
	}
	if got2 := capAnswerHistory(nil); got2 != nil {
		t.Errorf("nil in, got %+v", got2)
	}
}
