// Package chat: answer-time tool calling. answer_tools.go owns the
// streaming-completion tool loop that lets the answer-generation LLM
// call MCP tools mid-turn via native OpenAI function calling. This
// initial file lands the small helpers (stringifyToolResult); subsequent
// tasks add assembleToolCalls, streamOneRound, and RunAnswerWithTools.
//
// Design: docs/superpowers/specs/2026-05-13-llm-answer-tool-calling-design.md
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/safego"
)

// answerToolChunksMaxBytes caps the per-tool-result Content for chunk-shaped
// tools (kb_search etc.). 4 KB is enough for ~5-8 typical chunks at the
// preview length below; the LLM can chunk_read for full text if it
// decides it needs more.
const answerToolChunksMaxBytes = 4096

// answerToolChunkPreviewBytes is the per-chunk content slice we serialize
// when rendering a list. Larger than the chunk render's source content
// is fine — the cap above clips the assembled output.
const answerToolChunkPreviewBytes = 300

// stringifyToolResult converts a DispatchedToolResult plus an optional dispatch
// error into the string the answer-tools loop puts on the role:"tool"
// message content. The provider sees a single string per tool call, so
// chunk-shaped tools (kb_search, keyword_search, chunk_read,
// document_outline, graph_search) get rendered as a numbered Markdown
// list and capped at answerToolChunksMaxBytes.
//
// Precedence:
//
//	dispatchErr != nil    → {"error": "<message>"}        (loop continues)
//	res.Text != ""        → res.Text                       (calculator, etc.)
//	res.Structured != nil → string(res.Structured)         (sql_query rows)
//	res.Chunks != nil     → numbered Markdown list, capped (retrieval tools)
//	else                  → "(empty result)"
//
// Returns a non-nil error only for marshaling failures the caller cannot
// recover from — dispatch errors are encoded *as content*, not returned.
func stringifyToolResult(res DispatchedToolResult, dispatchErr error) (string, error) {
	if dispatchErr != nil {
		payload, mErr := json.Marshal(map[string]string{"error": dispatchErr.Error()})
		if mErr != nil {
			return "", fmt.Errorf("answer_tools: marshal dispatch error: %w", mErr)
		}
		return string(payload), nil
	}
	if res.Text != "" {
		return res.Text, nil
	}
	if len(res.Structured) > 0 {
		return string(res.Structured), nil
	}
	if len(res.Chunks) > 0 {
		var b strings.Builder
		truncated := false
		for i, c := range res.Chunks {
			entry := fmt.Sprintf("%d. %s — %s\n", i+1, c.FileName, truncatePreview(c.Content, answerToolChunkPreviewBytes))
			if b.Len()+len(entry) > answerToolChunksMaxBytes {
				truncated = true
				break
			}
			b.WriteString(entry)
		}
		if truncated {
			b.WriteString(fmt.Sprintf("[truncated: %d total chunks; call chunk_read for full text]", len(res.Chunks)))
		}
		return b.String(), nil
	}
	return "(empty result)", nil
}

