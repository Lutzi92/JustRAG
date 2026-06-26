package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/prompts"
)

func TestGenerateDriftFollowups_HappyPath(t *testing.T) {
	r := stubCompletion(t, `["What funding did project A receive?","Who leads project B?"]`)
	got, err := GenerateDriftFollowups(context.Background(), r, "kb", "themes across projects?", "Primer text", "en", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 2 || got[0] != "What funding did project A receive?" {
		t.Errorf("want 2 follow-ups, got %#v", got)
	}
}

func TestGenerateDriftFollowups_EmptyPrimerStillWorks(t *testing.T) {
	r := stubCompletion(t, `["sub q1","sub q2"]`)
	got, err := GenerateDriftFollowups(context.Background(), r, "kb", "broad?", "", "en", "")
	if err != nil || len(got) != 2 {
		t.Fatalf("empty primer: want 2 follow-ups no err, got %#v err=%v", got, err)
	}
}

func TestGenerateDriftFollowups_GarbageReturnsEmptyNoError(t *testing.T) {
	r := stubCompletion(t, "not json at all")
	got, err := GenerateDriftFollowups(context.Background(), r, "kb", "q", "p", "en", "")
	if err != nil {
		t.Fatalf("garbage should not error (caller falls back): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("garbage: want empty slice, got %#v", got)
	}
}

func TestGenerateDriftFollowups_TrimsAndDropsEmpties(t *testing.T) {
	r := stubCompletion(t, `["  q1  ","","\tq2\n"]`)
	got, _ := GenerateDriftFollowups(context.Background(), r, "kb", "q", "p", "en", "")
	if len(got) != 2 || got[0] != "q1" || got[1] != "q2" {
		t.Errorf("want [q1 q2], got %#v", got)
	}
}

func TestGenerateDriftFollowups_LLMErrorBubbles(t *testing.T) {
	r := stubCompletionError(t, errors.New("boom"))
	_, err := GenerateDriftFollowups(context.Background(), r, "kb", "q", "p", "en", "")
	if err == nil {
		t.Fatalf("want error to bubble (caller treats as fallback)")
	}
}

func TestDriftFollowupsPrompt_Langs(t *testing.T) {
	en := prompts.DriftFollowupsPrompt("en")
	de := prompts.DriftFollowupsPrompt("de")
	if en == "" || de == "" || en == de {
		t.Errorf("expected non-empty distinct prompts; en=%q de=%q", en, de)
	}
}
