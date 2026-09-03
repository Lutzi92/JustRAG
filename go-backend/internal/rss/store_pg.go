package rss

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/syncwindow"
)

// PGStore is a PostgreSQL-backed implementation of the rss RSSStore interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ RSSStore = (*PGStore)(nil)

// rssFeedColumns is the column list shared by every SELECT/RETURNING on
// rss_feeds. Keep it in one place — it used to be duplicated five times.
const rssFeedColumns = `id, kb_id, url, title, sync_schedule, next_sync_at, status, error_message,
		          consecutive_failures, last_polled_at, item_count, fetch_full_text, created_at`

// rssFeedRow is an internal struct with db tags for scanning rss_feeds rows.
type rssFeedRow struct {
	ID                  string     `db:"id"`
	KbID                string     `db:"kb_id"`
	URL                 string     `db:"url"`
	Title               *string    `db:"title"`
	SyncSchedule        string     `db:"sync_schedule"`
	NextSyncAt          *time.Time `db:"next_sync_at"`
	Status              string     `db:"status"`
	ErrorMessage        *string    `db:"error_message"`
	ConsecutiveFailures int        `db:"consecutive_failures"`
	LastPolledAt        *time.Time `db:"last_polled_at"`
	ItemCount           int        `db:"item_count"`
	FetchFullText       bool       `db:"fetch_full_text"`
	CreatedAt           time.Time  `db:"created_at"`
}

// toRSSFeedRow converts an internal rssFeedRow to the exported RSSFeedRow.
func toRSSFeedRow(r rssFeedRow) RSSFeedRow {
	return RSSFeedRow(r)
}

// CreateRSSFeed inserts a new RSS feed for the given KB and returns the stored
// row. syncSchedule is one of syncwindow.Schedule*; next_sync_at is left NULL
// and stamped by the sweeper on its next tick.
func (s *PGStore) CreateRSSFeed(ctx context.Context, kbID, url string, title *string, syncSchedule string, fetchFullText bool) (*RSSFeedRow, error) {
	sql := `
		INSERT INTO rss_feeds (kb_id, url, title, sync_schedule, fetch_full_text, status)
		VALUES ($1, $2, $3, $4, $5, 'active')
		RETURNING ` + rssFeedColumns

	rows, err := pgxutil.QueryRows[rssFeedRow](ctx, s.pool, sql, kbID, url, title, syncSchedule, fetchFullText)
	if err != nil {
		return nil, fmt.Errorf("CreateRSSFeed: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CreateRSSFeed: no row returned")
	}
	r := toRSSFeedRow(rows[0])
	return &r, nil
}

// ListRSSFeeds returns all RSS feeds for kbID ordered by created_at DESC.
func (s *PGStore) ListRSSFeeds(ctx context.Context, kbID string) ([]RSSFeedRow, error) {
	sql := `
		SELECT ` + rssFeedColumns + `
		FROM rss_feeds
		WHERE kb_id = $1
		ORDER BY created_at DESC`

	rows, err := pgxutil.QueryRows[rssFeedRow](ctx, s.pool, sql, kbID)
	if err != nil {
		return nil, fmt.Errorf("ListRSSFeeds: %w", err)
	}

	result := make([]RSSFeedRow, len(rows))
	for i, r := range rows {
		result[i] = toRSSFeedRow(r)
	}
	return result, nil
}