// truncatePreview shortens s to at most maxBytes bytes without splitting
// a UTF-8 codepoint. Chunk content can include non-ASCII characters
// (multilingual KBs), and a naive byte slice would produce invalid UTF-8
// at the boundary; we step back to the last rune start before slicing.
// Appends "…" when truncation occurred.
func truncatePreview(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// assembleToolCalls concatenates streamed ai.ToolCallDelta fragments
// (keyed by Index) into complete ai.ToolCall values. The first non-empty
// ID/Name/Type wins per index — subsequent deltas for the same index
// usually carry only argument continuations. Returns calls in ascending
// Index order so downstream dispatch is deterministic.
//
// Returns nil for nil/empty input so callers can pass an unaccumulated
// slice without a length check.
func assembleToolCalls(deltas []ai.ToolCallDelta) []ai.ToolCall {
	if len(deltas) == 0 {
		return nil
	}
	type acc struct {
		id, typeStr, name string
		args              strings.Builder
	}
	byIndex := map[int]*acc{}
	for _, d := range deltas {
		a, ok := byIndex[d.Index]
		if !ok {
			a = &acc{}
			byIndex[d.Index] = a
		}
		if a.id == "" {
			a.id = d.ID
		}
		if a.typeStr == "" {
			a.typeStr = d.Type
		}
		if a.name == "" {
			a.name = d.Name
		}
		a.args.WriteString(d.Arguments)
	}
	indices := make([]int, 0, len(byIndex))
	for i := range byIndex {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	out := make([]ai.ToolCall, 0, len(indices))
	for _, i := range indices {
		a := byIndex[i]
		typ := a.typeStr
		if typ == "" {
			typ = "function"
		}
		out = append(out, ai.ToolCall{
			ID:   a.id,
			Type: typ,
			Function: ai.ToolCallFunction{
				Name:      a.name,
				Arguments: a.args.String(),
			},
		})
	}
	return out
}

// roundStreamEvent is the lifted shape of one upstream chunk for the
// round handler. We can't reuse ai.StreamChunk directly because the
// round handler needs the FinishReason and ToolCallDeltas surfaced at
// the same level as Content — and we want a seam to inject fakes in
// tests.
type roundStreamEvent struct {
	Content        string
	Reasoning      string
	ToolCallDeltas []ai.ToolCallDelta
	FinishReason   string
	Done           bool
	// Err mirrors ai.StreamChunk.Err: non-nil on the terminal Done event
	// when the upstream stream aborted mid-flight. The round must fail
	// rather than treat the truncated content as a finished answer.
	Err error
}

// streamOneRound consumes one upstream round of streaming events,
// decides whether it's a content round or a tool round, and emits
// content live to `emit` while buffering tool-call deltas. Returns the
// assembled tool calls (empty for content rounds) and the round's
// finish_reason ("stop" | "tool_calls" | "length" | "").
//
// Streaming policy: the round is "content" if any chunk carries non-
// empty Content; otherwise "tool". Reasoning events are passed through
// to `emit` regardless — the frontend's reasoning panel renders them
// during both round types.
//
// Cancellation: a select on ctx.Done() runs BEFORE each upstream receive
// so a cancelled context returns immediately without emitting one more
// chunk. emit must not be nil; the function is package-internal and
// callers always wire a real handler.
func streamOneRound(ctx context.Context, upstream <-chan roundStreamEvent, emit func(ai.StreamEvent)) ([]ai.ToolCall, string, error) {
	var (
		deltas       []ai.ToolCallDelta
		finishReason string
		sawContent   bool
	)
loop:
	for {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case chunk, ok := <-upstream:
			if !ok {
				// Upstream closed without an explicit Done chunk.
				break loop
			}
			if chunk.FinishReason != "" {
				finishReason = chunk.FinishReason
			}
			if chunk.Reasoning != "" {
				emit(ai.StreamEvent{Reasoning: chunk.Reasoning})
			}
			if chunk.Content != "" {
				sawContent = true
				emit(ai.StreamEvent{Content: chunk.Content})
			}
			if len(chunk.ToolCallDeltas) > 0 {
				deltas = append(deltas, chunk.ToolCallDeltas...)
			}
			if chunk.Done {
				// Abnormal termination: the upstream stream died mid-round
				// (connection reset, oversized SSE frame). Failing the round
				// surfaces through RunAnswerWithTools' error return, so the
				// caller emits a stream error instead of persisting the
				// truncated content as a finished answer.
				if chunk.Err != nil {
					return nil, finishReason, fmt.Errorf("answer stream aborted: %w", chunk.Err)
				}
				break loop
			}
		}
	}
	if sawContent {
		// Content round: tool_calls would be vestigial. Drop them.
		return nil, finishReason, nil
	}
	return assembleToolCalls(deltas), finishReason, nil
}

// runAnswerInput is the assembled input to the loop. Threading it
// through a struct keeps the call signature flat and the test helper
// readable.
//
// ChatID is required when the answer-tool catalog exposes session-
// memory tools (memory_read, memory_write) — those tools require a
// chat_id injected by the orchestrator, just as retrieval tools
// require kb_id. Empty ChatID is allowed (e.g. tests that don't expose
// memory tools); the injector simply skips the chat_id field.
type runAnswerInput struct {
	KbID       string
	ChatID     string
	Messages   []ai.ChatMessage
	Tools      []ai.ChatTool
	Dispatcher ToolDispatcher
	MaxRounds  int
}

// roundRunner is the seam between the loop and the actual streaming
// completion call. Production wires it to a closure over the resolver +
// kb-id + ai.StreamChatCompletion; tests inject a fakeRoundRunner.
type roundRunner interface {
	Run(ctx context.Context, messages []ai.ChatMessage, tools []ai.ChatTool, toolChoice any) (<-chan roundStreamEvent, error)
}

// trajectoryEmit is the (optional) callback for agent_tool_call events.
// Nil means "no trajectory emission" — production passes the existing
// emit closure from http_send.go.
type trajectoryEmit func(stage, decision, reason string, details map[string]any)

// runAnswerWithToolsWithRunner is the testable variant of
// RunAnswerWithTools. The public entry point in a later task is a thin
// wrapper that builds a real roundRunner.
//
// Loop shape:
//
//	round = 1..MaxRounds:
//	  issue one tool-aware streaming round
//	  if finish_reason="stop"|"length" or the round produced no tool calls,
//	     the answer is already streamed to `emit`; return.
//	  else (finish_reason="tool_calls"):
//	     append assistant message with tool_calls
//	     for each call: dispatch via Dispatcher, emit trajectory events,
//	     append role:"tool" message with the stringified result.
//
// MaxRounds exhaustion (the forced-finish path) is a later task.
//
// AP-A3 turn-budget integration: at the top of every round (and before
// each individual tool dispatch within a round) the loop consults
// TurnBudgetFromContext(ctx).CheckExceeded(ctx, "answer_tools"). On
// the first exhausted dimension the loop short-circuits into the same
// forced no-tool finish path used for MaxRounds exhaustion — the user
// always sees a textual answer, never a partial / stalled response.
func runAnswerWithToolsWithRunner(
	ctx context.Context,
	in runAnswerInput,
	runner roundRunner,
	emit func(ai.StreamEvent),
	trace trajectoryEmit,
) error {
	if in.MaxRounds < 1 {
		in.MaxRounds = 1
	}
	if in.MaxRounds > 10 {
		in.MaxRounds = 10
	}
	budget := TurnBudgetFromContext(ctx)
	messages := append([]ai.ChatMessage(nil), in.Messages...)
	roundsConsumed := 0
	forcedReason := ""
	for round := 1; round <= in.MaxRounds; round++ {
		if kind := budget.CheckExceeded(ctx, "answer_tools"); kind != "" {
			forcedReason = "budget_exceeded:" + kind
			if trace != nil {
				trace("decision", "budget_exceeded", kind, map[string]any{
					"orchestrator": "answer_tools",
					"round":        round,
				})
			}
			break
		}
		upstream, err := runner.Run(ctx, messages, in.Tools, "auto")
		if err != nil {
			return fmt.Errorf("answer_tools: round %d: %w", round, err)
		}
		calls, finish, err := streamOneRound(ctx, upstream, emit)
		if err != nil {
			return fmt.Errorf("answer_tools: round %d stream: %w", round, err)
		}
		roundsConsumed = round
		if finish == "stop" || finish == "length" || len(calls) == 0 {
			observability.RecordAnswerToolLoopRounds(roundsConsumed)
			return nil
		}
		// finish == "tool_calls"; dispatch and loop.
		messages = append(messages, ai.ChatMessage{Role: "assistant", ToolCalls: calls})
		for _, c := range calls {
			if trace != nil {
				trace("decision", "agent_tool_call", c.Function.Name, map[string]any{
					"tool": c.Function.Name,
					"args": previewArgs(c.Function.Arguments),
				})
			}
			res, derr := in.Dispatcher.Dispatch(ctx, in.KbID, c.Function.Name,
				injectInjectedIDs(c.Function.Arguments, in.KbID, in.ChatID))
			content, sErr := stringifyToolResult(res, derr)
			if sErr != nil {
				return fmt.Errorf("answer_tools: stringify tool %s: %w", c.Function.Name, sErr)
			}
			messages = append(messages, ai.ChatMessage{
				Role:       "tool",
				ToolCallID: c.ID,
				Name:       c.Function.Name,
				Content:    content,
			})
			if trace != nil {
				trace("decision", "agent_tool_call_result", c.Function.Name, map[string]any{
					"tool":    c.Function.Name,
					"preview": previewContent(content),
				})
			}
		}
	}
	// Exhaustion path. Two reasons land here:
	//
	//   (a) MaxRounds rounds completed without a content finish, OR
	//   (b) TurnBudget exhausted at the top of some round.
	//
	// Either way: append a system nudge tailored to the reason and issue
	// one forced round with tool_choice="none" so the model must answer
	// textually. Tool_calls on this round would be vestigial and
	// discarded by streamOneRound; tool_choice="none" makes them
	// impossible anyway.
	nudge := "You have used the maximum number of tool calls. Provide your final answer now."
	if forcedReason != "" {
		nudge = "The turn budget has been exhausted (" + forcedReason + "). Provide your best final answer now using only the information you already have."
	}
	messages = append(messages, ai.ChatMessage{
		Role:    "system",
		Content: nudge,
	})
	upstream, err := runner.Run(ctx, messages, in.Tools, "none")
	if err != nil {
		return fmt.Errorf("answer_tools: forced final round: %w", err)
	}
	if _, _, err := streamOneRound(ctx, upstream, emit); err != nil {
		return fmt.Errorf("answer_tools: forced final stream: %w", err)
	}
	observability.RecordAnswerToolLoopExhausted()
	if roundsConsumed == 0 {
		// Budget exceeded before round 1 even ran.
		observability.RecordAnswerToolLoopRounds(0)
	} else {
		observability.RecordAnswerToolLoopRounds(roundsConsumed)
	}
	return nil
}

// previewArgs returns a short, log-safe rendering of tool-call JSON args.
// Uses truncatePreview so multibyte runes never split — JSON arg strings
// can carry multilingual content that would otherwise produce invalid
// UTF-8 in the SSE payload.
func previewArgs(raw string) string {
	return truncatePreview(raw, 200)
}

// injectInjectedIDs merges the orchestrator-supplied identifiers into
// the model's tool-call arguments JSON before dispatch. MCP tool
// schemas deliberately hide these fields from the LLM (the model has
// no business knowing its own KB / chat session), but the tools
// themselves require them:
//
//   - kb_id: kb_search, keyword_search, count_mentions, chunk_read,
//     document_outline, graph_search.
//   - chat_id: memory_read, memory_write.
//
// Both fields are injected unconditionally when non-empty so AnswerToolCatalog
// can keep exposing all tools without the dispatcher having to branch
// per-tool. The plan-execute path does the same via MCPDispatcher.RunTool
// (kb_id only — that path doesn't currently expose memory tools); the
// answer-tools loop has to do it inline because it calls Dispatch
// directly.
//
// On malformed upstream JSON the function falls back to a minimal
// {"kb_id": ..., "chat_id": ...} payload — the tool's own arg validator
// will surface a clear error if other required fields are missing.
func injectInjectedIDs(rawArgs, kbID, chatID string) json.RawMessage {
	if kbID == "" && chatID == "" {
		return json.RawMessage(rawArgs)
	}
	args := map[string]any{}
	if rawArgs != "" {
		_ = json.Unmarshal([]byte(rawArgs), &args)
	}
	if kbID != "" {
		args["kb_id"] = kbID
	}
	if chatID != "" {
		args["chat_id"] = chatID
	}
	out, err := json.Marshal(args)
	if err != nil {
		return json.RawMessage(rawArgs)
	}
	return out
}

// previewContent is the trajectory-emit-safe preview of a stringified
// tool result. Keep small — the SSE frame is in the user's network path.
// Uses truncatePreview so multibyte content (German, Chinese, emoji) in
// a tool result preview never produces invalid UTF-8.
func previewContent(s string) string {
	return truncatePreview(s, 200)
}

// AnswerToolsParams is the public input to RunAnswerWithTools. The HTTP
// handler builds this once per turn and hands it to the loop.
//
// ChatID is required when the tool catalog exposes session-memory
// tools (memory_read, memory_write); pass the current chat session's
// ID so the dispatcher can inject it. Empty ChatID is allowed for
// callers that don't expose memory tools.
type AnswerToolsParams struct {
	AIResolver   *ai.ConfigResolver
	KbID         string
	ChatID       string
	SystemPrompt string
	UserPrompt   string
	// History is the recent-conversation block inserted between the system
	// prompt and the user message (see ai.BuildAnswerMessages). Optional.
	History    []ai.ChatHistoryEntry
	Tools      []ai.ChatTool
	Dispatcher ToolDispatcher
	MaxRounds  int
	// ReasoningEffort ("low"/"medium"/"high", or "" for off) is the o-series
	// request-side signal that makes the model produce chain-of-thought. The
	// runner already extracts/emits any reasoning the provider returns; this
	// is what asks for it. Mirrors the standard streaming path.
	ReasoningEffort string
}

// RunAnswerWithTools is the production entry point for the answer-time
// tool-call loop. Builds the initial [system, user] message list, wires
// a streaming-completion-backed roundRunner, and delegates to
// runAnswerWithToolsWithRunner. emit is called for every content +
// reasoning event the model produces; trace is the optional trajectory
// callback for agent_tool_call events (pass nil to suppress).
func RunAnswerWithTools(
	ctx context.Context,
	p AnswerToolsParams,
	emit func(ai.StreamEvent),
	trace trajectoryEmit,
) error {
	config, err := p.AIResolver.Resolve(ctx, p.KbID)
	if err != nil {
		return fmt.Errorf("answer_tools: resolve config: %w", err)
	}
	systemPrompt := p.SystemPrompt
	if len(p.Tools) > 0 {
		hint := "TOOL USE RULES (strict):\n" +
			"1. For arithmetic, call `calculator` — never compute multi-step math in your head.\n" +
			"2. For 'how often / wie oft / how many times' questions about the knowledge base, call `count_mentions` ONCE. For person names (or any term with multiple natural forms), pass an ARRAY of candidate substrings via `queries`, e.g. `queries: [\"Karcher\", \"Steffen Karcher\", \"steffen.karcher\"]`. The tool returns each query with its count, sorted descending. The largest count is almost always the correct answer for 'how often is X mentioned'. State it directly. Do NOT call the tool multiple times; do NOT count retrieved chunks; do NOT 'adjust' based on retrieved context — the tool counts across the FULL KB while the retrieved context is a small sample.\n" +
			"3. For exact-text recall beyond the retrieved chunks, call `keyword_search`.\n" +
			"4. For database meta-questions ('when uploaded', 'how many messages'), call `sql_query`.\n" +
			"5. When a tool returns a number, treat that number as authoritative for its specific query."
		if systemPrompt == "" {
			systemPrompt = hint
		} else {
			systemPrompt = systemPrompt + "\n\n" + hint
		}
	}
	messages := ai.BuildAnswerMessages(systemPrompt, config.ChatModel, p.History, p.UserPrompt)
	runner := &liveRoundRunner{config: config, reasoningEffort: p.ReasoningEffort}
	return runAnswerWithToolsWithRunner(ctx, runAnswerInput{
		KbID:       p.KbID,
		ChatID:     p.ChatID,
		Messages:   messages,
		Tools:      p.Tools,
		Dispatcher: p.Dispatcher,
		MaxRounds:  p.MaxRounds,
	}, runner, emit, trace)
}

// liveRoundRunner is the production roundRunner — wraps an ai.Client
// keyed by the resolved kb config and lifts each ai.StreamChunk to the
// roundStreamEvent the loop expects.
type liveRoundRunner struct {
	config          *ai.ResolvedConfig
	reasoningEffort string
}

func (r *liveRoundRunner) Run(ctx context.Context, messages []ai.ChatMessage, tools []ai.ChatTool, toolChoice any) (<-chan roundStreamEvent, error) {
	temp := 0.2
	req := ai.ChatRequest{
		Model:              r.config.ChatModel,
		Messages:           messages,
		Temperature:        &temp,
		Tools:              tools,
		ToolChoice:         toolChoice,
		ReasoningEffort:    r.reasoningEffort,
		ChatTemplateKwargs: ai.ThinkingChatTemplateKwargs(r.reasoningEffort),
	}
	client := ai.CachedClient(r.config.BaseURL, r.config.APIKey)
	rawCh, err := client.StreamChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	// Small buffer decouples the streaming goroutine from the SSE writer so
	// neither blocks on the other for every token. 16 matches the typical
	// burst size of a fast model emitting back-to-back deltas.
	out := make(chan roundStreamEvent, 16)
	// safego.GoCtx wraps the goroutine in the shared panic-recovery handler
	// that logs through the request-scoped logger (request_id / user_id /
	// kb_id). close(out) runs as fn's last defer before the panic propagates
	// up to safego.GoCtx's recover handler — so consumers always see
	// end-of-stream regardless of panic, and the recover handler logs
	// afterward independently. The chunk loop below is a struct copy + channel
	// send, but the upstream rawCh is fed by an HTTP/SSE pipeline whose panics
	// surface here; we contain them rather than crashing the process.
	safego.GoCtx(ctx, func() {
		defer close(out)
		// Per-Run() think-tag filter — provider may emit reasoning either via
		// chunk.ReasoningContent (vLLM/LiteLLM) or inline as <think>...</think>
		// tokens in chunk.Content (other providers). The legacy ai.StreamCompletion
		// path strips the latter; before this filter ran here, those tokens
		// leaked straight through to the user as visible answer text.
		var filter ai.ThinkTagFilter
		send := func(evt roundStreamEvent) bool {
			select {
			case out <- evt:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for chunk := range rawCh {
			// Tool-call / control chunks (no Content) pass through unchanged so
			// they cannot be reordered against any pending content segments
			// produced by the filter.
			if chunk.Content == "" {
				evt := roundStreamEvent{
					Reasoning:      chunk.ReasoningContent,
					ToolCallDeltas: chunk.ToolCallDeltas,
					FinishReason:   chunk.FinishReason,
					Done:           chunk.Done,
					Err:            chunk.Err,
				}
				if !send(evt) {
					return
				}
				continue
			}
			// Surface the provider's reasoning field first (separate event so
			// the consumer's content/reasoning bookkeeping stays simple), then
			// the filter-split content segments.
			if chunk.ReasoningContent != "" {
				if !send(roundStreamEvent{Reasoning: chunk.ReasoningContent}) {
					return
				}
			}
			segments := filter.Process(chunk.Content)
			for i, seg := range segments {
				evt := roundStreamEvent{}
				if seg.Reasoning != "" {
					evt.Reasoning = seg.Reasoning
				} else if seg.Content != "" {
					evt.Content = seg.Content
				}
				// Attach terminal metadata to the last emitted segment so the
				// consumer sees FinishReason/Done at the right moment.
				if i == len(segments)-1 {
					evt.ToolCallDeltas = chunk.ToolCallDeltas
					evt.FinishReason = chunk.FinishReason
					evt.Done = chunk.Done
					evt.Err = chunk.Err
				}
				if !send(evt) {
					return
				}
			}
			// If the filter buffered everything as a partial tag, segments is
			// empty — still forward terminal metadata so the consumer doesn't
			// stall waiting for a Done that never comes.
			if len(segments) == 0 && (chunk.Done || chunk.FinishReason != "" || len(chunk.ToolCallDeltas) > 0) {
				if !send(roundStreamEvent{
					ToolCallDeltas: chunk.ToolCallDeltas,
					FinishReason:   chunk.FinishReason,
					Done:           chunk.Done,
					Err:            chunk.Err,
				}) {
					return
				}
			}
		}
		// Drain anything still in the filter buffer (unterminated <think> or a
		// partial opener that never resolved). Unterminated reasoning surfaces
		// as Reasoning; partial-opener tail surfaces as Content.
		if tail := filter.Flush(); tail.Reasoning != "" {
			send(roundStreamEvent{Reasoning: tail.Reasoning})
		} else if tail.Content != "" {
			send(roundStreamEvent{Content: tail.Content})
		}
	})
	return out, nil
}
