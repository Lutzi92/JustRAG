package confluence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of the confluence ConfluenceStore interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ ConfluenceStore = (*PGStore)(nil)

// ---------------------------------------------------------------------------
// Connection scan structs & helpers
// ---------------------------------------------------------------------------

// confluenceConnectionRow is an internal struct with db tags for scanning
// confluence_connections rows.
type confluenceConnectionRow struct {
	ID             string     `db:"id"`
	UserID         string     `db:"user_id"`
	DisplayName    *string    `db:"display_name"`
	Token          string     `db:"token"`
	Status         string     `db:"status"`
	ErrorMessage   *string    `db:"error_message"`
	LastVerifiedAt *time.Time `db:"last_verified_at"`
	CreatedAt      time.Time  `db:"created_at"`
}

// toConfluenceConnectionRow converts an internal row to the exported type.
func toConfluenceConnectionRow(r confluenceConnectionRow) ConfluenceConnectionRow {
	return ConfluenceConnectionRow(r)
}

// ---------------------------------------------------------------------------
// Source scan structs & helpers
// ---------------------------------------------------------------------------

// confluenceSourceRow is an internal struct with db tags for scanning
// confluence_sources rows.
type confluenceSourceRow struct {
	ID                  string     `db:"id"`
	KbID                string     `db:"kb_id"`
	ConnectionID        string     `db:"connection_id"`
	SpaceKey            string     `db:"space_key"`
	RootPageID          *string    `db:"root_page_id"`
	RootPageTitle       *string    `db:"root_page_title"`
	IncludeAttachments  bool       `db:"include_attachments"`
	SyncInterval        *int       `db:"sync_interval"`
	Status              string     `db:"status"`
	ErrorMessage        *string    `db:"error_message"`
	ConsecutiveFailures int        `db:"consecutive_failures"`
	LastSyncedAt        *time.Time `db:"last_synced_at"`
	PageCount           int        `db:"page_count"`
	SyncProgress        int        `db:"sync_progress"`
	SyncTotal           int        `db:"sync_total"`
	CreatedAt           time.Time  `db:"created_at"`
}

// toConfluenceSourceRow converts an internal row to the exported type.
func toConfluenceSourceRow(r confluenceSourceRow) ConfluenceSourceRow {
	return ConfluenceSourceRow(r)
}

const confluenceSourceColumns = `
	id, kb_id, connection_id, space_key, root_page_id, root_page_title,
	include_attachments, sync_interval, status, error_message,
	consecutive_failures, last_synced_at, page_count, sync_progress, sync_total, created_at`

// ---------------------------------------------------------------------------
// Connection CRUD
// ---------------------------------------------------------------------------

