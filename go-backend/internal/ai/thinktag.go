package ai

import "strings"

// ThinkTagFilter is a stateful streaming filter that separates
// <think>...</think> reasoning blocks from regular content. It buffers
// partial tags across calls so a tag split between two stream chunks is
// detected correctly.
//
// Goroutine safety: not safe for concurrent use. Each stream should own its
// own instance and drive it from a single goroutine.
//
// Why this lives here: the legacy `StreamCompletion` wrapper has owned this
// state machine since the early Qwen think-tag work, and the answer-tools
// path (chat/answer_tools.go) bypasses that wrapper to access ToolCallDeltas
// directly. Without this extracted filter, models that emit reasoning inline
// via <think> tags leak those tokens into the user-facing answer. Both call
// sites now share this implementation.
type ThinkTagFilter struct {
	insideTag bool
	buffer    string
}

// ThinkSegment is one piece of filter output. Exactly one of Reasoning or
// Content is non-empty per segment.
type ThinkSegment struct {
	Reasoning string
	Content   string
}

// Process consumes text and returns 0+ segments that the caller should emit
// immediately. A trailing partial tag (e.g. “"foo<thi"“) is buffered and
// will be re-examined on the next Process or Flush call.
func (f *ThinkTagFilter) Process(text string) []ThinkSegment {
	if text == "" && f.buffer == "" {
		return nil
	}
	text = f.buffer + text
	f.buffer = ""

	var out []ThinkSegment
	for len(text) > 0 {
		if f.insideTag {
			idx := strings.Index(text, "</think>")
			if idx >= 0 {
				if idx > 0 {
					out = append(out, ThinkSegment{Reasoning: text[:idx]})
				}
				f.insideTag = false
				text = text[idx+len("</think>"):]
				continue
			}
			partial := longestSuffix(text, "</think>")
			reasoning := text
			if partial > 0 {
				reasoning = text[:len(text)-partial]
				f.buffer = text[len(text)-partial:]
			}
			if reasoning != "" {
				out = append(out, ThinkSegment{Reasoning: reasoning})
			}
			return out
		}

		idx := strings.Index(text, "<think>")
		if idx >= 0 {
			if idx > 0 {
				out = append(out, ThinkSegment{Content: text[:idx]})
			}
			f.insideTag = true
			text = text[idx+len("<think>"):]
			continue
		}
		partial := longestSuffix(text, "<think>")
		content := text
		if partial > 0 {
			content = text[:len(text)-partial]
			f.buffer = text[len(text)-partial:]
		}
		if content != "" {
			out = append(out, ThinkSegment{Content: content})
		}
		return out
	}
	return out
}

// Flush returns any text still held in the partial-tag buffer when the
// upstream stream ends. If we were inside an unterminated <think> block the
// buffered text is reasoning; otherwise it is unmatched content (a "<thi"
// suffix that never resolved). Callers should emit the result if non-empty.
func (f *ThinkTagFilter) Flush() ThinkSegment {
	if f.buffer == "" {
		return ThinkSegment{}
	}
	out := f.buffer
	f.buffer = ""
	if f.insideTag {
		return ThinkSegment{Reasoning: out}
	}
	return ThinkSegment{Content: out}
}
