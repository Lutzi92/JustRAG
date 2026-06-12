package ai

import "testing"

// ---------------------------------------------------------------------------
// BuildAnswerMessages tests
// ---------------------------------------------------------------------------

func TestBuildAnswerMessages_EmptyHistoryIsSystemThenUser(t *testing.T) {
	t.Parallel()
	got := BuildAnswerMessages("sys", "gemma-4-26b", nil, "question")
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2: %+v", len(got), got)
	}
	if got[0].Role != "system" || got[0].Content != "sys" {
		t.Errorf("messages[0]=%+v, want system/sys", got[0])
	}
	if got[1].Role != "user" || got[1].Content != "question" {
		t.Errorf("messages[1]=%+v, want user/question", got[1])
	}
}

func TestBuildAnswerMessages_HistoryBetweenSystemAndUser(t *testing.T) {
	t.Parallel()
	history := []ChatHistoryEntry{
		{Role: "user", Content: "welche KI Veranstaltungen gibt es?"},
		{Role: "ai", Content: "Es gibt drei: A, B und C."},
	}
	got := BuildAnswerMessages("sys", "gemma-4-26b", history, "als Tabelle bitte")
	if len(got) != 4 {
		t.Fatalf("len=%d, want 4: %+v", len(got), got)
	}
	if got[0].Role != "system" {
		t.Errorf("messages[0].Role=%q, want system", got[0].Role)
	}
	if got[1].Role != "user" || got[1].Content != history[0].Content {
		t.Errorf("messages[1]=%+v, want prior user turn", got[1])
	}
	if got[2].Role != "assistant" || got[2].Content != history[1].Content {
		t.Errorf("messages[2]=%+v, want role assistant (mapped from ai)", got[2])
	}
	if got[3].Role != "user" || got[3].Content != "als Tabelle bitte" {
		t.Errorf("messages[3]=%+v, want current user turn last", got[3])
	}
}

func TestBuildAnswerMessages_AssistantRolePassesThrough(t *testing.T) {
	t.Parallel()
	got := BuildAnswerMessages("", "m", []ChatHistoryEntry{{Role: "assistant", Content: "x"}}, "q")
	if len(got) != 2 || got[0].Role != "assistant" {
		t.Fatalf("got %+v, want assistant entry then user", got)
	}
}

func TestBuildAnswerMessages_O1FamilyDropsSystem(t *testing.T) {
	t.Parallel()
	got := BuildAnswerMessages("sys", "o1-mini", nil, "q")
	if len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("got %+v, want only the user message for o1 models", got)
	}
}

func TestBuildAnswerMessages_SkipsBlankHistoryEntries(t *testing.T) {
	t.Parallel()
	history := []ChatHistoryEntry{
		{Role: "user", Content: "   "},
		{Role: "ai", Content: "answer"},
	}
	got := BuildAnswerMessages("", "m", history, "q")
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2 (blank entry skipped): %+v", len(got), got)
	}
	if got[0].Role != "assistant" || got[0].Content != "answer" {
		t.Errorf("messages[0]=%+v, want the non-blank assistant entry", got[0])
	}
}
