package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/mcp"
)

// recentDocumentsMaxDefault caps the list when the caller doesn't override.
// Kept in sync with chat.ChatDateToolsMaxResults' default; the wiring layer
// may pass a configured cap in a future revision.
const recentDocumentsMaxDefault = 50

// RecentDocRow is one file surfaced by recent_documents. ID is unused by
// the tool's own text rendering but consumed by chat's recency-listing
// path (retrieval scoping by file ID).
type RecentDocRow struct {
	ID        string
	Name      string
	Origin    string
	CreatedAt time.Time
}

// RecentDocsStore lists files added to a KB within a date window. The
// interface keeps the tool testable without a live pool (mirrors
// kb_search's SearchService interface).
type RecentDocsStore interface {
	RecentDocuments(ctx context.Context, kbID string, after, before time.Time, limit int) ([]RecentDocRow, error)
}

// RecentDocumentsArgs documents the tool input. kb_id is injected by the
// dispatcher; the LLM only supplies the date window.
type RecentDocumentsArgs struct {
	DateFrom string `json:"date_from"`
	DateTo   string `json:"date_to,omitempty"`
	KbID     string `json:"kb_id,omitempty"`
}

const recentDocumentsInputSchema = `{
  "type": "object",
  "required": ["date_from"],
  "properties": {
    "date_from": { "type": "string", "description": "Inclusive start date, ISO YYYY-MM-DD. Compute from the current date in the system prompt (e.g. today for \"added today\")." },
    "date_to":   { "type": "string", "description": "Inclusive end date, ISO YYYY-MM-DD. Omit for \"up to now\"." }
  }
}`

// NewRecentDocuments builds the recent_documents tool: list files added to
// the current KB within a date window, newest first. Gated by `enabled`
// (chat_date_tools_enabled); returns a disabled error when off, mirroring
// code_exec / table_query.
// NewRecentDocuments builds the tool. `enabled` gates it
// (chat_date_tools_enabled); `maxResults` resolves the per-invocation result
// cap (chat_date_tools_max_results). Both may be nil — enabled defaults to
// off, maxResults defaults to recentDocumentsMaxDefault.
func NewRecentDocuments(store RecentDocsStore, enabled func(ctx context.Context) bool, maxResults func(ctx context.Context) int) mcp.Tool {
	if enabled == nil {
		enabled = func(context.Context) bool { return false }
	}
	if maxResults == nil {
		maxResults = func(context.Context) int { return recentDocumentsMaxDefault }
	}
	return mcp.Tool{
		Name:        "recent_documents",
		Description: "List documents added to this knowledge base within a date window (newest first). Use for \"what was added today / yesterday / this week / since <date>\". Compute the ISO dates from the current date in the system prompt.",
		InputSchema: json.RawMessage(recentDocumentsInputSchema),
		Handler:     mcp.ToolHandlerFunc(recentDocumentsHandler(store, enabled, maxResults)),
	}
}

func recentDocumentsHandler(store RecentDocsStore, enabled func(ctx context.Context) bool, maxResults func(ctx context.Context) int) mcp.ToolHandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (mcp.ToolResult, error) {
		if !enabled(ctx) {
			return mcp.ToolResult{}, fmt.Errorf("recent_documents: disabled (site_config chat_date_tools_enabled=false)")
		}
		var args RecentDocumentsArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return mcp.ToolResult{}, fmt.Errorf("recent_documents: parse args: %w", err)
		}
		if args.KbID == "" {
			return mcp.ToolResult{}, fmt.Errorf("recent_documents: kb_id was not injected by the orchestrator")
		}
		if args.DateFrom == "" {
			return mcp.ToolResult{}, fmt.Errorf("recent_documents: date_from is required")
		}
		after, err := time.Parse("2006-01-02", args.DateFrom)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("recent_documents: date_from must be ISO YYYY-MM-DD: %w", err)
		}
		// Default upper bound: end of today (open-ended "up to now"). A
		// supplied date_to is treated as inclusive of the whole day.
		before := time.Now()
		if args.DateTo != "" {
			d, perr := time.Parse("2006-01-02", args.DateTo)
			if perr != nil {
				return mcp.ToolResult{}, fmt.Errorf("recent_documents: date_to must be ISO YYYY-MM-DD: %w", perr)
			}
			before = d.Add(24*time.Hour - time.Second)
		}

		limit := maxResults(ctx)
		if limit <= 0 {
			limit = recentDocumentsMaxDefault
		}
		rows, err := store.RecentDocuments(ctx, args.KbID, after, before, limit)
		if err != nil {
			return mcp.ToolResult{}, fmt.Errorf("recent_documents: %w", err)
		}
		if len(rows) == 0 {
			return mcp.ToolResult{Text: "No documents were added in that window."}, nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "%d document(s) added:\n", len(rows))
		for _, r := range rows {
			fmt.Fprintf(&sb, "- %s (%s, added %s)\n", r.Name, r.Origin, r.CreatedAt.Format("2006-01-02"))
		}
		return mcp.ToolResult{
			Text: strings.TrimRight(sb.String(), "\n"),
			Meta: map[string]any{"count": len(rows)},
		}, nil
	}
}

// PgxRecentDocsStore is the production RecentDocsStore backed by the main
// DB pool. Keyed on files.created_at (the effective document date).
type PgxRecentDocsStore struct {
	pool *pgxpool.Pool
}

// NewPgxRecentDocsStore wraps the main DB pool.
func NewPgxRecentDocsStore(pool *pgxpool.Pool) *PgxRecentDocsStore {
	return &PgxRecentDocsStore{pool: pool}
}

// RecentDocuments lists files in kbID within [after, before], newest first.
func (s *PgxRecentDocsStore) RecentDocuments(ctx context.Context, kbID string, after, before time.Time, limit int) ([]RecentDocRow, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("recent_documents: no db pool")
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, name, origin, created_at
		   FROM files
		  WHERE kb_id = $1::uuid AND created_at >= $2 AND created_at <= $3
		  ORDER BY created_at DESC
		  LIMIT $4`,
		kbID, after, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecentDocRows(rows)
}

// NameMarkerDocuments lists files in kbID whose name matches the given
// case-insensitive Postgres regex (word-boundary anchors expected, e.g.
// `\m(neu|new)\M`), newest first. Backs the recency-listing name-marker
// arm in chat: corpora like CERT-Bund advisories carry "NEU"/"UPDATE" as
// a status label in the title, so "neue Meldungen" can target the labeled
// subset regardless of ingest recency.
func (s *PgxRecentDocsStore) NameMarkerDocuments(ctx context.Context, kbID, nameRegex string, limit int) ([]RecentDocRow, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("recent_documents: no db pool")
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, name, origin, created_at
		   FROM files
		  WHERE kb_id = $1::uuid AND name ~* $2
		  ORDER BY created_at DESC
		  LIMIT $3`,
		kbID, nameRegex, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecentDocRows(rows)
}

func scanRecentDocRows(rows pgx.Rows) ([]RecentDocRow, error) {
	var out []RecentDocRow
	for rows.Next() {
		var r RecentDocRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Origin, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
