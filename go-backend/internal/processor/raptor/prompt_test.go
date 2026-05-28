package raptor

import (
	"strings"
	"testing"
)

func TestBuildSummaryMessages_HasSystemAndUserRoles(t *testing.T) {
	msgs := BuildSummaryMessages("report.pdf", 2, "passage one. passage two.")
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("first message role = %q, want system", msgs[0].Role)
	}
	if msgs[1].Role != "user" {
		t.Errorf("second message role = %q, want user", msgs[1].Role)
	}
}

func TestBuildSummaryMessages_UserContentIncludesFileNameAndLevel(t *testing.T) {
	msgs := BuildSummaryMessages("report.pdf", 2, "passage one.")
	u := msgs[1].Content
	if !strings.Contains(u, "report.pdf") {
		t.Errorf("user content should mention file name; got %q", u)
	}
	if !strings.Contains(u, "Tree level: 2") {
		t.Errorf("user content should mention tree level; got %q", u)
	}
	if !strings.Contains(u, "passage one") {
		t.Errorf("user content should embed passages; got %q", u)
	}
}

func TestBuildSummaryMessages_SystemPromptForbidsEditorialising(t *testing.T) {
	msgs := BuildSummaryMessages("a.txt", 1, "x")
	if !strings.Contains(strings.ToLower(msgs[0].Content), "editorial") {
		t.Errorf("system prompt should forbid editorialising; got %q", msgs[0].Content)
	}
}
