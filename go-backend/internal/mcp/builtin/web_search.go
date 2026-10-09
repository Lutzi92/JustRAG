package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/research"
	"github.com/justrag/go-backend/internal/websearch"
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

// Generic tool errors for the model (see webSearchHandler).
var (
	errWebSearchUnavailable = errors.New("web_search: web search is not available")
	errWebSearchFailed      = errors.New("web_search: the web search failed")
)

func webSearchHandler(client *research.WebClient) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		var args WebSearchArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("web_search: parse args: %w", err)
		}
		if args.Query == "" {
			return mcp.ToolResult{}, fmt.Errorf("web_search: query is required")
		}
		// The error text goes back to the model, which may repeat it to the
		// user: it stays generic and never names admin config keys. The
		// detail (which switch or credential is missing) goes to the log.
		if client == nil {
			logctx.From(ctx).Warn("web_search: tool called without a web client")
			return mcp.ToolResult{}, errWebSearchUnavailable
		}
		pages, err := client.Search(ctx, args.Query, args.Limit, args.Language)
		if err != nil {
			logctx.From(ctx).Warn("web_search: search failed", "error", err)
			if websearch.IsUnavailable(err) {
				return mcp.ToolResult{}, errWebSearchUnavailable
			}
			return mcp.ToolResult{}, errWebSearchFailed
		}
		// Fetched pages are attacker-controllable text: frame them as
		// untrusted data before any model reads them (prompts.WebSearchResults).
		framed := make([]prompts.WebSearchResultPage, len(pages))
		for i, p := range pages {
			framed[i] = prompts.WebSearchResultPage{URL: p.URL, Title: p.Title, Content: p.Content}
		}
		return mcp.ToolResult{
			Text: prompts.WebSearchResults(framed),
			Meta: map[string]any{"page_count": len(pages)},
		}, nil
	}
}
