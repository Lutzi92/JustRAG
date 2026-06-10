package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestJudgeContextSufficiency_Insufficient(t *testing.T) {
	var captured map[string]any
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeChatJSON(w, chatResponse(`{"sufficient":false}`, ""))
	})
	resolver := resolverForServer(srv, "fast-model")

	if got := JudgeContextSufficiency(context.Background(), resolver, "", "q", "ctx", "en", ""); got {
		t.Errorf("sufficient = true, want false")
	}
	rf, ok := captured["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_schema" {
		t.Errorf("request must carry json_schema response_format, got %v", captured["response_format"])
	}
}

func TestJudgeContextSufficiency_Sufficient(t *testing.T) {
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatJSON(w, chatResponse(`{"sufficient":true}`, ""))
	})
	resolver := resolverForServer(srv, "fast-model")
	if got := JudgeContextSufficiency(context.Background(), resolver, "", "q", "ctx", "en", ""); !got {
		t.Errorf("sufficient = false, want true")
	}
}

func TestJudgeContextSufficiency_FailsOpenOnHookError(t *testing.T) {
	// Gate must never block answers on a flaky judge: LLM error → sufficient.
	resolver := newTestResolverWithCompletion(t, func(ctx context.Context, r *ConfigResolver, prompt, systemPrompt, kbID, modelOverride string) (*CompletionResult, error) {
		return nil, context.DeadlineExceeded
	})
	if got := JudgeContextSufficiency(context.Background(), resolver, "", "q", "ctx", "en", ""); !got {
		t.Errorf("LLM error must fail open to sufficient=true")
	}
}

func TestJudgeContextSufficiency_FailsOpenOnGarbage(t *testing.T) {
	resolver := newTestResolverWithCompletion(t, func(ctx context.Context, r *ConfigResolver, prompt, systemPrompt, kbID, modelOverride string) (*CompletionResult, error) {
		return &CompletionResult{Content: "I think it depends"}, nil
	})
	if got := JudgeContextSufficiency(context.Background(), resolver, "", "q", "ctx", "en", ""); !got {
		t.Errorf("unparsable verdict must fail open to sufficient=true")
	}
}
