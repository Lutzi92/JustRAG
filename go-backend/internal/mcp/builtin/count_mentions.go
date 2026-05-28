package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/justrag/go-backend/internal/mcp"
)

// CountMentionsArgs documents the count_mentions tool input.
//
// Single-query usage (legacy): pass `query` with the substring. Returns
// one integer.
//
// Multi-query usage (preferred for ambiguous matches like person names
// in multiple forms): pass `queries` with an array of candidate
// substrings. Returns each substring with its individual count. Useful
// for "how often is <person> mentioned" where "Karcher", "Steffen
// Karcher", and "steffen.karcher@..." all match different sets.
type CountMentionsArgs struct {
	Query   string   `json:"query"`
	Queries []string `json:"queries"`
	KbID    string   `json:"kb_id,omitempty"` // injected by the orchestrator
}

const countMentionsInputSchema = `{
  "type": "object",
  "properties": {
    "query":   { "type": "string", "description": "Single literal substring to count. Case-insensitive." },
    "queries": { "type": "array", "items": { "type": "string" }, "description": "Multiple candidate substrings. Each is counted independently and the per-query counts are returned together. Prefer this for person names that may appear as 'Surname', 'Firstname Surname', or 'firstname.surname@…' — pass all forms in one call rather than calling the tool multiple times." }
  }
}`

// MentionsCounter is the minimal vector.SearchService surface the tool
// needs. Lets tests inject a fake without dragging in pgx.
type MentionsCounter interface {
	CountMentions(ctx context.Context, kbID, pattern string) (int, error)
}

// NewCountMentions builds the count_mentions tool. Use over kb_search +
// LLM-side counting when the question is "how many chunks contain X" —
// the count is deterministic and the LLM never has to count retrieved
// chunks (which it does poorly on long lists).
//
// For ambiguous matches (names that appear in multiple forms), the
// caller can pass `queries: ["...", ...]` and get all counts in one
// call rather than calling the tool multiple times and confusing
// itself with cross-results.
func NewCountMentions(svc MentionsCounter) mcp.Tool {
	return mcp.Tool{
		Name:        "count_mentions",
		Description: "Counts CHUNKS in the current knowledge base whose text contains a given literal substring (case-insensitive). Authoritative for 'how often / wie oft / how many times X is mentioned' questions across the WHOLE KB — the retrieved context is only a small sample, this counts everything. For person names that may appear in multiple forms (surname only, full name, email), pass ALL forms in a single call via `queries: [\"Karcher\", \"Steffen Karcher\", \"steffen.karcher\"]` and pick the largest meaningful count as the answer. Do NOT call the tool multiple times with variants. Counts chunks (not unique files), so one file with multiple matches contributes more than one.",
		InputSchema: json.RawMessage(countMentionsInputSchema),
		Handler:     mcp.ToolHandlerFunc(countMentionsHandler(svc)),
	}
}

func countMentionsHandler(svc MentionsCounter) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		var args CountMentionsArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("count_mentions: parse args: %w", err)
		}
		queries := args.Queries
		if args.Query != "" {
			queries = append(queries, args.Query)
		}
		if len(queries) == 0 {
			return mcp.ToolResult{}, fmt.Errorf("count_mentions: query or queries is required")
		}
		if args.KbID == "" {
			return mcp.ToolResult{}, fmt.Errorf("count_mentions: kb_id was not injected by the orchestrator")
		}

		type pair struct {
			Query string `json:"query"`
			Count int    `json:"count"`
		}
		results := make([]pair, 0, len(queries))
		for _, q := range queries {
			if q == "" {
				continue
			}
			n, err := svc.CountMentions(ctx, args.KbID, q)
			if err != nil {
				return mcp.ToolResult{}, fmt.Errorf("count_mentions: query %q: %w", q, err)
			}
			results = append(results, pair{Query: q, Count: n})
		}
		if len(results) == 0 {
			return mcp.ToolResult{}, fmt.Errorf("count_mentions: all queries empty")
		}
		// Sort by descending count so the model's eye lands on the
		// largest-and-likely-most-meaningful figure first.
		sort.SliceStable(results, func(i, j int) bool { return results[i].Count > results[j].Count })

		var text strings.Builder
		for _, p := range results {
			fmt.Fprintf(&text, "%q: %d\n", p.Query, p.Count)
		}
		structured, _ := json.Marshal(map[string]any{"results": results})
		return mcp.ToolResult{
			Text:       strings.TrimRight(text.String(), "\n"),
			Structured: structured,
			Meta: map[string]any{
				"queries": queries,
				"counts":  results,
			},
		}, nil
	}
}
