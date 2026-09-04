package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/syncwindow"
)

const gitRepoSourceColumns = `
	id, kb_id, repo_url, is_private, access_token_encrypted, branch,
	sync_schedule, next_sync_at, status,
	error_message, consecutive_failures, last_synced_at, last_commit_sha,
	file_count, sync_progress, sync_total, created_at`

type GitRepoSourceRow struct {
	ID                   string
	KbID                 string
	RepoURL              string
	IsPrivate            bool
	AccessTokenEncrypted *string
	Branch               *string
	SyncSchedule         string
	NextSyncAt           *time.Time
	Status               string
	ErrorMessage         *string
	ConsecutiveFailures  int
	LastSyncedAt         *time.Time
	LastCommitSHA        *string
	FileCount            int
	SyncProgress         int
	SyncTotal            int
	CreatedAt            time.Time
}

type gitRepoSourceDBRow struct {
	ID                   string     `db:"id"`
	KbID                 string     `db:"kb_id"`
	RepoURL              string     `db:"repo_url"`
	IsPrivate            bool       `db:"is_private"`
	AccessTokenEncrypted *string    `db:"access_token_encrypted"`
	Branch               *string    `db:"branch"`
	SyncSchedule         string     `db:"sync_schedule"`
	NextSyncAt           *time.Time `db:"next_sync_at"`
	Status               string     `db:"status"`
	ErrorMessage         *string    `db:"error_message"`
	ConsecutiveFailures  int        `db:"consecutive_failures"`
	LastSyncedAt         *time.Time `db:"last_synced_at"`
	LastCommitSHA        *string    `db:"last_commit_sha"`
	FileCount            int        `db:"file_count"`
	SyncProgress         int        `db:"sync_progress"`
	SyncTotal            int        `db:"sync_total"`
	CreatedAt            time.Time  `db:"created_at"`
}

func toGitRepoSourceRow(r gitRepoSourceDBRow) GitRepoSourceRow {
	return GitRepoSourceRow(r) // identical field order/types; if vet complains, map explicitly
}

type CreateGitRepoSourceInput struct {
	KbID                 string
	RepoURL              string
	IsPrivate            bool
	AccessTokenEncrypted *string // nil for public
	Branch               *string // nil => default HEAD
	SyncSchedule         string  // one of syncwindow.Schedule*
}

// GitRepoSourceUpdate carries optional fields for a PATCH update. NextSyncAt
// is a double pointer so "leave untouched" (nil) and "set to NULL" (non-nil
// pointing at a nil *time.Time) are both expressible.
type GitRepoSourceUpdate struct {
	SyncSchedule *string
	NextSyncAt   **time.Time
	Status       *string // "active" | "paused"
}

type SyncState struct {
	Status              string
	ErrorMessage        *string
	LastCommitSHA       *string
	LastSyncedAt        *time.Time
	FileCount           *int
	SyncProgress        *int
	SyncTotal           *int
	ConsecutiveFailures *int
}

type GitRepoFileRow struct {
	FileID      string `db:"file_id"`
	Path        string `db:"git_file_path"`
	BlobSHA     string `db:"git_blob_sha"`
	StoragePath string `db:"storage_path"`
}

type CreateGitRepoFileInput struct {
	KbID            string
	Name            string // repo-relative path (used as display name)
	Type            string // mime type, e.g. "text/markdown" or "text/plain"
	Size            int
	StoragePath     string
	GitRepoSourceID string
	GitFilePath     string
	GitBlobSHA      string
}

