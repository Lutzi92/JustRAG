package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
)

// ---------------------------------------------------------------------------
// IsTransformFollowUpQuery tests
// ---------------------------------------------------------------------------

func TestIsTransformFollowUpQuery(t *testing.T) {
	t.Parallel()
	positives := []string{
		"kannst du das als Tabelle erstellen?",
		"kannst du mir das als Tabelle darstellen",
		"can you create this as table for me?",
		"can you show this as a table?",
		"fasse das bitte zusammen",
		"fasse deine Antwort kürzer zusammen",
		"make it shorter",
		"kannst du das kürzer machen?",
		"übersetze das ins Englische",
		"translate this to German please",
		"das bitte als Stichpunkte",
		"mach daraus bitte eine Tabelle",
		"als Tabelle bitte",
		"kürzer bitte",
		"zusammenfassen bitte",
		"summarize that",
		// Export-format follow-ups: reformat the previous answer as a
		// spreadsheet/table the UI can export to .xlsx (retrieval-free).
		"gib mir das als Excel-Datei",
		"das bitte als Excel",
		"exportiere das als Excel",
		"kannst du das als Excel-Tabelle ausgeben?",
		"as an excel file",
		"can you give me that as an xlsx?",
		"das als csv bitte",
	}
	for _, q := range positives {
		if !IsTransformFollowUpQuery(q) {
			t.Errorf("expected transform follow-up: %q", q)
		}
	}

	negatives := []string{
		"",
		"welche KI bezogenen Veranstaltungen gibt es?",
		"erstelle eine Tabelle aller Veranstaltungen im Mai",
		"vergleiche alle Dokumente in einer Tabelle",
		"create a table of all employees and their salaries",
		"wie viele Veranstaltungen sind das?",
		"what is the difference between A and B?",
		"welche davon finden online statt?",
		"wann findet die nächste Veranstaltung statt?",
		"erzähl mir mehr über die zweite Veranstaltung",
	}
	for _, q := range negatives {
		if IsTransformFollowUpQuery(q) {
			t.Errorf("expected NOT a transform follow-up: %q", q)
		}
	}
}

// ---------------------------------------------------------------------------
// lastAIMessage tests
// ---------------------------------------------------------------------------

func TestLastAIMessage_PicksMostRecentAIRow(t *testing.T) {
	t.Parallel()
	rows := []MessageRow{
		{ID: "1", Role: "user", Content: "q1"},
		{ID: "2", Role: "ai", Content: "a1"},
		{ID: "3", Role: "user", Content: "q2"},
		{ID: "4", Role: "ai", Content: "a2"},
	}
	got := lastAIMessage(rows)
	if got == nil || got.ID != "4" {
		t.Fatalf("got %+v, want row 4", got)
	}
}

func TestLastAIMessage_NilWhenNoAIRow(t *testing.T) {
	t.Parallel()
	if got := lastAIMessage([]MessageRow{{Role: "user", Content: "q"}}); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
	if got := lastAIMessage(nil); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// BuildTransformChatContext tests
// ---------------------------------------------------------------------------

func TestBuildTransformChatContext_CarriesAnswerAndSources(t *testing.T) {
	t.Parallel()
	prev := MessageRow{
		Role:    "ai",
		Content: "Es gibt drei KI-Veranstaltungen: A, B und C.",
		Sources: json.RawMessage(`[{"index":1,"fileId":"f1","fileName":"events.pdf"}]`),
	}
	got := BuildTransformChatContext(prev, "KB PROMPT", "de")
	if !strings.Contains(got.SystemPrompt, prev.Content) {
		t.Error("system prompt must embed the previous answer")
	}
	if !strings.Contains(got.SystemPrompt, "KB PROMPT") {
		t.Error("system prompt must keep the KB system prompt")
	}
	if len(got.Sources) != 1 || got.Sources[0].FileID != "f1" {
		t.Errorf("sources not carried over: %+v", got.Sources)
	}
	if got.Context != prev.Content {
		t.Errorf("Context must be the previous answer, got %q", got.Context)
	}
}

func TestBuildTransformChatContext_CapsVeryLongAnswer(t *testing.T) {
	t.Parallel()
	prev := MessageRow{Role: "ai", Content: strings.Repeat("ä", 30000)}
	got := BuildTransformChatContext(prev, "", "en")
	if n := len([]rune(got.Context)); n > 24000 {
		t.Errorf("previous answer not capped: %d runes", n)
	}
}

// ---------------------------------------------------------------------------
// answerHistoryFromMessages tests
// ---------------------------------------------------------------------------

func TestAnswerHistoryFromMessages_LastNAndRoles(t *testing.T) {
	t.Parallel()
	rows := []MessageRow{
		{Role: "user", Content: "old question"},
		{Role: "ai", Content: "old answer"},
		{Role: "user", Content: "q1"},
		{Role: "ai", Content: "a1"},
	}
	got := answerHistoryFromMessages(rows, 2, 100)
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2 (last N only): %+v", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content != "q1" {
		t.Errorf("got[0]=%+v", got[0])
	}
	if got[1].Role != "ai" || got[1].Content != "a1" {
		t.Errorf("got[1]=%+v", got[1])
	}
}

