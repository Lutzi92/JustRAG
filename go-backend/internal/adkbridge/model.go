package adkbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/justrag/go-backend/internal/ai"
)

// ChatClient is the subset of *ai.Client the adapter needs. Declared here so
// tests can substitute a fake without an HTTP server.
type ChatClient interface {
	ChatCompletion(ctx context.Context, req *ai.ChatRequest) (*ai.ChatResponse, error)
	StreamChatCompletion(ctx context.Context, req ai.ChatRequest) (<-chan ai.StreamChunk, error)
}

// Model implements ADK's model.LLM over our OpenAI-compatible client.
type Model struct {
	client ChatClient
	name   string
	// reasoningEffort, when non-empty, switches thinking on for every call
	// (gemma-4 needs chat_template_kwargs.enable_thinking; see
	// ai.ThinkingChatTemplateKwargs). A request's ThinkingConfig overrides it.
	reasoningEffort string
}

// Option configures a Model.
type Option func(*Model)

// WithReasoning turns thinking on for every call at the given effort.
func WithReasoning(effort string) Option { return func(m *Model) { m.reasoningEffort = effort } }

// NewModel returns an ADK model backed by client, calling modelName.
func NewModel(client ChatClient, modelName string, opts ...Option) *Model {
	m := &Model{client: client, name: modelName}
	for _, o := range opts {
		o(m)
	}
	return m
}

var _ model.LLM = (*Model)(nil)

// Name implements model.LLM.
func (m *Model) Name() string { return m.name }

// GenerateContent implements model.LLM.
func (m *Model) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		chatReq, err := m.buildRequest(req)
		if err != nil {
			yield(nil, err)
			return
		}
		if !stream {
			resp, err := m.client.ChatCompletion(ctx, &chatReq)
			if err != nil {
				yield(nil, err)
				return
			}
			out, err := convertResponse(resp)
			yield(out, err)
			return
		}
		m.stream(ctx, chatReq, yield)
	}
}

func (m *Model) stream(ctx context.Context, chatReq ai.ChatRequest, yield func(*model.LLMResponse, error) bool) {
	// Stopping early (yield false) must release the producer goroutine.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := m.client.StreamChatCompletion(ctx, chatReq)
	if err != nil {
		yield(nil, err)
		return
	}
	var (
		text, thought strings.Builder
		calls         = map[int]*ai.ToolCall{}
		finish        string
		filter        ai.ThinkTagFilter
	)
	for chunk := range ch {
		if chunk.Err != nil {
			yield(nil, fmt.Errorf("adkbridge: stream aborted: %w", chunk.Err))
			return
		}
		if r := firstNonEmpty(chunk.ReasoningContent, chunk.Reasoning); r != "" {
			thought.WriteString(r)
			if !yield(partial(&genai.Part{Text: r, Thought: true}), nil) {
				return
			}
		}
		if chunk.Content != "" {
			for _, seg := range filter.Process(chunk.Content) {
				if seg.Reasoning != "" {
					thought.WriteString(seg.Reasoning)
					if !yield(partial(&genai.Part{Text: seg.Reasoning, Thought: true}), nil) {
						return
					}
				}
				if seg.Content != "" {
					text.WriteString(seg.Content)
					if !yield(partial(&genai.Part{Text: seg.Content}), nil) {
						return
					}
				}
			}
		}
		for _, d := range chunk.ToolCallDeltas {
			c := calls[d.Index]
			if c == nil {
				c = &ai.ToolCall{Type: "function"}
				calls[d.Index] = c
			}
			if c.ID == "" {
				c.ID = d.ID
			}
			if c.Function.Name == "" {
				c.Function.Name = d.Name
			}
			c.Function.Arguments += d.Arguments
		}
		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}
	}
	// A tail the filter held back (e.g. a trailing "<") is streamed too:
	// AG-UI shows only partials once any were streamed.
	if seg := filter.Flush(); seg.Reasoning != "" {
		thought.WriteString(seg.Reasoning)
		if !yield(partial(&genai.Part{Text: seg.Reasoning, Thought: true}), nil) {
			return
		}
	} else if seg.Content != "" {
		text.WriteString(seg.Content)
		if !yield(partial(&genai.Part{Text: seg.Content}), nil) {
			return
		}
	}
	// The final, non-partial response carries the whole turn: ADK's runner
	// only processes this one; partials are forwarded for display.
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	ordered := make([]ai.ToolCall, 0, len(idx))
	for _, i := range idx {
		ordered = append(ordered, *calls[i])
	}
	final := &model.LLMResponse{
		Content:      assembleContent(thought.String(), text.String(), ordered),
		FinishReason: mapFinish(finish),
		TurnComplete: true,
	}
	yield(final, nil)
}

func partial(p *genai.Part) *model.LLMResponse {
	return &model.LLMResponse{
		Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{p}},
		Partial: true,
	}
}

