// Package recencylister adapts internal/mcp/builtin's pgx-backed recent
// documents store to chat.RecencyLister.
//
// It exists as its own package (rather than living in internal/chat, where
// the recency-listing mechanism itself is implemented) because internal/chat
// cannot import internal/mcp/builtin — that would create an import cycle
// (see internal/chat/recency_listing.go:19-21, and tool_dispatcher.go for
// the same reason chat keeps its own RecencyDoc type instead of reusing
// builtin.RecentDocRow). recencylister sits above both packages and depends
// on neither depending back on it.
package recencylister

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/mcp/builtin"
)

// docsStore is the subset of *builtin.PgxRecentDocsStore this adapter
// needs. Declared as an interface (rather than depending on the concrete
// type directly) so a test can substitute a fake without a live pool.
type docsStore interface {
	RecentDocuments(ctx context.Context, kbID string, after, before time.Time, limit int) ([]builtin.RecentDocRow, error)
	NameMarkerDocuments(ctx context.Context, kbID, nameRegex string, limit int) ([]builtin.RecentDocRow, error)
}

// adapter implements chat.RecencyLister on top of a docsStore, converting
// builtin.RecentDocRow to chat.RecencyDoc (the chat-local type recency
// listing works with).
type adapter struct {
	store docsStore
}

// New wraps pool in a chat.RecencyLister backed by
// builtin.NewPgxRecentDocsStore. Production wiring (internal/app/routes.go)
// and cmd/eval both call this to attach the deterministic recency-listing
// path to the standard PrepareChatContext pipeline.
func New(pool *pgxpool.Pool) chat.RecencyLister {
	return &adapter{store: builtin.NewPgxRecentDocsStore(pool)}
}

func (a *adapter) RecentDocuments(ctx context.Context, kbID string, after, before time.Time, limit int) ([]chat.RecencyDoc, error) {
	rows, err := a.store.RecentDocuments(ctx, kbID, after, before, limit)
	if err != nil {
		return nil, err
	}
	return toRecencyDocs(rows), nil
}

func (a *adapter) DocumentsWithNameMarker(ctx context.Context, kbID, nameRegex string, limit int) ([]chat.RecencyDoc, error) {
	rows, err := a.store.NameMarkerDocuments(ctx, kbID, nameRegex, limit)
	if err != nil {
		return nil, err
	}
	return toRecencyDocs(rows), nil
}

func toRecencyDocs(rows []builtin.RecentDocRow) []chat.RecencyDoc {
	out := make([]chat.RecencyDoc, len(rows))
	for i, r := range rows {
		out[i] = chat.RecencyDoc{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt}
	}
	return out
}
