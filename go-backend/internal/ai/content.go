package ai

import "encoding/json"

// ChatContentPart is one element of an OpenAI-compatible multimodal
// `content` array. Type is "text" or "image_url".
type ChatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ChatImageURL `json:"image_url,omitempty"`
}

// ChatImageURL holds an image reference; URL is typically a data URI
// ("data:image/png;base64,...") or an http(s) URL the provider can fetch.
type ChatImageURL struct {
	URL string `json:"url"`
}

// TextPart builds a text content part.
func TextPart(text string) ChatContentPart {
	return ChatContentPart{Type: "text", Text: text}
}

// ImageURLPart builds an image content part from a data URI or URL.
func ImageURLPart(url string) ChatContentPart {
	return ChatContentPart{Type: "image_url", ImageURL: &ChatImageURL{URL: url}}
}

// MarshalJSON emits the standard OpenAI shape: a string `content` for
// text-only messages, or an array `content` when MultiContent is set.
// The alias type avoids infinite recursion.
func (m ChatMessage) MarshalJSON() ([]byte, error) {
	type alias struct {
		Role       string     `json:"role"`
		Content    any        `json:"content,omitempty"`
		Name       string     `json:"name,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	}
	a := alias{Role: m.Role, Name: m.Name, ToolCallID: m.ToolCallID, ToolCalls: m.ToolCalls}
	if len(m.MultiContent) > 0 {
		a.Content = m.MultiContent
	} else if m.Content != "" {
		a.Content = m.Content
	}
	return json.Marshal(a)
}
