package chat

import (
	"context"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
)

// FileDates carries the two date columns of a `files` row: CreatedAt is the
// ingest timestamp (never NULL), PublishedAt the document's own publication
// date, which only the RSS poller fills today (W3-R9) and which is nil for
// every other origin. Consumers that need "the" date of a document use
// PublishedAt when set and CreatedAt otherwise — the Go-side twin of the SQL
// COALESCE(published_at, created_at) used by every date-window query.
type FileDates struct {
	CreatedAt   time.Time
	PublishedAt *time.Time
}

// FileDateLookup resolves file ids to their dates in one batch query. The
// production implementation is a thin adapter over the main-DB files store
// (internal/app/routes.go); internal/files must not import internal/chat, so
// the adapter — not the store — speaks this interface.
type FileDateLookup interface {
	FileDatesByIDs(ctx context.Context, ids []string) (map[string]FileDates, error)
}

// WithFileDates attaches the per-turn source-date lookup. When set, every
// answering path enriches its ChatSource slice with the cited files' dates
// right before the sources are emitted and persisted, so the frontend can
// show document freshness on a source card and the stored messages.sources
// JSONB carries the dates too. Optional — without it the date fields are
// simply omitted from the JSON, exactly as before this option existed.
func WithFileDates(l FileDateLookup) HandlerOption {
	return func(h *Handler) {
		h.fileDates = l
	}
}

// EnrichSourceDates is the exported entry point for answering surfaces that
// live outside this package (internal/publicapi). Identical behaviour to the
// package-internal call the web chat paths use — one function so the two
// surfaces cannot drift.
func EnrichSourceDates(ctx context.Context, l FileDateLookup, sources []ChatSource) {
	enrichSourceDates(ctx, l, sources)
}

// FormatSourceDate renders one source date for an API projection that speaks
// JSON strings rather than Go times: RFC 3339 in UTC, or "" when the date is
// absent so the caller's `omitempty` drops the key entirely (W4-R12).
//
// UTC, not the server's local zone: the OpenAI-compat and MCP surfaces are
// consumed by machines across timezones, and two surfaces rendering the same
// instant differently is the kind of drift that only shows up in a client bug
// report. One function so those surfaces cannot diverge.
func FormatSourceDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// enrichSourceDates fills CreatedAt/PublishedAt on the given sources in
// place, using one batch query for the distinct file ids present (W3-R11).
//
// Fail-soft by construction: a nil lookup, an empty id set, or a failing
// query all leave the sources exactly as they were — a freshness badge is
// never worth failing a chat turn over. The error guard also matters for
// correctness, not just for the turn: a partially-populated map returned
// alongside an error would otherwise be written onto the sources as if it
// were authoritative.
func enrichSourceDates(ctx context.Context, l FileDateLookup, sources []ChatSource) {
	if l == nil || len(sources) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(sources))
	ids := make([]string, 0, len(sources))
	for _, s := range sources {
		if s.FileID == "" {
			continue
		}
		if _, dup := seen[s.FileID]; dup {
			continue
		}
		seen[s.FileID] = struct{}{}
		ids = append(ids, s.FileID)
	}
	if len(ids) == 0 {
		return
	}
	dates, err := l.FileDatesByIDs(ctx, ids)
	if err != nil {
		logctx.From(ctx).Warn("chat: source date lookup failed", "error", err, "file_count", len(ids))
		return
	}
	for i := range sources {
		d, ok := dates[sources[i].FileID]
		if !ok {
			continue
		}
		// Both dates are copied per source rather than aliased: two sources
		// citing the same file would otherwise share one *time.Time (the
		// map's), so a later mutation through one of them — a caller
		// normalising a timezone, say — would silently change the other.
		created := d.CreatedAt
		sources[i].CreatedAt = &created
		if d.PublishedAt != nil {
			published := *d.PublishedAt
			sources[i].PublishedAt = &published
		} else {
			sources[i].PublishedAt = nil
		}
	}
}
