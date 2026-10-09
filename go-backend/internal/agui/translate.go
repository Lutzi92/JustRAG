// Package agui speaks the AG-UI protocol (https://docs.ag-ui.com) on top of
// ADK runs: it turns an AG-UI RunAgentInput into an ADK message and the ADK
// event stream into AG-UI events. The community Go SDK provides the event
// types and SSE encoding; it is wrapped here so it stays replaceable.
package agui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/google/uuid"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// ToolChunksEvent is the CUSTOM event carrying retrieved chunks of one tool
// call: {"toolCallId": "...", "chunks": [...]} — chunks are mcp.ResultChunk
// JSON. Documented in docs/api-contracts/agent-chat.md (Phase 2).
const ToolChunksEvent = "justrag.tool_chunks.v1"

// SourcesEvent is the CUSTOM event carrying a turn's final numbered sources
// (the agent-chat flow's []chat.ChatSource JSON). The Translator never
// emits it itself; a workflow node does, via CustomMetadata.
const SourcesEvent = "justrag.sources.v1"

// customKey is the ADK event CustomMetadata key a node or callback sets to
// emit generic AG-UI CUSTOM events; build its value with CustomMetadata.
const customKey = "agui.custom"

// CustomMetadata returns ADK event CustomMetadata that the Translator emits
// as CUSTOM(name, value). Several events on one ADK event: set the
// "agui.custom" key to a slice of {"name", "value"} maps.
func CustomMetadata(name string, value any) map[string]any {
	return map[string]any{customKey: map[string]any{"name": name, "value": value}}
}

// ErrResumeNeedsThread: a resume must name the thread it continues.
var ErrResumeNeedsThread = errors.New("agui: resume requires threadId")

var instanceSuffix = regexp.MustCompile(`@\d+`)

// StepName turns an ADK node path ("flow@1/retrieve@2") into the stable
// step name used in STEP_STARTED/FINISHED ("flow/retrieve") — the workflow
// canvas highlights nodes by this name.
func StepName(path string) string { return instanceSuffix.ReplaceAllString(path, "") }

// Interrupts returns the interrupts collected so far in this run.
func (t *Translator) Interrupts() []types.Interrupt { return slices.Clone(t.interrupts) }

// Emit sends one AG-UI event to the client.
type Emit func(events.Event) error

// Translator converts one ADK run's events into AG-UI events. Not safe for
// concurrent use; one per run.
type Translator struct {
	threadID, runID string
	emit            Emit

	textID      string // open TEXT_MESSAGE, "" if none
	reasoningID string // open REASONING message, "" if none
	streamed    bool   // partial text was streamed for the current message
	step        string // current workflow node
	interrupts  []types.Interrupt
	// lastOutput is the latest workflow node output; like ADK's console, only
	// the run's final output is shown, and only when the run did not pause.
	lastOutput string
	// expiresAt, when set, is stamped on every interrupt in RUN_FINISHED.
	expiresAt string

	// What the client received this run, for Output.
	outText      strings.Builder
	outReasoning strings.Builder
	custom       map[string]any
}

// Output returns what the client received so far: the assistant text
// (separate messages joined by a blank line), the reasoning, and the last
// value of each CUSTOM event by name.
func (t *Translator) Output() (text, reasoning string, custom map[string]any) {
	return t.outText.String(), t.outReasoning.String(), maps.Clone(t.custom)
}

// SetInterruptExpiry records when this run's interrupts stop being
// resumable; Finish reports it on each interrupt (RFC 3339, UTC).
func (t *Translator) SetInterruptExpiry(at time.Time) {
	t.expiresAt = at.UTC().Format(time.RFC3339)
}

// NewTranslator starts a run: it emits RUN_STARTED.
func NewTranslator(threadID, runID string, emit Emit) (*Translator, error) {
	t := &Translator{threadID: threadID, runID: runID, emit: emit}
	return t, emit(events.NewRunStartedEvent(threadID, runID))
}

