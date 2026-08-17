package openaicompat

import "github.com/justrag/go-backend/internal/chat"

// This file owns the shape of every citation-bearing response the
// OpenAI-compatible surface emits. Keeping assembly in pure functions makes
// the wire contract testable without an AI provider or an HTTP round trip.

// newMessageContext wraps the retrieved sources for the wire, returning nil
// when there is nothing to report so the `context` key stays absent.
func newMessageContext(sources []chat.ChatSource) *messageContext {
	cits := buildCitations(sources)
	if len(cits) == 0 {
		return nil
	}
	return &messageContext{Citations: cits}
}

// buildCompletionResponse assembles the non-streaming chat.completion body,
// attaching offset-anchored annotations and the retrieved chunk bodies.
func buildCompletionResponse(
	completionID, model string,
	created int64,
	content string,
	sources []chat.ChatSource,
) completionResponse {
	return completionResponse{
		ID:      completionID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []completionChoice{
			{
				Index: 0,
				Message: responseMessage{
					Role:        "assistant",
					Content:     content,
					Annotations: buildAnnotations(content, sources),
					Context:     newMessageContext(sources),
				},
				FinishReason: "stop",
			},
		},
		Usage: completionUsage{},
	}
}

// initialChunk is the opening streaming frame. Retrieval has already run by
// this point, so the sources ride along with the role delta and the client can
// render source cards before the first token arrives.
func initialChunk(completionID, model string, created int64, sources []chat.ChatSource) completionChunk {
	return completionChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []chunkChoice{
			{
				Index: 0,
				Delta: chunkDelta{
					Role:    "assistant",
					Context: newMessageContext(sources),
				},
				FinishReason: nil,
			},
		},
	}
}

// finalChunk is the closing streaming frame. Annotations are offsets into the
// finished answer, so they cannot be emitted until the stream has completed.
func finalChunk(completionID, model string, created int64, answer string, sources []chat.ChatSource) completionChunk {
	stopReason := "stop"
	return completionChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []chunkChoice{
			{
				Index:        0,
				Delta:        chunkDelta{Annotations: buildAnnotations(answer, sources)},
				FinishReason: &stopReason,
			},
		},
	}
}
