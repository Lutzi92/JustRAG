package rss

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
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

// rssFeedRow is an internal struct with db tags for scanning rss_feeds rows.
type rssFeedRow struct {
	ID                  string     `db:"id"`
	KbID                string     `db:"kb_id"`
	URL                 string     `db:"url"`
	Title               *string    `db:"title"`
	PollInterval        int        `db:"poll_interval"`
	Status              string     `db:"status"`
	ErrorMessage        *string    `db:"error_message"`
	ConsecutiveFailures int        `db:"consecutive_failures"`
	LastPolledAt        *time.Time `db:"last_polled_at"`
	ItemCount           int        `db:"item_count"`
	CreatedAt           time.Time  `db:"created_at"`
}

// toRSSFeedRow converts an internal rssFeedRow to the exported RSSFeedRow.
func toRSSFeedRow(r rssFeedRow) RSSFeedRow {
	return RSSFeedRow(r)
}

// CreateRSSFeed inserts a new RSS feed for the given KB and returns the stored row.
func (s *PGStore) CreateRSSFeed(ctx context.Context, kbID, url string, title *string, pollInterval int) (*RSSFeedRow, error) {
	const sql = `
		INSERT INTO rss_feeds (kb_id, url, title, poll_interval, status)
		VALUES ($1, $2, $3, $4, 'active')
		RETURNING id, kb_id, url, title, poll_interval, status, error_message,
		          consecutive_failures, last_polled_at, item_count, created_at`

	rows, err := pgxutil.QueryRows[rssFeedRow](ctx, s.pool, sql, kbID, url, title, pollInterval)
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
	const sql = `
		SELECT id, kb_id, url, title, poll_interval, status, error_message,
		       consecutive_failures, last_polled_at, item_count, created_at
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
	const sql = `
		SELECT id, kb_id, url, title, poll_interval, status, error_message,
		       consecutive_failures, last_polled_at, item_count, created_at
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

	if updates.PollInterval != nil {
		setClauses = append(setClauses, fmt.Sprintf("poll_interval = $%d", param))
		args = append(args, *updates.PollInterval)
		param++
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

	if len(setClauses) == 0 {
		// Nothing to update — return current row.
		return s.GetRSSFeedByID(ctx, feedID)
	}

	args = append(args, feedID)
	sql := fmt.Sprintf(`
		UPDATE rss_feeds
		SET %s
		WHERE id = $%d
		RETURNING id, kb_id, url, title, poll_interval, status, error_message,
		          consecutive_failures, last_polled_at, item_count, created_at`,
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

// ListActiveRSSFeeds returns all RSS feeds with status "active" across all KBs,
// used by the scheduler to initialize poll schedules on startup.
func (s *PGStore) ListActiveRSSFeeds(ctx context.Context) ([]RSSFeedRow, error) {
	const sql = `
		SELECT id, kb_id, url, title, poll_interval, status, error_message,
		       consecutive_failures, last_polled_at, item_count, created_at
		FROM rss_feeds
		WHERE status = 'active'
		ORDER BY created_at ASC`

	rows, err := pgxutil.QueryRows[rssFeedRow](ctx, s.pool, sql)
	if err != nil {
		return nil, fmt.Errorf("ListActiveRSSFeeds: %w", err)
	}
	result := make([]RSSFeedRow, len(rows))
	for i, r := range rows {
		result[i] = toRSSFeedRow(r)
	}
	return result, nil
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