// Event translates one ADK event.
func (t *Translator) Event(ev *session.Event) error {
	if ev == nil {
		return nil
	}
	if err := t.stepFor(ev); err != nil {
		return err
	}
	if err := t.customFrom(ev.CustomMetadata); err != nil {
		return err
	}
	if ev.RequestedInput != nil {
		t.interrupts = append(t.interrupts, inputInterrupt(ev.RequestedInput))
	}
	if ev.Output != nil {
		// Node output is data; an emitting node mirrors a string output into a
		// role-less Content, which must not reach the user as chat text.
		if s, ok := ev.Output.(string); ok && s != "" {
			t.lastOutput = s
		}
		return nil
	}
	if ev.Content == nil {
		return nil
	}
	for _, p := range ev.Content.Parts {
		if p == nil {
			continue
		}
		var err error
		switch {
		case p.Thought && p.Text != "":
			if ev.Partial {
				err = t.reasoning(p.Text)
			}
		case p.Text != "":
			// Model text supersedes any earlier node output: a node's text
			// output is shown only when it is the run's final word.
			t.lastOutput = ""
			err = t.text(p.Text, ev.Partial)
		case p.FunctionCall != nil && !ev.Partial:
			err = t.call(p.FunctionCall)
		case p.FunctionResponse != nil && !ev.Partial:
			err = t.result(p.FunctionResponse)
		}
		if err != nil {
			return err
		}
	}
	if !ev.Partial {
		// A complete model event closes whatever was streaming.
		return t.closeAll()
	}
	return nil
}

// settle closes open messages and the step and, when the run did not
// pause, emits the workflow's final output as text. Idempotent; Finish calls
// it, and the handler calls it first so Output is complete before the turn
// is handed to TurnFinished.
func (t *Translator) settle() error {
	if err := t.closeAll(); err != nil {
		return err
	}
	if err := t.endStep(); err != nil {
		return err
	}
	if len(t.interrupts) == 0 && t.lastOutput != "" {
		s := t.lastOutput
		t.lastOutput = ""
		return t.wholeText(s)
	}
	return nil
}

// Finish ends the run: success, or interrupt when the run paused.
func (t *Translator) Finish() error {
	if err := t.settle(); err != nil {
		return err
	}
	opt := events.WithSuccessOutcome()
	if len(t.interrupts) > 0 {
		out := slices.Clone(t.interrupts)
		if t.expiresAt != "" {
			for i := range out {
				out[i].ExpiresAt = t.expiresAt
			}
		}
		opt = events.WithInterruptOutcome(out)
	}
	return t.emit(events.NewRunFinishedEventWithOptions(t.threadID, t.runID, opt))
}

// emitCustom emits CUSTOM(name, value) and remembers it for Output.
func (t *Translator) emitCustom(name string, value any) error {
	if t.custom == nil {
		t.custom = map[string]any{}
	}
	t.custom[name] = value
	return t.emit(events.NewCustomEvent(name, events.WithValue(value)))
}

// customFrom emits the generic CUSTOM events an ADK event carries under
// CustomMetadata["agui.custom"]: one {"name", "value"} map or a slice of
// them. Entries without a name are ignored.
//
// Trust: whatever sits under this key reaches the client verbatim as a
// server-issued event. Only server-side Go code (workflow nodes, agent
// callbacks) may set it; model adapters and remote agents must never copy
// provider or remote fields into CustomMetadata.
func (t *Translator) customFrom(md map[string]any) error {
	raw, ok := md[customKey]
	if !ok {
		return nil
	}
	var entries []map[string]any
	switch v := raw.(type) {
	case map[string]any:
		entries = []map[string]any{v}
	case []map[string]any:
		entries = v
	case []any:
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				entries = append(entries, m)
			}
		}
	}
	for _, e := range entries {
		name, _ := e["name"].(string)
		if name == "" {
			continue
		}
		if err := t.emitCustom(name, e["value"]); err != nil {
			return err
		}
	}
	return nil
}

// Fail ends the run with RUN_ERROR.
func (t *Translator) Fail(err error) error {
	_ = t.closeAll()
	return t.emit(events.NewRunErrorEvent(err.Error()))
}

