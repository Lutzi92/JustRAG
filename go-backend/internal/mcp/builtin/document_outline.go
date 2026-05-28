package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/justrag/go-backend/internal/mcp"
)

// DocumentOutlineArgs documents the document_outline tool input.
type DocumentOutlineArgs struct {
	FileID string `json:"file_id"`
	KbID   string `json:"kb_id,omitempty"` // injected
}

const documentOutlineInputSchema = `{
  "type": "object",
  "required": ["file_id"],
  "properties": {
    "file_id": { "type": "string", "description": "UUID of the document to outline." }
  }
}`

// OutlineFetcher is the SearchService surface document_outline needs.
type OutlineFetcher interface {
	DocumentOutline(ctx context.Context, kbID, fileID string) ([]string, error)
}

// NewDocumentOutline builds the document_outline tool. Returns the
// per-chunk section breadcrumbs as a deduplicated list, giving the
// agent enough structure to navigate "go to the section about X" in
// long documents without re-parsing the source file.
//
// The result is a synthetic outline derived from chunk metadata —
// it does NOT include headings that fall inside a single chunk's
// text, only those that mark chunk boundaries. Good enough for
// router-style navigation; not a substitute for re-running the
// markdown parser.
func NewDocumentOutline(svc OutlineFetcher) mcp.Tool {
	return mcp.Tool{
		Name:        "document_outline",
		Description: "Return the breadcrumb-style heading structure of one document, reconstructed from chunk metadata. Use to find which section to inspect with chunk_read or kb_search.",
		InputSchema: json.RawMessage(documentOutlineInputSchema),
		Handler:     mcp.ToolHandlerFunc(documentOutlineHandler(svc)),
	}
}

func documentOutlineHandler(svc OutlineFetcher) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		var args DocumentOutlineArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("document_outline: parse args: %w", err)
		}
		if args.FileID == "" {
			return mcp.ToolResult{}, fmt.Errorf("document_outline: file_id is required")
		}
		if args.KbID == "" {
			return mcp.ToolResult{}, fmt.Errorf("document_outline: kb_id was not injected by the orchestrator")
		}
		breadcrumbs, err := svc.DocumentOutline(ctx, args.KbID, args.FileID)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("document_outline: %w", err)
		}
		// Outline lives in Meta because mcp.ResultChunk is shaped for
		// chunk content, not heading lists. The frontend / agent
		// pulls headings from `meta.outline` and renders them inline.
		return mcp.ToolResult{
			Chunks: nil,
			Meta: map[string]any{
				"file_id":         args.FileID,
				"outline":         breadcrumbs,
				"heading_count":   len(breadcrumbs),
				"position_basis":  "chunk_breadcrumbs",
				"limitation_note": "headings inside a single chunk are NOT surfaced",
			},
		}, nil
	}
}