func convertResponse(resp *ai.ChatResponse) (*model.LLMResponse, error) {
	if resp == nil || len(resp.Choices) == 0 {
		return nil, errors.New("adkbridge: empty completion")
	}
	ch := resp.Choices[0]
	var f ai.ThinkTagFilter
	var thought, text strings.Builder
	thought.WriteString(firstNonEmpty(ch.Message.ReasoningContent, ch.Message.Reasoning))
	for _, seg := range append(f.Process(ch.Message.Content), f.Flush()) {
		thought.WriteString(seg.Reasoning)
		text.WriteString(seg.Content)
	}
	out := &model.LLMResponse{
		Content:      assembleContent(thought.String(), text.String(), ch.Message.ToolCalls),
		FinishReason: mapFinish(ch.FinishReason),
		TurnComplete: true,
	}
	if u := resp.Usage; u.TotalTokens > 0 {
		out.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        int32(u.PromptTokens),
			CandidatesTokenCount:    int32(u.CompletionTokens),
			TotalTokenCount:         int32(u.TotalTokens),
			CachedContentTokenCount: int32(u.CachedTokens()),
		}
	}
	return out, nil
}

func assembleContent(thought, text string, calls []ai.ToolCall) *genai.Content {
	c := &genai.Content{Role: genai.RoleModel}
	if thought != "" {
		c.Parts = append(c.Parts, &genai.Part{Text: thought, Thought: true})
	}
	if text != "" {
		c.Parts = append(c.Parts, &genai.Part{Text: text})
	}
	for _, tc := range calls {
		id := tc.ID
		if id == "" {
			// vLLM's gemma parser has been seen to omit ids; ADK pairs the
			// response by id and AG-UI needs toolCallIds unique across the
			// run, so mint a random one (a per-turn index would repeat).
			id = "call_" + uuid.NewString()
		}
		c.Parts = append(c.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{
			ID:   id,
			Name: tc.Function.Name,
			Args: parseArgs(tc.Function.Arguments),
		}})
	}
	return c
}

// parseArgs decodes tool-call arguments. Malformed JSON is passed through
// under a sentinel key so the tool's schema validation rejects it and the
// model sees the error, instead of the turn failing outright.
func parseArgs(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{"__invalid_arguments": raw}
	}
	return m
}