func (t *Translator) stepFor(ev *session.Event) error {
	if ev.NodeInfo == nil || ev.NodeInfo.Path == "" {
		return nil
	}
	name := StepName(ev.NodeInfo.Path)
	if name == t.step {
		return nil
	}
	if err := t.endStep(); err != nil {
		return err
	}
	t.step = name
	return t.emit(events.NewStepStartedEvent(t.step))
}

func (t *Translator) endStep() error {
	if t.step == "" {
		return nil
	}
	s := t.step
	t.step = ""
	return t.emit(events.NewStepFinishedEvent(s))
}

func (t *Translator) reasoning(delta string) error {
	if t.reasoningID == "" {
		t.reasoningID = uuid.NewString()
		if err := t.emit(events.NewReasoningStartEvent(t.reasoningID)); err != nil {
			return err
		}
		if err := t.emit(events.NewReasoningMessageStartEvent(t.reasoningID, "reasoning")); err != nil {
			return err
		}
	}
	t.outReasoning.WriteString(delta)
	return t.emit(events.NewReasoningMessageContentEvent(t.reasoningID, delta))
}

func (t *Translator) closeReasoning() error {
	if t.reasoningID == "" {
		return nil
	}
	id := t.reasoningID
	t.reasoningID = ""
	if err := t.emit(events.NewReasoningMessageEndEvent(id)); err != nil {
		return err
	}
	return t.emit(events.NewReasoningEndEvent(id))
}

// text handles a text part. Partials stream; the final aggregated event
// repeats the whole text, which is only emitted if nothing was streamed.
func (t *Translator) text(s string, partial bool) error {
	if !partial && t.streamed {
		return nil
	}
	if err := t.closeReasoning(); err != nil {
		return err
	}
	if t.textID == "" {
		t.textID = uuid.NewString()
		if t.outText.Len() > 0 {
			t.outText.WriteString("\n\n")
		}
		if err := t.emit(events.NewTextMessageStartEvent(t.textID, events.WithRole("assistant"))); err != nil {
			return err
		}
	}
	if partial {
		t.streamed = true
	}
	t.outText.WriteString(s)
	return t.emit(events.NewTextMessageContentEvent(t.textID, s))
}

func (t *Translator) wholeText(s string) error {
	if err := t.text(s, false); err != nil {
		return err
	}
	return t.closeText()
}

func (t *Translator) closeText() error {
	t.streamed = false
	if t.textID == "" {
		return nil
	}
	id := t.textID
	t.textID = ""
	return t.emit(events.NewTextMessageEndEvent(id))
}

func (t *Translator) closeAll() error {
	if err := t.closeReasoning(); err != nil {
		return err
	}
	return t.closeText()
}

func (t *Translator) call(fc *genai.FunctionCall) error {
	if err := t.closeAll(); err != nil {
		return err
	}
	switch fc.Name {
	case toolconfirmation.FunctionCallName:
		// ADK asks for approval before running a tool: an AG-UI interrupt.
		in := types.Interrupt{ID: fc.ID, Reason: "tool_approval", Metadata: types.Metadata{"request": fc.Args}}
		if orig, ok := fc.Args["originalFunctionCall"].(map[string]any); ok {
			if id, ok := orig["id"].(string); ok {
				in.ToolCallID = id
			}
			if name, ok := orig["name"].(string); ok {
				in.Message = fmt.Sprintf("Approve calling %s?", name)
			}
		}
		t.interrupts = append(t.interrupts, in)
		return nil
	case workflow.WorkflowInputFunctionCallName:
		return nil // surfaced via RequestedInput on the same event
	}
	args, err := json.Marshal(fc.Args)
	if err != nil {
		return err
	}
	if err := t.emit(events.NewToolCallStartEvent(fc.ID, fc.Name)); err != nil {
		return err
	}
	if err := t.emit(events.NewToolCallArgsEvent(fc.ID, string(args))); err != nil {
		return err
	}
	return t.emit(events.NewToolCallEndEvent(fc.ID))
}

