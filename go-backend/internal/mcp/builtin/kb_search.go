// Package builtin holds the in-process MCP tools that ship with every
// JustRAG deployment. They satisfy mcp.ToolHandler and register into the
// process-wide mcp.Registry at startup. Per the Phase 2 §2.1 plan the
// initial set is:
//
//	kb_search   — wraps vector.SearchService.Search
//	web_search  — wraps internal/research/web.go's Google + fetcher pipeline
//	memory_read — Phase 2 §2.3 session memory accessor
//	memory_write — Phase 2 §2.3 session memory mutator
//
// Each tool is constructed with the dependencies it needs (search service,
// web client, session-memory store) so it can be wired in app/routes.go
// without the mcp package importing the entire backend graph.
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/vector"
)

// KbSearchArgs is the documented argument shape for the kb_search
// built-in tool.
type KbSearchArgs struct {
	Query    string   `json:"query"`
	TopK     int      `json:"top_k,omitempty"`
	FileIDs  []string `json:"file_ids,omitempty"`
	KbID     string   `json:"kb_id,omitempty"` // populated by the orchestrator
	DateFrom string   `json:"date_from,omitempty"`
	DateTo   string   `json:"date_to,omitempty"`
}

// kbSearchInputSchema is the JSON Schema embedded into the registered
// tool. Required: `query`. The orchestrator injects `kb_id` via the
// dispatch layer rather than asking the LLM to know its own KB.
const kbSearchInputSchema = `{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query":   { "type": "string",  "description": "Natural-language search query." },
    "top_k":   { "type": "integer", "description": "Maximum number of chunks to return. 0 = use site default." },
    "file_ids": {
      "type":  "array",
      "items": { "type": "string" },
      "description": "Optional file-ID allow-list to restrict the search."
    }
    ,"date_from": { "type": "string", "description": "Optional inclusive start date (ISO YYYY-MM-DD) to restrict results to documents added on/after it. Compute from the current date in the system prompt." },
    "date_to":   { "type": "string", "description": "Optional inclusive end date (ISO YYYY-MM-DD)." }
  }
}`

// SearchService is the minimal vector.SearchService surface kb_search
// needs. Defining it here lets the test suite inject a fake without
// dragging the full vector dependency chain into the test process.
type SearchService interface {
	Search(ctx context.Context, kbID, query string, limit int, opts vector.SearchOptions) (*vector.SearchResult, error)
}

// NewKbSearch builds the kb_search tool. The (kbID, fileIDs) defaults
// arrive from the chat orchestrator at dispatch time via the args
// payload — the chat layer is responsible for injecting `kb_id` so the
// LLM doesn't need to know it.
func NewKbSearch(svc SearchService) mcp.Tool {
	return mcp.Tool{
		Name:        "kb_search",
		Description: "Search the knowledge base for relevant chunks. Returns ranked, deduplicated evidence chunks with file metadata.",
		InputSchema: json.RawMessage(kbSearchInputSchema),
		Handler:     mcp.ToolHandlerFunc(kbSearchHandler(svc)),
	}
}

func kbSearchHandler(svc SearchService) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		var args KbSearchArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("kb_search: parse args: %w", err)
		}
		if args.Query == "" {
			return mcp.ToolResult{}, fmt.Errorf("kb_search: query is required")
		}
		if args.KbID == "" {
			return mcp.ToolResult{}, fmt.Errorf("kb_search: kb_id was not injected by the orchestrator")
		}
		var createdAfter, createdBefore *time.Time
		if args.DateFrom != "" {
			d, perr := time.Parse("2006-01-02", args.DateFrom)
			if perr != nil {
				return mcp.ToolResult{}, fmt.Errorf("kb_search: date_from must be ISO YYYY-MM-DD: %w", perr)
			}
			createdAfter = &d
		}
		if args.DateTo != "" {
			d, perr := time.Parse("2006-01-02", args.DateTo)
			if perr != nil {
				return mcp.ToolResult{}, fmt.Errorf("kb_search: date_to must be ISO YYYY-MM-DD: %w", perr)
			}
			end := d.Add(24*time.Hour - time.Second)
			createdBefore = &end
		}
		opts := vector.SearchOptions{
			FileIDs:       args.FileIDs,
			QueryType:     vector.QueryTypeComplexReasoning,
			CreatedAfter:  createdAfter,
			CreatedBefore: createdBefore,
		}
		res, err := svc.Search(ctx, args.KbID, args.Query, args.TopK, opts)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("kb_search: %w", err)
		}
		out := mcp.ToolResult{
			Chunks: make([]mcp.ResultChunk, 0, len(res.Chunks)),
			Meta:   map[string]any{"chunk_count": len(res.Chunks)},
		}
		for _, c := range res.Chunks {
			out.Chunks = append(out.Chunks, mcp.ResultChunk{
				ID:       c.ID,
				FileID:   c.FileID,
				FileName: c.FileName,
				Content:  c.Content,
				Score:    c.Score,
			})
		}
		return out, nil
	}
}

// VectorChunksFromResult is the orchestrator-side helper that converts
// kb_search's protocol-shaped chunks back into vector.SearchChunk so the
// existing dedup/sandwich/MMR helpers continue to work without a parallel
// implementation. Score and Content are sufficient for the downstream
// pipeline; fields the registry doesn't expose (RerankScore, Grade,
// ParentChunkID) are zero-valued.
func VectorChunksFromResult(r mcp.ToolResult) []vector.SearchChunk {
	if len(r.Chunks) == 0 {
		return nil
	}
	out := make([]vector.SearchChunk, len(r.Chunks))
	for i, c := range r.Chunks {
		out[i] = vector.SearchChunk{
			ID:       c.ID,
			FileID:   c.FileID,
			FileName: c.FileName,
			Content:  c.Content,
			Score:    c.Score,
		}
	}
	return out
}
