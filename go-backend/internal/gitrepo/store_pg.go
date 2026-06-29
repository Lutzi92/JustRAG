package gitrepo

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

const gitRepoSourceColumns = `
	id, kb_id, repo_url, is_private, access_token_encrypted, branch, status,
	error_message, consecutive_failures, last_synced_at, last_commit_sha,
	file_count, sync_progress, sync_total, created_at`

type GitRepoSourceRow struct {
	ID                   string
	KbID                 string
	RepoURL              string
	IsPrivate            bool
	AccessTokenEncrypted *string
	Branch               *string
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
}

type GitRepoSourceUpdate struct {
	Status *string // "active" | "paused"
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
}

type PGStore struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

func (s *PGStore) CreateGitRepoSource(ctx context.Context, in CreateGitRepoSourceInput) (*GitRepoSourceRow, error) {
	const q = `
		INSERT INTO git_repo_sources (kb_id, repo_url, is_private, access_token_encrypted, branch, status)
		VALUES ($1, $2, $3, $4, $5, 'active')
		RETURNING ` + gitRepoSourceColumns
	rows, err := pgxutil.QueryRows[gitRepoSourceDBRow](ctx, s.pool, q,
		in.KbID, in.RepoURL, in.IsPrivate, in.AccessTokenEncrypted, in.Branch)
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
	if upd.Status == nil {
		return nil // Nothing to update.
	}
	const q = `UPDATE git_repo_sources SET status = $2 WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, id, *upd.Status)
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

func (s *PGStore) DeleteGitRepoFileByID(ctx context.Context, fileID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM files WHERE id = $1`, fileID)
	if err != nil {
		return fmt.Errorf("DeleteGitRepoFileByID: %w", err)
	}
	return nil
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
