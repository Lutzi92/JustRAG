package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/research"
)

// WebSearchArgs is the documented argument shape for the web_search
// built-in tool. The orchestrator passes `language` through from the
// chat request; the LLM only needs to supply `query` (and optionally a
// `limit` cap).
type WebSearchArgs struct {
	Query    string `json:"query"`
	Limit    int    `json:"limit,omitempty"`
	Language string `json:"language,omitempty"` // injected by the orchestrator
}

const webSearchInputSchema = `{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query":    { "type": "string",  "description": "Search query for the web." },
    "limit":    { "type": "integer", "description": "Maximum number of pages to fetch (1..10)." }
  }
}`

// NewWebSearch registers the web_search built-in. The result is returned
// as Text (concatenated page summaries) rather than Chunks because the
// content is fetched-and-extracted prose without the file-scoped metadata
// kb_search produces. The orchestrator surfaces this as a `web_search`
// trajectory entry; how (or whether) it folds into the LLM context is a
// separate per-orchestrator decision.
func NewWebSearch(client *research.WebClient) mcp.Tool {
	return mcp.Tool{
		Name:        "web_search",
		Description: "Search the public web via Google Custom Search and return extracted page content for the top results.",
		InputSchema: json.RawMessage(webSearchInputSchema),
		Handler:     mcp.ToolHandlerFunc(webSearchHandler(client)),
	}
}

func webSearchHandler(client *research.WebClient) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		var args WebSearchArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("web_search: parse args: %w", err)
		}
		if args.Query == "" {
			return mcp.ToolResult{}, fmt.Errorf("web_search: query is required")
		}
		if client == nil {
			return mcp.ToolResult{}, fmt.Errorf("web_search: web client not configured (check google_search_api_key + web_search_enabled)")
		}
		pages, err := client.Search(ctx, args.Query, args.Limit, args.Language)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("web_search: %w", err)
		}
		var b strings.Builder
		for i, p := range pages {
			if i > 0 {
				b.WriteString("\n\n---\n\n")
			}
			b.WriteString("[")
			b.WriteString(p.URL)
			b.WriteString("] ")
			b.WriteString(p.Title)
			b.WriteByte('\n')
			b.WriteString(p.Content)
		}
		return mcp.ToolResult{
			Text: b.String(),
			Meta: map[string]any{"page_count": len(pages)},
		}, nil
	}
}
