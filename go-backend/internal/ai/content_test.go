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
