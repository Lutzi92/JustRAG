package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const askKBToolName = "ask_kb"

// Source is one citation surfaced in ask_kb's structured output. It is a
// lossy projection of chat.ChatSource (no chunk content — the answer text
// already embeds the evidence).
type Source struct {
	Index    int     `json:"index"`
	FileID   string  `json:"file_id"`
	FileName string  `json:"file_name"`
	Score    float64 `json:"score"`
}

// AnswerResult is what the production pipeline returns for one question.
type AnswerResult struct {
	Answer  string
	Sources []Source
}

// Answerer runs the real RAG answer pipeline for one KB question. The
// production implementation lives in pipeline.go; tests inject a fake.
type Answerer interface {
	Answer(ctx context.Context, kbID, question, language string) (AnswerResult, error)
}

type toolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// callResult is the MCP tools/call result shape (2025 spec): a content
// array, optional structuredContent, and an isError flag.
type callResult struct {
	Content           []contentBlock  `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

const askKBInputSchema = `{
  "type": "object",
  "required": ["question"],
  "properties": {
    "question": { "type": "string", "description": "Natural-language question to ask the knowledge base." },
    "language": { "type": "string", "enum": ["de", "en"], "description": "Answer language. Defaults to de." }
  }
}`

func askKBDescriptor() toolDescriptor {
	return toolDescriptor{
		Name:        askKBToolName,
		Description: "Ask the knowledge base a natural-language question. Runs the full RAG pipeline (retrieval, reranking, citation validation) and returns a synthesized answer with sources.",
		InputSchema: json.RawMessage(askKBInputSchema),
	}
}

type askKBArgs struct {
	Question string `json:"question"`
	Language string `json:"language"`
}

// runAskKB executes the ask_kb tool. kbID is injected from the URL path by
// the caller — it is NEVER read from arguments. A nil error means the call
// completed (success OR a pipeline failure reported via IsError); a non-nil
// error means invalid params (the caller maps it to JSON-RPC -32602).
func runAskKB(ctx context.Context, a Answerer, kbID string, arguments json.RawMessage) (callResult, error) {
	var args askKBArgs
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return callResult{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if strings.TrimSpace(args.Question) == "" {
		return callResult{}, fmt.Errorf("question is required")
	}
	lang := args.Language
	if lang != "de" && lang != "en" {
		lang = "de"
	}

	out, err := a.Answer(ctx, kbID, args.Question, lang)
	if err != nil {
		return callResult{
			Content: []contentBlock{{Type: "text", Text: "ask_kb failed to produce an answer"}},
			IsError: true,
		}, nil
	}

	// cannot fail: Source is all scalar fields, no unsupported types.
	sc, _ := json.Marshal(struct {
		Sources []Source `json:"sources"`
	}{Sources: out.Sources})

	return callResult{
		Content:           []contentBlock{{Type: "text", Text: out.Answer}},
		StructuredContent: sc,
	}, nil
}
