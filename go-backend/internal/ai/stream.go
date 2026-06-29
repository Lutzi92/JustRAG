package ai

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"

	"github.com/justrag/go-backend/internal/observability"
)

// StreamEvent is a processed event emitted by StreamCompletion. Content holds
// cleaned text (think tags stripped when reasoning mode is active) and
// Reasoning holds any extracted reasoning text.
//
// StreamEvent does not carry ToolCallDeltas or FinishReason — those fields
// live on the underlying StreamChunk. Callers that need answer-time tool
// calling must use Client.StreamChatCompletion directly (see
// chat/answer_tools.go).
type StreamEvent struct {
	Content   string
	Reasoning string
	Done      bool

	// Err mirrors StreamChunk.Err: non-nil on the terminal Done event when
	// the underlying stream aborted mid-flight (connection reset, oversized
	// SSE frame). Content received before this event is truncated output —
	// callers must not persist it as a complete answer.
	Err error
}

// StreamCompletion resolves the AI config for kbID, builds a chat request, and
// returns a channel of StreamEvent values. Think-tag extraction and
// provider-specific reasoning_content fields are both handled. The final event
// always has Done:true. Set wantReasoning to enable reasoning extraction.
//
// This wrapper does NOT request or surface tool calls. For tool-calling chat
// turns, use Client.StreamChatCompletion directly with Tools/ToolChoice set
// and consume StreamChunk values (which carry ToolCallDeltas + FinishReason).
//
// reasoningEffort is the OpenAI o-series budget ("low"/"medium"/"high"); a
// non-empty value both requests reasoning from the provider AND enables
// extraction/emission of the returned reasoning. Empty disables both.
func StreamCompletion(ctx context.Context, resolver *ConfigResolver, prompt, systemPrompt, kbID, reasoningEffort string) (<-chan StreamEvent, error) {
	return StreamCompletionWithHistory(ctx, resolver, nil, prompt, systemPrompt, kbID, reasoningEffort)
}

// StreamCompletionWithHistory is StreamCompletion plus recent conversation
// turns inserted between the system prompt and the current user message, so
// follow-ups that reference the previous answer ("can you show this as a
// table?") have something to refer to. A nil/empty history is byte-identical
// to StreamCompletion.
func StreamCompletionWithHistory(ctx context.Context, resolver *ConfigResolver, history []ChatHistoryEntry, prompt, systemPrompt, kbID, reasoningEffort string) (<-chan StreamEvent, error) {
	config, err := resolver.Resolve(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("ai: resolve config: %w", err)
	}

	// A non-empty effort both requests reasoning from the provider
	// (reasoning_effort) and turns on think-tag / reasoning_content extraction
	// below.
	wantReasoning := reasoningEffort != ""

	ctx, span := observability.StartGenerationSpan(ctx, "rag.llm_completion", observability.GenerationAttrs{
		System: observability.ProviderSystemFromBaseURL(config.BaseURL),
		Model:  config.ChatModel,
	})
	endSpan := func() { span.End() }

	messages := BuildAnswerMessages(systemPrompt, config.ChatModel, history, prompt)

	temp := 0.2
	req := ChatRequest{
		Model:              config.ChatModel,
		Messages:           messages,
		Temperature:        &temp,
		ReasoningEffort:    reasoningEffort,
		ChatTemplateKwargs: ThinkingChatTemplateKwargs(reasoningEffort),
	}

	client := CachedClient(config.BaseURL, config.APIKey)
	rawCh, err := client.StreamChatCompletion(ctx, req)
	if err != nil {
		endSpan()
		return nil, err
	}

	// Small buffer (not unbuffered) so the producer can parse the next event
	// while the consumer is still flushing the previous one — eliminates a
	// synchronous goroutine handoff per token under high concurrency. The
	// ctx.Done() escape in `send` still guards against a disconnected consumer.
	out := make(chan StreamEvent, 2)

	// Bare go (not safego.Go) on purpose: the recover/close/endSpan LIFO defer
	// chain below must run entirely under this goroutine's control so the
	// panic-recovery can forward a terminal event while `out` is still open. A
	// safego.Go wrapper would add an outer recovery layer that fires after
	// close(out), unable to guarantee that ordering.
	go func() {
		// Defers are LIFO: recovery runs first (channel still open), then
		// close(out), then endSpan. Do NOT reorder without understanding this.
		defer endSpan()
		defer close(out)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("stream processing goroutine panic recovered",
					"error", r,
					"stack", string(debug.Stack()),
				)
				// Non-blocking: if the consumer has already bailed (context
				// cancellation, receiver exit) we drop the terminal event
				// rather than leaking this goroutine on a full send.
				select {
				case out <- StreamEvent{Done: true}:
				default:
					slog.Warn("stream consumer already departed; terminal event after panic recovery dropped")
				}
			}
		}()

		var filter ThinkTagFilter

		// send wraps every out <- StreamEvent with a ctx.Done() escape so a
		// disconnected downstream consumer cannot deadlock this goroutine on
		// an unbuffered send (which would also stop draining rawCh and leak
		// the producer goroutine in client.go). Returns false when ctx was
		// cancelled, in which case the for-range over rawCh must bail out.
		send := func(evt StreamEvent) bool {
			select {
			case out <- evt:
				return true
			case <-ctx.Done():
				return false
			}
		}

		for chunk := range rawCh {
			if chunk.Done {
				send(StreamEvent{Done: true, Err: chunk.Err})
				return
			}

			// Provider supplies reasoning_content directly — emit without parsing.
			if chunk.ReasoningContent != "" {
				if wantReasoning {
					if !send(StreamEvent{Reasoning: chunk.ReasoningContent}) {
						return
					}
				}
				// Still process Content below if present.
			}

			if chunk.Content == "" {
				continue
			}

			if !wantReasoning {
				// No reasoning extraction requested — pass content straight through.
				if !send(StreamEvent{Content: chunk.Content}) {
					return
				}
				continue
			}

			for _, seg := range filter.Process(chunk.Content) {
				if seg.Reasoning != "" {
					if !send(StreamEvent{Reasoning: seg.Reasoning}) {
						return
					}
				} else if seg.Content != "" {
					if !send(StreamEvent{Content: seg.Content}) {
						return
					}
				}
			}
		}

		// rawCh closed without a Done chunk — flush any buffered partial-tag
		// text and send Done.
		if wantReasoning {
			if tail := filter.Flush(); tail.Reasoning != "" {
				send(StreamEvent{Reasoning: tail.Reasoning})
			} else if tail.Content != "" {
				send(StreamEvent{Content: tail.Content})
			}
		}
		send(StreamEvent{Done: true})
	}()

	return out, nil
}

// longestSuffix returns the length of the longest suffix of s that is a prefix
// of needle. Used to detect partial tag boundaries across chunk boundaries.
func longestSuffix(s, needle string) int {
	for l := min(len(needle), len(s)); l > 0; l-- {
		if strings.HasSuffix(s, needle[:l]) {
			return l
		}
	}
	return 0
}