// GetRSSFeedByID returns the RSS feed with the given ID, or nil if not found.
func (s *PGStore) GetRSSFeedByID(ctx context.Context, feedID string) (*RSSFeedRow, error) {
	sql := `
		SELECT ` + rssFeedColumns + `
		FROM rss_feeds
		WHERE id = $1`

	rows, err := pgxutil.QueryRows[rssFeedRow](ctx, s.pool, sql, feedID)
	if err != nil {
		return nil, fmt.Errorf("GetRSSFeedByID: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toRSSFeedRow(rows[0])
	return &r, nil
}

// UpdateRSSFeed applies non-nil fields from updates to the feed identified by feedID.
// Returns nil, nil if the feed does not exist.
func (s *PGStore) UpdateRSSFeed(ctx context.Context, feedID string, updates RSSFeedUpdate) (*RSSFeedRow, error) {
	var setClauses []string
	var args []any
	param := 1

	if updates.SyncSchedule != nil {
		setClauses = append(setClauses, fmt.Sprintf("sync_schedule = $%d", param))
		args = append(args, *updates.SyncSchedule)
		param++
	}
	if updates.NextSyncAt != nil {
		if *updates.NextSyncAt == nil {
			setClauses = append(setClauses, "next_sync_at = NULL")
		} else {
			setClauses = append(setClauses, fmt.Sprintf("next_sync_at = $%d", param))
			args = append(args, **updates.NextSyncAt)
			param++
		}
	}
	if updates.Status != nil {
		setClauses = append(setClauses, fmt.Sprintf("status = $%d", param))
		args = append(args, *updates.Status)
		param++
	}
	if updates.ErrorMessage != nil {
		if *updates.ErrorMessage == "" {
			setClauses = append(setClauses, "error_message = NULL")
		} else {
			setClauses = append(setClauses, fmt.Sprintf("error_message = $%d", param))
			args = append(args, *updates.ErrorMessage)
			param++
		}
	}
	if updates.ConsecutiveFailures != nil {
		setClauses = append(setClauses, fmt.Sprintf("consecutive_failures = $%d", param))
		args = append(args, *updates.ConsecutiveFailures)
		param++
	}
	if updates.FetchFullText != nil {
		setClauses = append(setClauses, fmt.Sprintf("fetch_full_text = $%d", param))
		args = append(args, *updates.FetchFullText)
		param++
	}

	if len(setClauses) == 0 {
		// Nothing to update — return current row.
		return s.GetRSSFeedByID(ctx, feedID)
	}

	args = append(args, feedID)
	sql := fmt.Sprintf(`
		UPDATE rss_feeds
		SET %s
		WHERE id = $%d
		RETURNING `+rssFeedColumns,
		strings.Join(setClauses, ", "), param)

	rows, err := pgxutil.QueryRows[rssFeedRow](ctx, s.pool, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("UpdateRSSFeed: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toRSSFeedRow(rows[0])
	return &r, nil
}

// DeleteRSSFeed deletes the RSS feed with the given ID.
func (s *PGStore) DeleteRSSFeed(ctx context.Context, feedID string) error {
	const sql = `DELETE FROM rss_feeds WHERE id = $1`
	_, err := s.pool.Exec(ctx, sql, feedID)
	if err != nil {
		return fmt.Errorf("DeleteRSSFeed: %w", err)
	}
	return nil
}

// UpdateRSSFeedPollSuccess updates last_polled_at, item_count, and clears error
// fields after a successful poll.
func (s *PGStore) UpdateRSSFeedPollSuccess(ctx context.Context, feedID string, itemCount int) error {
	const sql = `
		UPDATE rss_feeds
		SET last_polled_at = NOW(), item_count = $1, consecutive_failures = 0, error_message = NULL
		WHERE id = $2`
	_, err := s.pool.Exec(ctx, sql, itemCount, feedID)
	if err != nil {
		return fmt.Errorf("UpdateRSSFeedPollSuccess: %w", err)
	}
	return nil
}

// UpdateRSSFeedPollFailure increments consecutive_failures and records the error.
func (s *PGStore) UpdateRSSFeedPollFailure(ctx context.Context, feedID string, errMsg string) error {
	const sql = `
		UPDATE rss_feeds
		SET last_polled_at = NOW(),
		    consecutive_failures = consecutive_failures + 1,
		    error_message = $1
		WHERE id = $2`
	_, err := s.pool.Exec(ctx, sql, errMsg, feedID)
	if err != nil {
		return fmt.Errorf("UpdateRSSFeedPollFailure: %w", err)
	}
	return nil
}

// fileNameRow is used for scanning file names.
type fileNameRow struct {
	Name string `db:"name"`
}

// ListFileNamesByRSSFeedID returns all file names linked to the given RSS feed.
func (s *PGStore) ListFileNamesByRSSFeedID(ctx context.Context, rssFeedID string) (map[string]bool, error) {
	const sql = `SELECT name FROM files WHERE rss_feed_id = $1`
	rows, err := pgxutil.QueryRows[fileNameRow](ctx, s.pool, sql, rssFeedID)
	if err != nil {
		return nil, fmt.Errorf("ListFileNamesByRSSFeedID: %w", err)
	}
	result := make(map[string]bool, len(rows))
	for _, r := range rows {
		result[r.Name] = true
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Sweeper contract (internal/syncsched)
// ---------------------------------------------------------------------------

// Kind identifies this store to the sweeper (internal/syncsched).
func (s *PGStore) Kind() string { return "rss" }

// dueSourceRow scans the two columns the sweeper needs.
type dueSourceRow struct {
	ID       string `db:"id"`
	Schedule string `db:"sync_schedule"`
}

func toDueSources(rows []dueSourceRow) []syncwindow.DueSource {
	out := make([]syncwindow.DueSource, 0, len(rows))
	for _, r := range rows {
		out = append(out, syncwindow.DueSource{ID: r.ID, Schedule: r.Schedule})
	}
	return out
}

// ListDue returns feeds whose stamped slot has arrived. status IN
// ('active','error') deliberately keeps an errored feed on the schedule: a
// sync failure must not permanently drop the source out of ListDue/
// ListUnscheduled, or a single transient failure (a 500, a network blip)
// would silently stop the feed from ever syncing again. 'paused' and
// 'syncing' stay excluded. RSS's poll failure path never actually sets
// status='error' today (see UpdateRSSFeedPollFailure), but the predicate is
// kept in sync with confluence/gitrepo for consistency and in case that
// changes.
func (s *PGStore) ListDue(ctx context.Context, now time.Time) ([]syncwindow.DueSource, error) {
	const sql = `
		SELECT id::text AS id, sync_schedule FROM rss_feeds
		 WHERE sync_schedule <> 'manual'
		   AND status IN ('active', 'error')
		   AND next_sync_at IS NOT NULL
		   AND next_sync_at <= $1`
	rows, err := pgxutil.QueryRows[dueSourceRow](ctx, s.pool, sql, now)
	if err != nil {
		return nil, fmt.Errorf("ListDue(rss): %w", err)
	}
	return toDueSources(rows), nil
}

// ListUnscheduled returns feeds that have a schedule but no stamped slot yet
// (newly created, or newly switched away from manual). The sweeper stamps
// them WITHOUT enqueuing, so enabling a schedule never triggers a daytime sync.
func (s *PGStore) ListUnscheduled(ctx context.Context) ([]syncwindow.DueSource, error) {
	const sql = `
		SELECT id::text AS id, sync_schedule FROM rss_feeds
		 WHERE sync_schedule <> 'manual'
		   AND status IN ('active', 'error')
		   AND next_sync_at IS NULL`
	rows, err := pgxutil.QueryRows[dueSourceRow](ctx, s.pool, sql)
	if err != nil {
		return nil, fmt.Errorf("ListUnscheduled(rss): %w", err)
	}
	return toDueSources(rows), nil
}

// MarkScheduled stamps the next slot. No status predicate here: it addresses
// a single row by primary key (the id came from ListDue/ListUnscheduled,
// which already filtered on status), so re-checking status would only risk a
// silent no-op update if the status changed between list and stamp.
func (s *PGStore) MarkScheduled(ctx context.Context, id string, next time.Time) error {
	const sql = `UPDATE rss_feeds SET next_sync_at = $2 WHERE id = $1`
	_, err := s.pool.Exec(ctx, sql, id, next)
	if err != nil {
		return fmt.Errorf("MarkScheduled(rss, %s): %w", id, err)
	}
	return nil
}