func mapFinish(r string) genai.FinishReason {
	switch r {
	case "length":
		return genai.FinishReasonMaxTokens
	case "", "stop", "tool_calls":
		return genai.FinishReasonStop
	default:
		return genai.FinishReasonOther
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// buildRequest converts an ADK request into our ChatRequest.
func (m *Model) buildRequest(req *model.LLMRequest) (ai.ChatRequest, error) {
	if req == nil {
		return ai.ChatRequest{}, errors.New("adkbridge: nil request")
	}
	out := ai.ChatRequest{Model: m.name}
	if req.Model != "" {
		out.Model = req.Model
	}
	msgs, err := convertContents(req.Contents)
	if err != nil {
		return ai.ChatRequest{}, err
	}
	effort := m.reasoningEffort
	if cfg := req.Config; cfg != nil {
		if cfg.SystemInstruction != nil {
			if sys := flattenText(cfg.SystemInstruction); sys != "" {
				msgs = append([]ai.ChatMessage{{Role: "system", Content: sys}}, msgs...)
			}
		}
		if cfg.Temperature != nil {
			t := float64(*cfg.Temperature)
			out.Temperature = &t
		}
		if cfg.MaxOutputTokens > 0 {
			out.MaxTokens = int(cfg.MaxOutputTokens)
		}
		if rf, err := responseFormat(cfg); err != nil {
			return ai.ChatRequest{}, err
		} else if rf != nil {
			out.ResponseFormat = rf
		}
		tools, err := convertTools(cfg)
		if err != nil {
			return ai.ChatRequest{}, err
		}
		out.Tools = tools
		if len(tools) > 0 && cfg.ToolConfig != nil && cfg.ToolConfig.FunctionCallingConfig != nil {
			out.ToolChoice = toolChoice(cfg.ToolConfig.FunctionCallingConfig)
		}
		if tc := cfg.ThinkingConfig; tc != nil {
			switch {
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget == 0:
				effort = ""
			case tc.ThinkingLevel != "" && tc.ThinkingLevel != genai.ThinkingLevelUnspecified:
				effort = strings.ToLower(string(tc.ThinkingLevel))
			case tc.IncludeThoughts && effort == "":
				effort = "medium"
			}
		}
	}
	if len(msgs) == 0 {
		return ai.ChatRequest{}, errors.New("adkbridge: request has no messages")
	}
	out.Messages = msgs
	out.ReasoningEffort = effort
	out.ChatTemplateKwargs = ai.ThinkingChatTemplateKwargs(effort)
	return out, nil
}

func convertContents(contents []*genai.Content) ([]ai.ChatMessage, error) {
	var msgs []ai.ChatMessage
	for _, c := range contents {
		if c == nil || len(c.Parts) == 0 {
			continue
		}
		role := "user"
		switch c.Role {
		case "", genai.RoleUser:
		case genai.RoleModel:
			role = "assistant"
		case "system":
			role = "system"
		default:
			return nil, fmt.Errorf("adkbridge: unsupported role %q", c.Role)
		}
		var (
			texts []string
			calls []ai.ToolCall
		)
		for _, p := range c.Parts {
			if p == nil {
				continue
			}
			if p.Text != "" && !p.Thought {
				texts = append(texts, p.Text)
			}
			switch {
			case p.FunctionCall != nil:
				args, err := json.Marshal(nonNilMap(p.FunctionCall.Args))
				if err != nil {
					return nil, fmt.Errorf("adkbridge: marshal call args: %w", err)
				}
				calls = append(calls, ai.ToolCall{ID: p.FunctionCall.ID, Type: "function",
					Function: ai.ToolCallFunction{Name: p.FunctionCall.Name, Arguments: string(args)}})
			case p.FunctionResponse != nil:
				payload, err := json.Marshal(p.FunctionResponse.Response)
				if err != nil {
					return nil, fmt.Errorf("adkbridge: marshal function response: %w", err)
				}
				msgs = append(msgs, ai.ChatMessage{Role: "tool", ToolCallID: p.FunctionResponse.ID,
					Name: p.FunctionResponse.Name, Content: string(payload)})
			case p.InlineData != nil || p.FileData != nil:
				// Vision goes through ai.DescribeImage today; multimodal agent
				// turns are not supported yet.
				return nil, errors.New("adkbridge: inline/file data parts not supported yet")
			}
		}
		text := strings.Join(texts, "\n")
		if len(calls) > 0 {
			if role != "assistant" {
				return nil, fmt.Errorf("adkbridge: function call in a %s turn", role)
			}
			msgs = append(msgs, ai.ChatMessage{Role: "assistant", Content: text, ToolCalls: calls})
			continue
		}
		if strings.TrimSpace(text) != "" {
			msgs = append(msgs, ai.ChatMessage{Role: role, Content: text})
		}
	}
	return msgs, nil
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func flattenText(c *genai.Content) string {
	var parts []string
	for _, p := range c.Parts {
		if p != nil && p.Text != "" && !p.Thought {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func convertTools(cfg *genai.GenerateContentConfig) ([]ai.ChatTool, error) {
	var tools []ai.ChatTool
	for _, t := range cfg.Tools {
		if t == nil {
			continue
		}
		for _, d := range t.FunctionDeclarations {
			if d == nil || d.Name == "" {
				return nil, errors.New("adkbridge: function declaration without name")
			}
			params, err := declParams(d)
			if err != nil {
				return nil, fmt.Errorf("adkbridge: tool %q: %w", d.Name, err)
			}
			tools = append(tools, ai.ChatTool{Type: "function",
				Function: ai.ChatToolFunction{Name: d.Name, Description: d.Description, Parameters: params}})
		}
	}
	return tools, nil
}

func declParams(d *genai.FunctionDeclaration) (json.RawMessage, error) {
	switch {
	case d.ParametersJsonSchema != nil:
		return marshalSchema(d.ParametersJsonSchema)
	case d.Parameters != nil:
		return genaiSchemaJSON(d.Parameters)
	default:
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
}

func marshalSchema(v any) (json.RawMessage, error) {
	if raw, ok := v.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(v)
}

// genaiSchemaJSON renders a genai.Schema as JSON Schema. genai spells types in
// upper case ("OBJECT"); JSON Schema and vLLM's guided decoding need lower case.
func genaiSchemaJSON(s *genai.Schema) (json.RawMessage, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	lowerTypes(v)
	return json.Marshal(v)
}

func lowerTypes(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "type" {
				if s, ok := val.(string); ok {
					t[k] = strings.ToLower(s)
					continue
				}
			}
			lowerTypes(val)
		}
	case []any:
		for _, e := range t {
			lowerTypes(e)
		}
	}
}

func responseFormat(cfg *genai.GenerateContentConfig) (*ai.ResponseFormat, error) {
	var (
		schema json.RawMessage
		err    error
	)
	switch {
	case cfg.ResponseJsonSchema != nil:
		schema, err = marshalSchema(cfg.ResponseJsonSchema)
	case cfg.ResponseSchema != nil:
		schema, err = genaiSchemaJSON(cfg.ResponseSchema)
	case cfg.ResponseMIMEType == "application/json":
		return &ai.ResponseFormat{Type: "json_object"}, nil
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	name := "adk_response"
	if cfg.ResponseSchema != nil && cfg.ResponseSchema.Title != "" {
		name = cfg.ResponseSchema.Title
	}
	return &ai.ResponseFormat{Type: "json_schema",
		JSONSchema: &ai.ResponseJSONSchema{Name: name, Strict: true, Schema: schema}}, nil
}

func toolChoice(fc *genai.FunctionCallingConfig) any {
	switch fc.Mode {
	case genai.FunctionCallingConfigModeNone:
		return "none"
	case genai.FunctionCallingConfigModeAny:
		if len(fc.AllowedFunctionNames) == 1 {
			return map[string]any{"type": "function", "function": map[string]any{"name": fc.AllowedFunctionNames[0]}}
		}
		return "required"
	default:
		return nil
	}
}