func (t *Translator) result(fr *genai.FunctionResponse) error {
	if fr.Name == toolconfirmation.FunctionCallName || fr.Name == workflow.WorkflowInputFunctionCallName {
		return nil
	}
	body, err := json.Marshal(fr.Response)
	if err != nil {
		return err
	}
	if err := t.emit(events.NewToolCallResultEvent(uuid.NewString(), fr.ID, string(body))); err != nil {
		return err
	}
	if chunks, ok := fr.Response["chunks"]; ok {
		return t.emitCustom(ToolChunksEvent, map[string]any{"toolCallId": fr.ID, "chunks": chunks})
	}
	return nil
}

func inputInterrupt(r *session.RequestInput) types.Interrupt {
	in := types.Interrupt{ID: r.InterruptID, Reason: "input_required", Message: r.Message}
	if p, ok := r.Payload.(map[string]any); ok {
		if reason, ok := p["reason"].(string); ok && reason != "" {
			in.Reason = reason
		}
		in.Metadata = types.Metadata(p)
	}
	return in
}

// ErrNoInput is returned when a RunAgentInput carries neither a resume nor a
// new user message.
var ErrNoInput = errors.New("agui: run input has no user message and no resume")

// InterruptKind tells Input how to answer an interrupt id.
type InterruptKind func(ctx context.Context, interruptID string) (functionName string, err error)

// Input converts an AG-UI RunAgentInput into the ADK message for the run.
//
// Client-sent history is NOT trusted: only the newest user message is taken;
// earlier turns come from our own store. A client could otherwise forge
// assistant or tool messages into the model's context.
func Input(ctx context.Context, in *types.RunAgentInput, kind InterruptKind) (*genai.Content, error) {
	if len(in.Resume) > 0 {
		if in.ThreadID == "" {
			return nil, ErrResumeNeedsThread
		}
		if kind == nil {
			return nil, errors.New("agui: resume without an interrupt lookup")
		}
		parts := make([]*genai.Part, 0, len(in.Resume))
		for _, r := range in.Resume {
			name, err := kind(ctx, r.InterruptID)
			if err != nil {
				return nil, err
			}
			resolved := r.Status == types.ResumeStatusResolved
			var resp map[string]any
			switch name {
			case toolconfirmation.FunctionCallName:
				approved := resolved
				if p, ok := r.Payload.(map[string]any); ok {
					if a, ok := p["approved"].(bool); ok {
						approved = approved && a
					}
				}
				resp = map[string]any{"confirmed": approved}
			case workflow.WorkflowInputFunctionCallName:
				if !resolved {
					resp = map[string]any{"payload": map[string]any{"cancelled": true}}
				} else {
					resp = map[string]any{"payload": r.Payload}
				}
			default:
				return nil, fmt.Errorf("agui: interrupt %q is not open", r.InterruptID)
			}
			parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: r.InterruptID, Name: name, Response: resp}})
		}
		return &genai.Content{Role: genai.RoleUser, Parts: parts}, nil
	}
	for i := len(in.Messages) - 1; i >= 0; i-- {
		m := in.Messages[i]
		if m.Role != types.RoleUser {
			continue
		}
		if s := messageText(m.Content); strings.TrimSpace(s) != "" {
			return genai.NewContentFromText(s, genai.RoleUser), nil
		}
	}
	return nil, ErrNoInput
}

func messageText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]any); ok && m["type"] == "text" {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

// OpenInterrupts returns a lookup over a session's events: an interrupt is
// open if its request call has no answering response yet.
func OpenInterrupts(sess session.Session) InterruptKind {
	calls := map[string]string{}
	for ev := range sess.Events().All() {
		if ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			switch {
			case p.FunctionCall != nil && (p.FunctionCall.Name == toolconfirmation.FunctionCallName || p.FunctionCall.Name == workflow.WorkflowInputFunctionCallName):
				calls[p.FunctionCall.ID] = p.FunctionCall.Name
			case p.FunctionResponse != nil:
				delete(calls, p.FunctionResponse.ID)
			}
		}
	}
	return func(_ context.Context, id string) (string, error) {
		if n, ok := calls[id]; ok {
			return n, nil
		}
		return "", fmt.Errorf("agui: interrupt %q is not open", id)
	}
}