type Store interface {
	CreateGitRepoSource(ctx context.Context, in CreateGitRepoSourceInput) (*GitRepoSourceRow, error)
	ListGitRepoSources(ctx context.Context, kbID string) ([]GitRepoSourceRow, error)
	GetGitRepoSourceByID(ctx context.Context, id string) (*GitRepoSourceRow, error)
	UpdateGitRepoSource(ctx context.Context, id string, upd GitRepoSourceUpdate) error
	DeleteGitRepoSource(ctx context.Context, id string) error
	SetGitRepoSyncState(ctx context.Context, id string, st SyncState) error
	ListGitRepoFiles(ctx context.Context, sourceID string) ([]GitRepoFileRow, error)
	CreateGitRepoFile(ctx context.Context, in CreateGitRepoFileInput) (string, error)
	DeleteGitRepoFileByID(ctx context.Context, fileID string) error
	GetGitRepoSourceFileProgress(ctx context.Context, sourceID string) (total, done int, err error)
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// TableDropper drops a file's materialised spreadsheet tables (the
// `tabular.sheet_*` tables), its tabular_column_values rows and its
// tabular_catalog rows. Satisfied by *tabular.Materializer. See the
// identical interface documented at internal/files.TableDropper for the
// full rationale: the catalog row is the only index from a file to its
// physical tables, so this MUST run before the files row is deleted.
//
// Optional: a nil dropper (the NewStore default) leaves the tables alone,
// which is what a text-only repository (or any caller that never wires
// SetTableDropper) gets — cheap and correct for the common case.
type TableDropper interface {
	DropTablesForFile(ctx context.Context, fileID string) error
}

type PGStore struct {
	pool         *pgxpool.Pool
	tableDropper TableDropper
}

func NewStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// SetTableDropper injects the spreadsheet table cleanup hook for
// DeleteGitRepoFileByID. Optional — nil (the default) leaves materialised
// tables in place.
func (s *PGStore) SetTableDropper(d TableDropper) { s.tableDropper = d }

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// CreateGitRepoSource inserts a new git repo source and returns the stored
// row. SyncSchedule is one of syncwindow.Schedule*; next_sync_at is left
// NULL and stamped by the sweeper on its next tick.
func (s *PGStore) CreateGitRepoSource(ctx context.Context, in CreateGitRepoSourceInput) (*GitRepoSourceRow, error) {
	const q = `
		INSERT INTO git_repo_sources (kb_id, repo_url, is_private, access_token_encrypted, branch, sync_schedule, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'active')
		RETURNING ` + gitRepoSourceColumns
	rows, err := pgxutil.QueryRows[gitRepoSourceDBRow](ctx, s.pool, q,
		in.KbID, in.RepoURL, in.IsPrivate, in.AccessTokenEncrypted, in.Branch, in.SyncSchedule)
	if err != nil {
		return nil, fmt.Errorf("CreateGitRepoSource: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CreateGitRepoSource: no row returned")
	}
	r := toGitRepoSourceRow(rows[0])
	return &r, nil
}

func (s *PGStore) ListGitRepoSources(ctx context.Context, kbID string) ([]GitRepoSourceRow, error) {
	const q = `SELECT ` + gitRepoSourceColumns + ` FROM git_repo_sources WHERE kb_id = $1 ORDER BY created_at DESC`
	rows, err := pgxutil.QueryRows[gitRepoSourceDBRow](ctx, s.pool, q, kbID)
	if err != nil {
		return nil, fmt.Errorf("ListGitRepoSources: %w", err)
	}
	out := make([]GitRepoSourceRow, len(rows))
	for i, r := range rows {
		out[i] = toGitRepoSourceRow(r)
	}
	return out, nil
}

func (s *PGStore) GetGitRepoSourceByID(ctx context.Context, id string) (*GitRepoSourceRow, error) {
	const q = `SELECT ` + gitRepoSourceColumns + ` FROM git_repo_sources WHERE id = $1`
	rows, err := pgxutil.QueryRows[gitRepoSourceDBRow](ctx, s.pool, q, id)
	if err != nil {
		return nil, fmt.Errorf("GetGitRepoSourceByID: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toGitRepoSourceRow(rows[0])
	return &r, nil
}

func (s *PGStore) UpdateGitRepoSource(ctx context.Context, id string, upd GitRepoSourceUpdate) error {
	var setClauses []string
	var args []any
	param := 1

	if upd.SyncSchedule != nil {
		setClauses = append(setClauses, fmt.Sprintf("sync_schedule = $%d", param))
		args = append(args, *upd.SyncSchedule)
		param++
	}
	if upd.NextSyncAt != nil {
		if *upd.NextSyncAt == nil {
			setClauses = append(setClauses, "next_sync_at = NULL")
		} else {
			setClauses = append(setClauses, fmt.Sprintf("next_sync_at = $%d", param))
			args = append(args, **upd.NextSyncAt)
			param++
		}
	}
	if upd.Status != nil {
		setClauses = append(setClauses, fmt.Sprintf("status = $%d", param))
		args = append(args, *upd.Status)
		param++
	}

	if len(setClauses) == 0 {
		return nil // Nothing to update.
	}

	args = append(args, id)
	q := fmt.Sprintf(`UPDATE git_repo_sources SET %s WHERE id = $%d`, strings.Join(setClauses, ", "), param)
	_, err := s.pool.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("UpdateGitRepoSource: %w", err)
	}
	return nil
}

func (s *PGStore) DeleteGitRepoSource(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM git_repo_sources WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("DeleteGitRepoSource: %w", err)
	}
	return nil
}

func (s *PGStore) SetGitRepoSyncState(ctx context.Context, id string, st SyncState) error {
	const q = `
		UPDATE git_repo_sources SET
			status = $2,
			error_message = $3,
			last_commit_sha = COALESCE($4, last_commit_sha),
			last_synced_at = COALESCE($5, last_synced_at),
			file_count = COALESCE($6, file_count),
			sync_progress = COALESCE($7, sync_progress),
			sync_total = COALESCE($8, sync_total),
			consecutive_failures = COALESCE($9, consecutive_failures)
		WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, id, st.Status, st.ErrorMessage, st.LastCommitSHA,
		st.LastSyncedAt, st.FileCount, st.SyncProgress, st.SyncTotal, st.ConsecutiveFailures)
	if err != nil {
		return fmt.Errorf("SetGitRepoSyncState: %w", err)
	}
	return nil
}

func (s *PGStore) ListGitRepoFiles(ctx context.Context, sourceID string) ([]GitRepoFileRow, error) {
	const q = `
		SELECT id AS file_id, COALESCE(git_file_path,'') AS git_file_path,
		       COALESCE(git_blob_sha,'') AS git_blob_sha, COALESCE(storage_path,'') AS storage_path
		FROM files WHERE git_repo_source_id = $1`
	rows, err := pgxutil.QueryRows[GitRepoFileRow](ctx, s.pool, q, sourceID)
	if err != nil {
		return nil, fmt.Errorf("ListGitRepoFiles: %w", err)
	}
	return rows, nil
}

func (s *PGStore) CreateGitRepoFile(ctx context.Context, in CreateGitRepoFileInput) (string, error) {
	const q = `
		INSERT INTO files (kb_id, name, type, size, status, origin, storage_path,
		                   git_repo_source_id, git_file_path, git_blob_sha)
		VALUES ($1, $2, $3, $4, 'pending', 'git', $5, $6, $7, $8)
		RETURNING id`
	type idRow struct {
		ID string `db:"id"`
	}
	rows, err := pgxutil.QueryRows[idRow](ctx, s.pool, q,
		in.KbID, in.Name, in.Type, in.Size, in.StoragePath, in.GitRepoSourceID, in.GitFilePath, in.GitBlobSHA)
	if err != nil {
		return "", fmt.Errorf("CreateGitRepoFile: %w", err)
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("CreateGitRepoFile: no row returned")
	}
	return rows[0].ID, nil
}

// dropTablesFor drops each id's materialised spreadsheet tables via the
// injected TableDropper. Nil-safe: returns immediately (never touching the
// pool) when no dropper is wired, so this costs nothing for a deployment
// without a main pool or a caller that only ever handles text files.
// Best effort per id: a failure is logged and the rest still run.
func (s *PGStore) dropTablesFor(ctx context.Context, ids []string) {
	if s.tableDropper == nil {
		return
	}
	for _, id := range ids {
		if err := s.tableDropper.DropTablesForFile(ctx, id); err != nil {
			slog.Warn("tabular: drop tables for deleted git repo file failed",
				"fileId", id, "error", err)
		}
	}
}

func (s *PGStore) DeleteGitRepoFileByID(ctx context.Context, fileID string) error {
	// Drop any materialised spreadsheet tables BEFORE the files row goes
	// away: tabular_catalog is the only index from a file to its physical
	// tables, so deleting the files row first would orphan them beyond any
	// future reach (see TableDropper).
	s.dropTablesFor(ctx, []string{fileID})
	_, err := s.pool.Exec(ctx, `DELETE FROM files WHERE id = $1`, fileID)
	if err != nil {
		return fmt.Errorf("DeleteGitRepoFileByID: %w", err)
	}
	return nil
}

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

func (s *PGStore) GetGitRepoSourceFileProgress(ctx context.Context, sourceID string) (total, done int, err error) {
	const q = `
		SELECT COUNT(*)::int AS total,
		       COUNT(*) FILTER (WHERE status NOT IN ('pending','processing'))::int AS done
		FROM files WHERE git_repo_source_id = $1`
	type progressRow struct {
		Total int `db:"total"`
		Done  int `db:"done"`
	}
	rows, err := pgxutil.QueryRows[progressRow](ctx, s.pool, q, sourceID)
	if err != nil {
		return 0, 0, fmt.Errorf("GetGitRepoSourceFileProgress: %w", err)
	}
	if len(rows) == 0 {
		return 0, 0, nil
	}
	return rows[0].Total, rows[0].Done, nil
}

// ---------------------------------------------------------------------------
// Sweeper contract (internal/syncsched)
// ---------------------------------------------------------------------------

// Kind identifies this store to the sweeper (internal/syncsched).
func (s *PGStore) Kind() string { return "git_repo" }

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

// ListDue returns sources whose stamped slot has arrived. status IN
// ('active','error') deliberately keeps an errored source on the schedule:
// sync.go sets status='error' on any sync failure (a clone timeout, a
// revoked PAT), and dropping the predicate to status='active' would take the
// source out of both ListDue and ListUnscheduled permanently — nothing would
// ever move next_sync_at again. 'paused' and 'syncing' stay excluded.
func (s *PGStore) ListDue(ctx context.Context, now time.Time) ([]syncwindow.DueSource, error) {
	const sql = `
		SELECT id::text AS id, sync_schedule FROM git_repo_sources
		 WHERE sync_schedule <> 'manual'
		   AND status IN ('active', 'error')
		   AND next_sync_at IS NOT NULL
		   AND next_sync_at <= $1`
	rows, err := pgxutil.QueryRows[dueSourceRow](ctx, s.pool, sql, now)
	if err != nil {
		return nil, fmt.Errorf("ListDue(git_repo): %w", err)
	}
	return toDueSources(rows), nil
}

// ListUnscheduled returns sources with a schedule but no stamped slot yet
// (newly created, or newly switched away from manual). The sweeper stamps
// them WITHOUT enqueuing, so enabling a schedule never triggers a daytime sync.
func (s *PGStore) ListUnscheduled(ctx context.Context) ([]syncwindow.DueSource, error) {
	const sql = `
		SELECT id::text AS id, sync_schedule FROM git_repo_sources
		 WHERE sync_schedule <> 'manual'
		   AND status IN ('active', 'error')
		   AND next_sync_at IS NULL`
	rows, err := pgxutil.QueryRows[dueSourceRow](ctx, s.pool, sql)
	if err != nil {
		return nil, fmt.Errorf("ListUnscheduled(git_repo): %w", err)
	}
	return toDueSources(rows), nil
}

// MarkScheduled stamps the next slot. No status predicate here: it addresses
// a single row by primary key (the id came from ListDue/ListUnscheduled,
// which already filtered on status).
func (s *PGStore) MarkScheduled(ctx context.Context, id string, next time.Time) error {
	const sql = `UPDATE git_repo_sources SET next_sync_at = $2 WHERE id = $1`
	_, err := s.pool.Exec(ctx, sql, id, next)
	if err != nil {
		return fmt.Errorf("MarkScheduled(git_repo, %s): %w", id, err)
	}
	return nil
}
