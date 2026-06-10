package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

var testSpec = &StructuredSpec{
	Name: "test_shape",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {"ok": {"type": "boolean"}},
		"required": ["ok"],
		"additionalProperties": false
	}`),
}

func TestGenerateCompletionStructured_SendsStrictJSONSchema(t *testing.T) {
	var captured map[string]any
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeChatJSON(w, chatResponse(`{"ok":true}`, ""))
	})
	resolver := resolverForServer(srv, "fast-model")

	res, err := GenerateCompletionStructured(context.Background(), resolver, "user prompt", "system prompt", "", "", testSpec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Content != `{"ok":true}` {
		t.Errorf("Content = %q", res.Content)
	}

	rf, ok := captured["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("request carried no response_format: %v", captured)
	}
	if rf["type"] != "json_schema" {
		t.Errorf("response_format.type = %v, want json_schema", rf["type"])
	}
	js, ok := rf["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("response_format.json_schema missing: %v", rf)
	}
	if js["name"] != "test_shape" {
		t.Errorf("json_schema.name = %v", js["name"])
	}
	if js["strict"] != true {
		t.Errorf("json_schema.strict = %v, want true", js["strict"])
	}
	if temp, ok := captured["temperature"].(float64); !ok || temp != 0.0 {
		t.Errorf("temperature = %v, want 0", captured["temperature"])
	}
}

func TestGenerateCompletionStructured_FallsBackToJSONObject(t *testing.T) {
	var calls atomic.Int32
	var secondFormat map[string]any
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if n == 1 {
			// Backend without strict json_schema support rejects the call.
			http.Error(w, `{"error":"response_format json_schema not supported"}`, http.StatusBadRequest)
			return
		}
		secondFormat, _ = body["response_format"].(map[string]any)
		writeChatJSON(w, chatResponse(`{"ok":true}`, ""))
	})
	resolver := resolverForServer(srv, "fast-model")

	res, err := GenerateCompletionStructured(context.Background(), resolver, "u", "s", "", "", testSpec)
	if err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if res.Content != `{"ok":true}` {
		t.Errorf("Content = %q", res.Content)
	}
	if calls.Load() < 2 {
		t.Fatalf("expected a retry after json_schema rejection, got %d calls", calls.Load())
	}
	if secondFormat == nil || secondFormat["type"] != "json_object" {
		t.Errorf("retry response_format = %v, want type json_object", secondFormat)
	}
}

func TestGenerateCompletionStructured_NilSpecSendsNoResponseFormat(t *testing.T) {
	var captured map[string]any
	srv := buildChatServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeChatJSON(w, chatResponse("plain", ""))
	})
	resolver := resolverForServer(srv, "fast-model")

	if _, err := GenerateCompletionStructured(context.Background(), resolver, "u", "s", "", "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, has := captured["response_format"]; has {
		t.Errorf("nil spec must not set response_format, got %v", captured["response_format"])
	}
}

func TestStructuredCompletionFn_HookStillIntercepts(t *testing.T) {
	called := false
	resolver := newTestResolverWithCompletion(t, func(ctx context.Context, r *ConfigResolver, prompt, systemPrompt, kbID, modelOverride string) (*CompletionResult, error) {
		called = true
		return &CompletionResult{Content: `{"ok":true}`}, nil
	})
	res, err := resolver.structuredCompletionFn(context.Background(), "u", "s", "", "", testSpec)
	if err != nil || !called {
		t.Fatalf("hook not used: err=%v called=%v", err, called)
	}
	if res.Content != `{"ok":true}` {
		t.Errorf("Content = %q", res.Content)
	}
}