// GetConfluenceConnectionByUserID returns the Confluence connection for the
// given user, or nil if none exists.
func (s *PGStore) GetConfluenceConnectionByUserID(ctx context.Context, userID string) (*ConfluenceConnectionRow, error) {
	const sql = `
		SELECT id, user_id, display_name, token, status, error_message,
		       last_verified_at, created_at
		FROM confluence_connections
		WHERE user_id = $1`

	rows, err := pgxutil.QueryRows[confluenceConnectionRow](ctx, s.pool, sql, userID)
	if err != nil {
		return nil, fmt.Errorf("GetConfluenceConnectionByUserID: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toConfluenceConnectionRow(rows[0])
	return &r, nil
}

// DeleteConfluenceConnection deletes the Confluence connection with the given ID.
func (s *PGStore) DeleteConfluenceConnection(ctx context.Context, id string) error {
	const sql = `DELETE FROM confluence_connections WHERE id = $1`
	_, err := s.pool.Exec(ctx, sql, id)
	if err != nil {
		return fmt.Errorf("DeleteConfluenceConnection: %w", err)
	}
	return nil
}

// CreateConfluenceConnection inserts a new Confluence connection and returns the
// stored row.
func (s *PGStore) CreateConfluenceConnection(ctx context.Context, userID, encryptedToken string, displayName *string) (*ConfluenceConnectionRow, error) {
	const sql = `
		INSERT INTO confluence_connections (user_id, token, display_name)
		VALUES ($1, $2, $3)
		RETURNING id, user_id, display_name, token, status, error_message,
		          last_verified_at, created_at`

	rows, err := pgxutil.QueryRows[confluenceConnectionRow](ctx, s.pool, sql, userID, encryptedToken, displayName)
	if err != nil {
		return nil, fmt.Errorf("CreateConfluenceConnection: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CreateConfluenceConnection: no row returned")
	}
	r := toConfluenceConnectionRow(rows[0])
	return &r, nil
}

// UpdateConfluenceConnection applies the non-nil fields in updates to the given
// connection and returns the updated row.
func (s *PGStore) UpdateConfluenceConnection(ctx context.Context, id string, updates ConfluenceConnectionUpdate) (*ConfluenceConnectionRow, error) {
	setClauses := []string{}
	args := []any{}
	argIdx := 1

	if updates.Token != nil {
		setClauses = append(setClauses, fmt.Sprintf("token = $%d", argIdx))
		args = append(args, *updates.Token)
		argIdx++
	}
	if updates.DisplayName != nil {
		setClauses = append(setClauses, fmt.Sprintf("display_name = $%d", argIdx))
		args = append(args, *updates.DisplayName)
		argIdx++
	}
	if updates.Status != nil {
		setClauses = append(setClauses, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, *updates.Status)
		argIdx++
	}
	if updates.ErrorMessage != nil {
		setClauses = append(setClauses, fmt.Sprintf("error_message = $%d", argIdx))
		args = append(args, *updates.ErrorMessage)
		argIdx++
	}
	if updates.LastVerifiedAt != nil {
		setClauses = append(setClauses, fmt.Sprintf("last_verified_at = $%d", argIdx))
		args = append(args, *updates.LastVerifiedAt)
		argIdx++
	}

	if len(setClauses) == 0 {
		// Nothing to update — return the current row.
		rows, err := pgxutil.QueryRows[confluenceConnectionRow](ctx, s.pool,
			`SELECT id, user_id, display_name, token, status, error_message,
			        last_verified_at, created_at
			 FROM confluence_connections WHERE id = $1`, id)
		if err != nil {
			return nil, fmt.Errorf("UpdateConfluenceConnection: %w", err)
		}
		if len(rows) == 0 {
			return nil, nil
		}
		r := toConfluenceConnectionRow(rows[0])
		return &r, nil
	}

	args = append(args, id)
	sql := fmt.Sprintf(`
		UPDATE confluence_connections
		SET %s
		WHERE id = $%d
		RETURNING id, user_id, display_name, token, status, error_message,
		          last_verified_at, created_at`,
		strings.Join(setClauses, ", "), argIdx)

	rows, err := pgxutil.QueryRows[confluenceConnectionRow](ctx, s.pool, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("UpdateConfluenceConnection: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toConfluenceConnectionRow(rows[0])
	return &r, nil
}

// ---------------------------------------------------------------------------
// Source CRUD
// ---------------------------------------------------------------------------

// CreateConfluenceSource inserts a new Confluence source and returns the stored row.
func (s *PGStore) CreateConfluenceSource(ctx context.Context, kbID, connectionID, spaceKey string, rootPageID, rootPageTitle *string, includeAttachments bool, syncInterval *int) (*ConfluenceSourceRow, error) {
	const sql = `
		INSERT INTO confluence_sources
		  (kb_id, connection_id, space_key, root_page_id, root_page_title,
		   include_attachments, sync_interval, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'active')
		RETURNING ` + confluenceSourceColumns

	rows, err := pgxutil.QueryRows[confluenceSourceRow](ctx, s.pool, sql,
		kbID, connectionID, spaceKey, rootPageID, rootPageTitle, includeAttachments, syncInterval)
	if err != nil {
		return nil, fmt.Errorf("CreateConfluenceSource: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CreateConfluenceSource: no row returned")
	}
	r := toConfluenceSourceRow(rows[0])
	return &r, nil
}

// ListConfluenceSources returns all sources for the given KB ordered by
// created_at DESC.
func (s *PGStore) ListConfluenceSources(ctx context.Context, kbID string) ([]ConfluenceSourceRow, error) {
	const sql = `
		SELECT ` + confluenceSourceColumns + `
		FROM confluence_sources
		WHERE kb_id = $1
		ORDER BY created_at DESC`

	rows, err := pgxutil.QueryRows[confluenceSourceRow](ctx, s.pool, sql, kbID)
	if err != nil {
		return nil, fmt.Errorf("ListConfluenceSources: %w", err)
	}

	result := make([]ConfluenceSourceRow, len(rows))
	for i, r := range rows {
		result[i] = toConfluenceSourceRow(r)
	}
	return result, nil
}

// GetConfluenceSourceByID returns the Confluence source with the given ID, or
// nil if not found.
func (s *PGStore) GetConfluenceSourceByID(ctx context.Context, sourceID string) (*ConfluenceSourceRow, error) {
	const sql = `
		SELECT ` + confluenceSourceColumns + `
		FROM confluence_sources
		WHERE id = $1`

	rows, err := pgxutil.QueryRows[confluenceSourceRow](ctx, s.pool, sql, sourceID)
	if err != nil {
		return nil, fmt.Errorf("GetConfluenceSourceByID: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toConfluenceSourceRow(rows[0])
	return &r, nil
}

// UpdateConfluenceSource applies the non-nil fields in updates to the given
// source and returns the updated row.
func (s *PGStore) UpdateConfluenceSource(ctx context.Context, sourceID string, updates ConfluenceSourceUpdate) (*ConfluenceSourceRow, error) {
	setClauses := []string{}
	args := []any{}
	param := 1

	if updates.SpaceKey != nil {
		setClauses = append(setClauses, fmt.Sprintf("space_key = $%d", param))
		args = append(args, *updates.SpaceKey)
		param++
	}
	if updates.RootPageID != nil {
		if *updates.RootPageID == "" {
			setClauses = append(setClauses, "root_page_id = NULL")
		} else {
			setClauses = append(setClauses, fmt.Sprintf("root_page_id = $%d", param))
			args = append(args, *updates.RootPageID)
			param++
		}
	}
	if updates.RootPageTitle != nil {
		if *updates.RootPageTitle == "" {
			setClauses = append(setClauses, "root_page_title = NULL")
		} else {
			setClauses = append(setClauses, fmt.Sprintf("root_page_title = $%d", param))
			args = append(args, *updates.RootPageTitle)
			param++
		}
	}
	if updates.IncludeAttachments != nil {
		setClauses = append(setClauses, fmt.Sprintf("include_attachments = $%d", param))
		args = append(args, *updates.IncludeAttachments)
		param++
	}
	if updates.SyncInterval != nil {
		setClauses = append(setClauses, fmt.Sprintf("sync_interval = $%d", param))
		args = append(args, *updates.SyncInterval)
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
	if updates.PageCount != nil {
		setClauses = append(setClauses, fmt.Sprintf("page_count = $%d", param))
		args = append(args, *updates.PageCount)
		param++
	}
	if updates.SyncProgress != nil {
		setClauses = append(setClauses, fmt.Sprintf("sync_progress = $%d", param))
		args = append(args, *updates.SyncProgress)
		param++
	}
	if updates.SyncTotal != nil {
		setClauses = append(setClauses, fmt.Sprintf("sync_total = $%d", param))
		args = append(args, *updates.SyncTotal)
		param++
	}
	if updates.LastSyncedAt != nil {
		setClauses = append(setClauses, fmt.Sprintf("last_synced_at = $%d", param))
		args = append(args, *updates.LastSyncedAt)
		param++
	}

	if len(setClauses) == 0 {
		// Nothing to update — return current row.
		return s.GetConfluenceSourceByID(ctx, sourceID)
	}

	args = append(args, sourceID)
	sql := fmt.Sprintf(`
		UPDATE confluence_sources
		SET %s
		WHERE id = $%d
		RETURNING `+confluenceSourceColumns,
		strings.Join(setClauses, ", "), param)

	rows, err := pgxutil.QueryRows[confluenceSourceRow](ctx, s.pool, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("UpdateConfluenceSource: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toConfluenceSourceRow(rows[0])
	return &r, nil
}

// DeleteConfluenceSource deletes the Confluence source with the given ID.
func (s *PGStore) DeleteConfluenceSource(ctx context.Context, sourceID string) error {
	const sql = `DELETE FROM confluence_sources WHERE id = $1`
	_, err := s.pool.Exec(ctx, sql, sourceID)
	if err != nil {
		return fmt.Errorf("DeleteConfluenceSource: %w", err)
	}
	return nil
}

// ListActiveConfluenceSources returns all Confluence sources with status "active"
// that have a sync interval set, across all KBs. Used by the scheduler to
// initialize sync schedules on startup.
func (s *PGStore) ListActiveConfluenceSources(ctx context.Context) ([]ConfluenceSourceRow, error) {
	const sql = `
		SELECT id, kb_id, connection_id, space_key, root_page_id, root_page_title,
		       include_attachments, sync_interval, status, error_message,
		       consecutive_failures, last_synced_at, page_count, sync_progress, sync_total, created_at
		FROM confluence_sources
		WHERE status = 'active' AND sync_interval IS NOT NULL
		ORDER BY created_at ASC`

	rows, err := pgxutil.QueryRows[confluenceSourceRow](ctx, s.pool, sql)
	if err != nil {
		return nil, fmt.Errorf("ListActiveConfluenceSources: %w", err)
	}
	result := make([]ConfluenceSourceRow, len(rows))
	for i, r := range rows {
		result[i] = toConfluenceSourceRow(r)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// GetConfluenceConnectionByID
// ---------------------------------------------------------------------------

// GetConfluenceConnectionByID returns the Confluence connection with the given
// ID, or nil if not found.
func (s *PGStore) GetConfluenceConnectionByID(ctx context.Context, id string) (*ConfluenceConnectionRow, error) {
	const sql = `
		SELECT id, user_id, display_name, token, status, error_message,
		       last_verified_at, created_at
		FROM confluence_connections
		WHERE id = $1`

	rows, err := pgxutil.QueryRows[confluenceConnectionRow](ctx, s.pool, sql, id)
	if err != nil {
		return nil, fmt.Errorf("GetConfluenceConnectionByID: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toConfluenceConnectionRow(rows[0])
	return &r, nil
}

// ---------------------------------------------------------------------------
// Confluence file operations
// ---------------------------------------------------------------------------

// confluenceFileDBRow is an internal struct for scanning confluence file rows.
type confluenceFileDBRow struct {
	ID                 string    `db:"id"`
	KbID               string    `db:"kb_id"`
	Name               string    `db:"name"`
	Type               string    `db:"type"`
	Size               *int      `db:"size"`
	Status             string    `db:"status"`
	Origin             string    `db:"origin"`
	StoragePath        *string   `db:"storage_path"`
	ConfluenceSourceID *string   `db:"confluence_source_id"`
	ConfluencePageID   *string   `db:"confluence_page_id"`
	CreatedAt          time.Time `db:"created_at"`
}

func toConfluenceFileRow(r confluenceFileDBRow) ConfluenceFileRow {
	return ConfluenceFileRow(r)
}

// CreateConfluenceFile inserts a new file record with confluence_source_id and
// confluence_page_id set.
func (s *PGStore) CreateConfluenceFile(ctx context.Context, data CreateConfluenceFileData) (*ConfluenceFileRow, error) {
	const sql = `
		INSERT INTO files (kb_id, name, type, size, status, origin, storage_path,
		                   confluence_source_id, confluence_page_id)
		VALUES ($1, $2, $3, $4, 'pending', $5, $6, $7, $8)
		RETURNING id, kb_id, name, type, size, status, origin, storage_path,
		          confluence_source_id, confluence_page_id, created_at`

	rows, err := pgxutil.QueryRows[confluenceFileDBRow](ctx, s.pool, sql,
		data.KbID, data.Name, data.Type, data.Size, data.Origin, data.StoragePath,
		data.ConfluenceSourceID, data.ConfluencePageID,
	)
	if err != nil {
		return nil, fmt.Errorf("CreateConfluenceFile: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CreateConfluenceFile: no row returned")
	}
	r := toConfluenceFileRow(rows[0])
	return &r, nil
}

// GetConfluenceSourceIDForFile returns the confluence_source_id for a file,
// or empty string if the file is not linked to a confluence source.
func (s *PGStore) GetConfluenceSourceIDForFile(ctx context.Context, fileID string) (string, error) {
	const sql = `SELECT confluence_source_id FROM files WHERE id = $1 AND confluence_source_id IS NOT NULL`
	var sourceID string
	err := s.pool.QueryRow(ctx, sql, fileID).Scan(&sourceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("GetConfluenceSourceIDForFile: %w", err)
	}
	return sourceID, nil
}

// GetFilesByConfluenceSourceID returns all files linked to the given Confluence
// source, ordered by created_at ASC.
func (s *PGStore) GetFilesByConfluenceSourceID(ctx context.Context, sourceID string) ([]ConfluenceFileRow, error) {
	const sql = `
		SELECT id, kb_id, name, type, size, status, origin, storage_path,
		       confluence_source_id, confluence_page_id, created_at
		FROM files
		WHERE confluence_source_id = $1
		ORDER BY created_at ASC`

	rows, err := pgxutil.QueryRows[confluenceFileDBRow](ctx, s.pool, sql, sourceID)
	if err != nil {
		return nil, fmt.Errorf("GetFilesByConfluenceSourceID: %w", err)
	}
	result := make([]ConfluenceFileRow, len(rows))
	for i, r := range rows {
		result[i] = toConfluenceFileRow(r)
	}
	return result, nil
}

// DeleteFilesByIDs deletes all file records with the given IDs in a single
// batch DELETE.
func (s *PGStore) DeleteFilesByIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM files WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return fmt.Errorf("DeleteFilesByIDs: %w", err)
	}
	return nil
}

// GetConfluenceSourceFileProgress returns the total number of files for a
// confluence source and how many are done (status not pending/processing).
func (s *PGStore) GetConfluenceSourceFileProgress(ctx context.Context, sourceID string) (total, done int, err error) {
	const sql = `
		SELECT
			COUNT(*)::int AS total,
			COUNT(*) FILTER (WHERE status NOT IN ('pending', 'processing'))::int AS done
		FROM files
		WHERE confluence_source_id = $1`

	type progressRow struct {
		Total int `db:"total"`
		Done  int `db:"done"`
	}
	rows, err := pgxutil.QueryRows[progressRow](ctx, s.pool, sql, sourceID)
	if err != nil {
		return 0, 0, fmt.Errorf("GetConfluenceSourceFileProgress: %w", err)
	}
	if len(rows) == 0 {
		return 0, 0, nil
	}
	return rows[0].Total, rows[0].Done, nil
}

// ---------------------------------------------------------------------------
// GetSiteConfigValue (required by confluence.ConfluenceStore interface)
// ---------------------------------------------------------------------------

// GetSiteConfigValue returns the value for a single site_configs key, or nil
// if the key does not exist.
func (s *PGStore) GetSiteConfigValue(ctx context.Context, key string) (*string, error) {
	const sql = `SELECT value FROM site_configs WHERE key = $1`

	var value *string
	err := s.pool.QueryRow(ctx, sql, key).Scan(&value)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("GetSiteConfigValue: %w", err)
	}
	return value, nil
}