func TestAnswerHistoryFromMessages_TruncatesRuneSafe(t *testing.T) {
	t.Parallel()
	rows := []MessageRow{{Role: "ai", Content: strings.Repeat("ü", 50)}}
	got := answerHistoryFromMessages(rows, 6, 10)
	if len(got) != 1 {
		t.Fatalf("len=%d, want 1", len(got))
	}
	if !strings.HasSuffix(got[0].Content, "ü") || len([]rune(got[0].Content)) != 10 {
		t.Errorf("truncation not rune-safe: %q", got[0].Content)
	}
}

func TestAnswerHistoryFromMessages_EmptyAndDisabled(t *testing.T) {
	t.Parallel()
	if got := answerHistoryFromMessages(nil, 6, 100); got != nil {
		t.Errorf("nil rows: got %+v", got)
	}
	if got := answerHistoryFromMessages([]MessageRow{{Role: "user", Content: "q"}}, 0, 100); got != nil {
		t.Errorf("maxMessages=0: got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Site-config reader tests (A + B gates)
// ---------------------------------------------------------------------------

func TestChatAnswerHistoryEnabled_DefaultOn(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{}}
	if !ChatAnswerHistoryEnabled(context.Background(), reader) {
		t.Error("expected default true")
	}
}

func TestChatAnswerHistoryEnabled_ExplicitFalse(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{
		"chat_answer_history_enabled": strPtr("false"),
	}}
	if ChatAnswerHistoryEnabled(context.Background(), reader) {
		t.Error("expected false")
	}
}

func TestChatAnswerHistoryMessages_DefaultAndClamp(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{}}
	if got := ChatAnswerHistoryMessages(context.Background(), reader); got != 6 {
		t.Errorf("default=%d, want 6", got)
	}
	// House readInt semantics: out-of-range falls back to the default.
	reader = &fakeSiteConfigReader{values: map[string]*string{
		"chat_answer_history_messages": strPtr("999"),
	}}
	if got := ChatAnswerHistoryMessages(context.Background(), reader); got != 6 {
		t.Errorf("out-of-range=%d, want default 6", got)
	}
}

func TestChatAnswerHistoryMaxChars_Default(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{}}
	if got := ChatAnswerHistoryMaxChars(context.Background(), reader); got != 4000 {
		t.Errorf("default=%d, want 4000", got)
	}
}

func TestChatTransformFollowupEnabled_DefaultOn(t *testing.T) {
	t.Parallel()
	reader := &fakeSiteConfigReader{values: map[string]*string{}}
	if !ChatTransformFollowupEnabled(context.Background(), reader) {
		t.Error("expected default true")
	}
}

// historyRowsExcluding drops the row with the given ID — used by the
// transform route so the previous answer (already embedded in full in the
// system prompt) is not duplicated into the capped history.
func TestHistoryRowsExcluding(t *testing.T) {
	t.Parallel()
	rows := []MessageRow{{ID: "1", Role: "user"}, {ID: "2", Role: "ai"}, {ID: "3", Role: "user"}}
	got := historyRowsExcluding(rows, "2")
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "3" {
		t.Fatalf("got %+v", got)
	}
}

// Compile-time assertion: the answer-tools params accept history.
var _ = AnswerToolsParams{History: []ai.ChatHistoryEntry{}}
