package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatMessage_StringContent_ByteStable(t *testing.T) {
	m := ChatMessage{Role: "user", Content: "hello"}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"role":"user","content":"hello"}` {
		t.Fatalf("unexpected JSON: %s", got)
	}
}

func TestChatMessage_MultiContent_EmitsArray(t *testing.T) {
	m := ChatMessage{
		Role: "user",
		MultiContent: []ChatContentPart{
			TextPart("describe this"),
			ImageURLPart("data:image/png;base64,AAAA"),
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `"type":"text"`) || !strings.Contains(got, `"type":"image_url"`) {
		t.Fatalf("expected text + image_url parts, got: %s", got)
	}
	if strings.Contains(got, `"content":"`) {
		t.Fatalf("MultiContent must not emit a string content field: %s", got)
	}
}

// TestChatMessage_ByteStable_RiskSurface locks in byte-identity for the
// message shapes the custom MarshalJSON could regress app-wide: assistant
// messages carrying tool_calls, tool-role results (Name + ToolCallID), and
// empty-content messages. Any field silently dropped from the marshaler's
// alias struct would change these exact bytes.
func TestChatMessage_ByteStable_RiskSurface(t *testing.T) {
	cases := []struct {
		name string
		msg  ChatMessage
		want string
	}{
		{
			name: "assistant_with_tool_calls_empty_content",
			msg: ChatMessage{
				Role:      "assistant",
				ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "f", Arguments: "{}"}}},
			},
			want: `{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]}`,
		},
		{
			name: "tool_role_result",
			msg:  ChatMessage{Role: "tool", Content: "result", Name: "f", ToolCallID: "call_1"},
			want: `{"role":"tool","content":"result","name":"f","tool_call_id":"call_1"}`,
		},
		{
			name: "empty_content_assistant",
			msg:  ChatMessage{Role: "assistant"},
			want: `{"role":"assistant"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.msg)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(b); got != tc.want {
				t.Fatalf("byte drift:\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}
