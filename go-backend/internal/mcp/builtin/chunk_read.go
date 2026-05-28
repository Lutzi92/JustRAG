package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/vector"
)

// ChunkReadArgs documents the chunk_read tool input. The agent uses
// this to expand context around a chunk-id it already knows about
// (typically from a kb_search result), without paying for another
// retrieval round.
type ChunkReadArgs struct {
	ChunkID   string `json:"chunk_id"`
	Direction string `json:"direction,omitempty"` // prev_sibling | next_sibling | both (default)
	Depth     int    `json:"depth,omitempty"`     // 1..10; default 1
	KbID      string `json:"kb_id,omitempty"`     // injected
}

const chunkReadInputSchema = `{
  "type": "object",
  "required": ["chunk_id"],
  "properties": {
    "chunk_id":  { "type": "string", "description": "UUID of an already-known chunk." },
    "direction": { "type": "string", "enum": ["prev_sibling", "next_sibling", "both"], "description": "Which neighbours to fetch." },
    "depth":     { "type": "integer", "description": "Chunks to fetch per direction. 1..10, default 1." }
  }
}`

// ChunkReader is the SearchService surface chunk_read needs.
type ChunkReader interface {
	LookupChunkNeighbors(ctx context.Context, kbID, chunkID, direction string, depth int) ([]vector.ChunkRow, error)
}

// NewChunkRead builds the chunk_read tool. Returns the target chunk
// + up to N neighbours on each side, in source order. The "source
// order" guarantee is best-effort: it relies on chunks being
// inserted sequentially during ingestion (chunk table has no
// explicit position column today). Re-ingestion that reorders
// chunks would invalidate this for the affected file.
func NewChunkRead(svc ChunkReader) mcp.Tool {
	return mcp.Tool{
		Name:        "chunk_read",
		Description: "Expand context around a known chunk by fetching neighbouring chunks from the same file. Useful when a kb_search hit needs surrounding context to be informative.",
		InputSchema: json.RawMessage(chunkReadInputSchema),
		Handler:     mcp.ToolHandlerFunc(chunkReadHandler(svc)),
	}
}

func chunkReadHandler(svc ChunkReader) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		var args ChunkReadArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("chunk_read: parse args: %w", err)
		}
		if args.ChunkID == "" {
			return mcp.ToolResult{}, fmt.Errorf("chunk_read: chunk_id is required")
		}
		if args.KbID == "" {
			return mcp.ToolResult{}, fmt.Errorf("chunk_read: kb_id was not injected by the orchestrator")
		}
		rows, err := svc.LookupChunkNeighbors(ctx, args.KbID, args.ChunkID, args.Direction, args.Depth)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("chunk_read: %w", err)
		}
		out := mcp.ToolResult{
			Chunks: make([]mcp.ResultChunk, 0, len(rows)),
			Meta: map[string]any{
				"chunk_count":    len(rows),
				"direction":      args.Direction,
				"depth":          args.Depth,
				"target_chunk":   args.ChunkID,
				"navigation":     "sibling",
				"position_basis": "created_at",
			},
		}
		for _, r := range rows {
			out.Chunks = append(out.Chunks, mcp.ResultChunk{
				ID:      r.ID,
				FileID:  r.FileID,
				Content: r.Content,
			})
		}
		return out, nil
	}
}
