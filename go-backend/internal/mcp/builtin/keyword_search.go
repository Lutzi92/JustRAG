package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/vector"
)

// KeywordSearchArgs documents the keyword_search tool input. Mirrors
// kb_search but with no top_k bias toward the global default — the
// tool is meant for cheap exact-match probes, so the default cap is
// set deliberately low (10) to keep the LLM's window clean.
type KeywordSearchArgs struct {
	Query   string   `json:"query"`
	TopK    int      `json:"top_k,omitempty"`
	FileIDs []string `json:"file_ids,omitempty"`
	KbID    string   `json:"kb_id,omitempty"` // injected by the orchestrator
}

const keywordSearchInputSchema = `{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query":   { "type": "string",  "description": "BM25 query — exact terms, quoted phrases, no semantic match." },
    "top_k":   { "type": "integer", "description": "Maximum chunks to return. Default 10, max 50." },
    "file_ids": { "type": "array", "items": { "type": "string" }, "description": "Optional file-ID allow-list." }
  }
}`

// KeywordSearcher is the minimal vector.SearchService surface this
// tool needs. Lets tests inject a fake without dragging in pgx.
type KeywordSearcher interface {
	KeywordSearch(ctx context.Context, kbID, query string, limit int, fileIDs []string) ([]vector.SearchChunk, error)
}

// NewKeywordSearch builds the keyword_search tool. Use over kb_search
// when the agent wants pure exact-match recall (e.g. "does the corpus
// contain the literal string `ARTIKEL 13`?") without paying for
// vector ANN + reranker latency. Returns chunks in BM25-rank order.
func NewKeywordSearch(svc KeywordSearcher) mcp.Tool {
	return mcp.Tool{
		Name:        "keyword_search",
		Description: "BM25-only search for exact-term recall. Faster than kb_search (no vector embedding, no reranker, no MMR). Use when the query has distinctive literal terms or quoted phrases.",
		InputSchema: json.RawMessage(keywordSearchInputSchema),
		Handler:     mcp.ToolHandlerFunc(keywordSearchHandler(svc)),
	}
}

func keywordSearchHandler(svc KeywordSearcher) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		var args KeywordSearchArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("keyword_search: parse args: %w", err)
		}
		if args.Query == "" {
			return mcp.ToolResult{}, fmt.Errorf("keyword_search: query is required")
		}
		if args.KbID == "" {
			return mcp.ToolResult{}, fmt.Errorf("keyword_search: kb_id was not injected by the orchestrator")
		}
		chunks, err := svc.KeywordSearch(ctx, args.KbID, args.Query, args.TopK, args.FileIDs)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("keyword_search: %w", err)
		}
		out := mcp.ToolResult{
			Chunks: make([]mcp.ResultChunk, 0, len(chunks)),
			Meta:   map[string]any{"chunk_count": len(chunks), "method": "bm25_only"},
		}
		for _, c := range chunks {
			out.Chunks = append(out.Chunks, mcp.ResultChunk{
				ID:      c.ID,
				FileID:  c.FileID,
				Content: c.Content,
				Score:   c.Score,
			})
		}
		return out, nil
	}
}
