package ai

import (
	"context"
	"net/http"
	"testing"
)

func TestGenerateStepBackQuery_ReturnsTrimmedSingleLine(t *testing.T) {
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse("  What are the requirements in Project A and Project B?\n", ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	got, err := GenerateStepBackQuery(context.Background(), resolver, "How do A and B differ?", "", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "What are the requirements in Project A and Project B?"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestGenerateStepBackQuery_TrimsToFirstLine(t *testing.T) {
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse("Broader question?\nIgnored second line\n", ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	got, err := GenerateStepBackQuery(context.Background(), resolver, "Original?", "", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Broader question?" {
		t.Errorf("want first-line trim, got %q", got)
	}
}

func TestGenerateStepBackQuery_EmptyOutputReturnsEmpty(t *testing.T) {
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse("   \n", ""))
	})

	resolver := resolverForServer(srv, "gpt-4o")
	got, err := GenerateStepBackQuery(context.Background(), resolver, "q", "", "en")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("want empty string for empty LLM output, got %q", got)
	}
}
