package chat

import (
	"context"
	"fmt"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/vector"
)

// newMarkerNameRegex is the Postgres regex ('~*') matching file names that
// carry a "new" status label as its own word ("NEU Citrix …", "[NEU] …").
// Word-boundary anchors keep "Neues Konzept"/"Neustadt" out. Passed to the
// lister so the SQL stays in one place.
const newMarkerNameRegex = `\m(neu|new)\M`

// RecencyDoc is one file surfaced by the recency-listing lookup. Kept as a
// chat-local type (rather than reusing mcp/builtin.RecentDocRow) because
// importing mcp/builtin from chat would create an import cycle — same
// reason ToolDispatcher lives here (see tool_dispatcher.go).
type RecencyDoc struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// RecencyLister backs the recency-listing lookups. RecentDocuments lists
// files added within a date window; DocumentsWithNameMarker lists files
// whose NAME matches a word-boundary regex (CERT-Bund advisories carry
// "NEU"/"UPDATE" as a status label in the title, so "neue Meldungen" can
// target the labeled subset rather than ingest recency). Both newest
// first. Production implementation adapts mcp/builtin.PgxRecentDocsStore
// (app wiring); nil disables the recency-listing path (public API,
// OpenAI-compat, mcpserver callers).
type RecencyLister interface {
	RecentDocuments(ctx context.Context, kbID string, after, before time.Time, limit int) ([]RecencyDoc, error)
	DocumentsWithNameMarker(ctx context.Context, kbID, nameRegex string, limit int) ([]RecencyDoc, error)
}

// recencyListingResult carries what PrepareChatContext needs to build the
// system-prompt addendum after retrieval ran.
type recencyListingResult struct {
	fired     bool
	sinceISO  string   // inclusive window start, ISO date in the configured TZ
	entries   []string // preformatted "name (added YYYY-MM-DD)" lines, newest first
	truncated bool     // listing hit its cap — must be disclosed as incomplete
}

// applyRecencyListing is the deterministic recency path for "what is new /
// recently added" queries (production bug 2026-07-02: "Welche neuen
// Meldungen gibt es?" listed 1 of many new advisories). When the classifier
// fires it (a) scopes retrieval to the recency-relevant files so chunks and
// citations come from them, and (b) fetches the complete file listing,
// which PrepareChatContext injects as a system-prompt addendum: semantic
// top-k cannot hold chunks from every new file, so the listing, not the
// context, is the completeness contract.
//
// Two arms feed the listing:
//   - Window arm: files with created_at in [today − N days, now], N from
//     chat_recency_listing_window_days or an explicit window in the query
//     ("in den letzten 5 Tagen", "von heute"), resolved in
//     chat_date_timezone. Same inclusive-window semantics as the date-line
//     prompt rule.
//   - Name-marker arm (chat_recency_listing_name_match_enabled, only when
//     the query literally says "neu"/"new"): files whose NAME carries that
//     word — the labeled-subset intent. Out-of-window marker matches are
//     annotated in the listing, and retrieval switches from the CreatedAfter
//     window to an explicit FileIDs union so their chunks stay citable.
//     Skipped when the user pre-selected files (Search intersects FileIDs;
//     a union would silently widen the selection) or when the window
//     listing is truncated (the union would then *narrow* retrieval to the
//     capped listing).
//
// Fail-open throughout: a window-lister error reverts to legacy retrieval
// (no window, no addendum) — an empty-but-wrong "nothing new" listing would
// be worse than the old incomplete answer; a marker-lister error keeps the
// window arm.
func applyRecencyListing(
	ctx context.Context,
	siteConfig SiteConfigReader,
	lister RecencyLister,
	kbID, query string,
	opts *vector.SearchOptions,
	now time.Time,
) recencyListingResult {
	if lister == nil || !ChatRecencyListingEnabled(ctx, siteConfig) || !IsRecencyListingQuery(query) {
		return recencyListingResult{}
	}

	loc, err := time.LoadLocation(ChatDateTimezone(ctx, siteConfig))
	if err != nil {
		loc = time.UTC
	}
	nowLoc := now.In(loc)

	days := ChatRecencyListingWindowDays(ctx, siteConfig)
	if n, ok := explicitRecencyWindowDays(query); ok && n <= 365 {
		days = n
	}
	cutoff := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -days)

	limit := ChatRecencyListingMaxResults(ctx, siteConfig)
	rows, err := lister.RecentDocuments(ctx, kbID, cutoff, nowLoc, limit)
	if err != nil {
		logctx.From(ctx).Warn("rag.recency_listing.lister_failed", "error", err.Error())
		return recencyListingResult{}
	}
	truncated := len(rows) >= limit

	// Name-marker arm.
	var markerOnly []RecencyDoc
	if ChatRecencyListingNameMatchEnabled(ctx, siteConfig) && queryMentionsNewMarker(query) {
		markerRows, merr := lister.DocumentsWithNameMarker(ctx, kbID, newMarkerNameRegex, limit)
		if merr != nil {
			logctx.From(ctx).Warn("rag.recency_listing.marker_failed", "error", merr.Error())
		} else {
			inWindow := make(map[string]struct{}, len(rows))
			for _, r := range rows {
				inWindow[r.ID] = struct{}{}
			}
			for _, m := range markerRows {
				if _, ok := inWindow[m.ID]; !ok && m.CreatedAt.Before(cutoff) {
					markerOnly = append(markerOnly, m)
				}
			}
			truncated = truncated || len(markerRows) >= limit
		}
	}

	// Retrieval scope: FileIDs union when out-of-window marker docs must
	// stay citable and it's safe (no user selection to respect, window
	// fully enumerated); otherwise the CreatedAfter window.
	if len(markerOnly) > 0 && len(opts.FileIDs) == 0 && len(rows) < limit {
		ids := make([]string, 0, len(rows)+len(markerOnly))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		for _, m := range markerOnly {
			ids = append(ids, m.ID)
		}
		opts.FileIDs = ids
	} else {
		opts.CreatedAfter = &cutoff
	}

	entries := make([]string, 0, len(rows)+len(markerOnly))
	for _, r := range rows {
		entries = append(entries, fmt.Sprintf("%s (added %s)", r.Name, r.CreatedAt.In(loc).Format("2006-01-02")))
	}
	for _, m := range markerOnly {
		entries = append(entries, fmt.Sprintf("%s (added %s — older than the window; name carries a \"NEU\"/\"new\" label)", m.Name, m.CreatedAt.In(loc).Format("2006-01-02")))
	}

	res := recencyListingResult{
		fired:     true,
		sinceISO:  cutoff.Format("2006-01-02"),
		entries:   entries,
		truncated: truncated,
	}
	logctx.From(ctx).Info("rag.recency_listing.fired",
		"window_days", days,
		"since", res.sinceISO,
		"files_listed", len(entries),
		"name_marker_extra", len(markerOnly),
		"truncated", res.truncated,
	)
	return res
}
