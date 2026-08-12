package files

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of files.Store and
// processor.ProcessorStore.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// ---------------------------------------------------------------------------
// kbaccess helpers (required by files.Store)
// ---------------------------------------------------------------------------

// kbRow is an internal struct with db tags for scanning knowledge_bases.
type kbRow struct {
	ID          string  `db:"id"`
	UserID      *string `db:"user_id"`
	IsGlobal    bool    `db:"is_global"`
	IsPublished bool    `db:"is_published"`
}

// GetKBByID returns the knowledge base with the given ID, or nil if not found.
func (s *PGStore) GetKBByID(ctx context.Context, id string) (*kbaccess.KnowledgeBase, error) {
	const sql = `SELECT id, user_id, is_global, is_published FROM knowledge_bases WHERE id = $1`

	rows, err := pgxutil.QueryRows[kbRow](ctx, s.pool, sql, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	r := rows[0]
	return &kbaccess.KnowledgeBase{
		ID:          r.ID,
		UserID:      r.UserID,
		IsGlobal:    r.IsGlobal,
		IsPublished: r.IsPublished,
	}, nil
}

// kbRoleRow is an internal struct with db tags for scanning kb_members.
type kbRoleRow struct {
	Role string `db:"role"`
}

// GetKBRole returns the explicit kb_members role for (kbID, userID), or ""
// when the user has no row. Implicit roles (superadmin, systemadmin on a
// public KB, any user on a published public KB) are resolved by
// kbaccess.EffectiveRole, not here — the store answers only what is stored.
// Mirrors internal/kbaccess/store_pg.go's GetKBRole.
func (s *PGStore) GetKBRole(ctx context.Context, kbID, userID string) (string, error) {
	const sql = `SELECT role FROM kb_members WHERE kb_id = $1 AND user_id = $2 LIMIT 1`

	rows, err := pgxutil.QueryRows[kbRoleRow](ctx, s.pool, sql, kbID, userID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Role, nil
}

// ---------------------------------------------------------------------------
// File operations
// ---------------------------------------------------------------------------

// fileInfoDBRow is an internal struct for scanning the fields needed by the
// files handler (download / delete).
type fileInfoDBRow struct {
	ID          string  `db:"id"`
	KbID        string  `db:"kb_id"`
	Name        string  `db:"name"`
	Type        string  `db:"type"`
	StoragePath *string `db:"storage_path"`
}

// GetFileByID returns the FileInfo for the given file ID, or nil if not found.
func (s *PGStore) GetFileByID(ctx context.Context, id string) (*FileInfo, error) {
	const sql = `SELECT id, kb_id, name, type, storage_path FROM files WHERE id = $1`

	rows, err := pgxutil.QueryRows[fileInfoDBRow](ctx, s.pool, sql, id)
	if err != nil {
		return nil, fmt.Errorf("GetFileByID: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := rows[0]
	return &FileInfo{
		ID:          r.ID,
		KbID:        r.KbID,
		Name:        r.Name,
		Type:        r.Type,
		StoragePath: r.StoragePath,
	}, nil
}

// DeleteFileRecord removes the file row with the given ID from the database.
func (s *PGStore) DeleteFileRecord(ctx context.Context, id string) error {
	const sql = `DELETE FROM files WHERE id = $1`
	_, err := s.pool.Exec(ctx, sql, id)
	if err != nil {
		return fmt.Errorf("DeleteFileRecord: %w", err)
	}
	return nil
}

// createFileDBRow is an internal struct for scanning the RETURNING clause of CreateFile.
type createFileDBRow struct {
	ID          string    `db:"id"`
	KbID        string    `db:"kb_id"`
	Name        string    `db:"name"`
	Type        string    `db:"type"`
	Size        *int      `db:"size"`
	Status      string    `db:"status"`
	Progress    int       `db:"progress"`
	Origin      string    `db:"origin"`
	StoragePath *string   `db:"storage_path"`
	CreatedAt   time.Time `db:"created_at"`
}

// CreateFile inserts a new file record with status 'pending' and returns the created row.
func (s *PGStore) CreateFile(ctx context.Context, data CreateFileData) (*FileRecord, error) {
	var sqlStr string
	var args []any

	if data.RSSFeedID != "" {
		sqlStr = `
			INSERT INTO files (kb_id, name, type, size, status, origin, storage_path, rss_feed_id)
			VALUES ($1, $2, $3, $4, 'pending', $5, $6, $7)
			RETURNING id, kb_id, name, type, size, status, progress, origin, storage_path, created_at`
		args = []any{data.KbID, data.Name, data.Type, data.Size, data.Origin, data.StoragePath, data.RSSFeedID}
	} else {
		sqlStr = `
			INSERT INTO files (kb_id, name, type, size, status, origin, storage_path)
			VALUES ($1, $2, $3, $4, 'pending', $5, $6)
			RETURNING id, kb_id, name, type, size, status, progress, origin, storage_path, created_at`
		args = []any{data.KbID, data.Name, data.Type, data.Size, data.Origin, data.StoragePath}
	}

	rows, err := pgxutil.QueryRows[createFileDBRow](ctx, s.pool, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("CreateFile: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CreateFile: no row returned")
	}
	r := rows[0]
	return &FileRecord{
		ID:          r.ID,
		KbID:        r.KbID,
		Name:        r.Name,
		Type:        r.Type,
		Size:        r.Size,
		Status:      r.Status,
		Progress:    r.Progress,
		Origin:      r.Origin,
		StoragePath: r.StoragePath,
		CreatedAt:   r.CreatedAt,
	}, nil
}

// ---------------------------------------------------------------------------
// Per-KB file limits
// ---------------------------------------------------------------------------

// kbFileLimitsRow is an internal struct for scanning the file count and total size.
type kbFileLimitsRow struct {
	FileCount int   `db:"file_count"`
	TotalSize int64 `db:"total_size"`
}

// GetKBFileLimits returns the current file count and total size for a KB.
func (s *PGStore) GetKBFileLimits(ctx context.Context, kbID string) (*KBFileLimits, error) {
	const sql = `SELECT COUNT(*)::int AS file_count, COALESCE(SUM(size), 0)::bigint AS total_size FROM files WHERE kb_id = $1`
	rows, err := pgxutil.QueryRows[kbFileLimitsRow](ctx, s.pool, sql, kbID)
	if err != nil {
		return nil, fmt.Errorf("GetKBFileLimits: %w", err)
	}
	return &KBFileLimits{FileCount: rows[0].FileCount, TotalSize: rows[0].TotalSize}, nil
}

// ---------------------------------------------------------------------------
// processor.ProcessorStore — UpdateFileStatus / UpdateFileProgress
// ---------------------------------------------------------------------------

// UpdateFileStatus sets the status column for the given file ID and clears
// any recorded error detail — every non-error transition (pending,
// processing, completed, partial) invalidates a previous failure reason.
// Error transitions go through MarkFileError / MarkFileErrorIfUnset instead.
func (s *PGStore) UpdateFileStatus(ctx context.Context, fileID, status string) error {
	const sql = `UPDATE files SET status = $1, error_stage = NULL, error_message = NULL WHERE id = $2`
	_, err := s.pool.Exec(ctx, sql, status, fileID)
	if err != nil {
		return fmt.Errorf("UpdateFileStatus: %w", err)
	}
	return nil
}

// UpdateFileProgress updates the progress column as a percentage (0-100) for the given file ID.
func (s *PGStore) UpdateFileProgress(ctx context.Context, fileID string, progress int) error {
	const sql = `UPDATE files SET progress = $1, progress_updated_at = NOW() WHERE id = $2`
	_, err := s.pool.Exec(ctx, sql, progress, fileID)
	if err != nil {
		return fmt.Errorf("UpdateFileProgress: %w", err)
	}
	return nil
}

// UpdateFileStage records the file's current ingestion stage for the upload
// spinner + n/x indicator. stage is a stable key (parse/tabular/enrich/embed/
// kg/hype/raptor); index is 1-based; total is the enabled-stage count. Set at
// every stage boundary; cleared via ClearFileStage at true completion.
//
// Also bumps progress_updated_at: a stage transition is a liveness heartbeat
// from the worker, and the stuck-file maintenance sweep keys on
// progress_updated_at. Without this, a long stage that doesn't emit a percent
// update (enrich/KG/RAPTOR/large-file parse) lets progress_updated_at go stale
// past StuckFileTimeout, so the sweep transiently flags an actively-ingesting
// file as error/timeout until completion clears it (see worker.checkStuckFiles).
func (s *PGStore) UpdateFileStage(ctx context.Context, fileID, stage string, index, total int) error {
	const sql = `UPDATE files SET current_stage = $1, stage_index = $2, stage_total = $3, progress_updated_at = NOW() WHERE id = $4`
	if _, err := s.pool.Exec(ctx, sql, stage, index, total, fileID); err != nil {
		return fmt.Errorf("UpdateFileStage: %w", err)
	}
	return nil
}

// ClearFileStage nulls the stage columns — the file is no longer actively
// ingesting (done, errored, or abandoned). Idempotent.
func (s *PGStore) ClearFileStage(ctx context.Context, fileID string) error {
	const sql = `UPDATE files SET current_stage = NULL, stage_index = NULL, stage_total = NULL WHERE id = $1`
	if _, err := s.pool.Exec(ctx, sql, fileID); err != nil {
		return fmt.Errorf("ClearFileStage: %w", err)
	}
	return nil
}

// MarkFileError sets status='error' and records the failing stage plus a
// short, sanitized, user-facing message. Overwrites previous detail — call
// sites record the most recent attempt's failure. The raw Go error must
// never be passed as message; it stays in logs (request_id-joinable).
//
// Stage vocabulary: unsupported_type, parse, embedding, canceled,
// processing, timeout, queue. The frontend maps stages to translated
// labels, so additions need a matching key in web/src/translations.ts.
func (s *PGStore) MarkFileError(ctx context.Context, fileID, stage, message string) error {
	const sql = `UPDATE files SET status = 'error', error_stage = $1, error_message = $2 WHERE id = $3`
	_, err := s.pool.Exec(ctx, sql, stage, message, fileID)
	if err != nil {
		return fmt.Errorf("MarkFileError: %w", err)
	}
	return nil
}

// MarkFileErrorIfUnset sets status='error' but keeps an already-recorded
// stage/message. Used by the retry-exhaustion wrapper, which fires after
// the final attempt's handler already recorded the specific reason — its
// generic message must not clobber that. Files that already reached a
// successful terminal state (completed/partial) are left untouched: a
// late-firing exhaustion wrapper must not regress a successful ingest.
func (s *PGStore) MarkFileErrorIfUnset(ctx context.Context, fileID, stage, message string) error {
	const sql = `
		UPDATE files SET status = 'error',
		       error_stage   = COALESCE(error_stage, $1),
		       error_message = COALESCE(error_message, $2)
		WHERE id = $3 AND status NOT IN ('completed', 'partial')`
	_, err := s.pool.Exec(ctx, sql, stage, message, fileID)
	if err != nil {
		return fmt.Errorf("MarkFileErrorIfUnset: %w", err)
	}
	return nil
}

// ResetFileForRetry atomically flips an errored file back to 'pending' and
// clears its error detail. Returns false when the file is not in 'error'
// status (already retried, deleted, or still processing) — the WHERE
// clause doubles as the double-click / concurrent-retry guard.
func (s *PGStore) ResetFileForRetry(ctx context.Context, fileID string) (bool, error) {
	const sql = `
		UPDATE files SET status = 'pending', progress = 0,
		       error_stage = NULL, error_message = NULL
		WHERE id = $1 AND status = 'error'`
	tag, err := s.pool.Exec(ctx, sql, fileID)
	if err != nil {
		return false, fmt.Errorf("ResetFileForRetry: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListErrorFiles returns the FileInfo of every file in kbID with
// status='error', oldest first (stable bulk-retry order).
func (s *PGStore) ListErrorFiles(ctx context.Context, kbID string) ([]*FileInfo, error) {
	const sql = `
		SELECT id, kb_id, name, type, storage_path
		FROM files
		WHERE kb_id = $1 AND status = 'error'
		ORDER BY created_at`
	rows, err := pgxutil.QueryRows[fileInfoDBRow](ctx, s.pool, sql, kbID)
	if err != nil {
		return nil, fmt.Errorf("ListErrorFiles: %w", err)
	}
	out := make([]*FileInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, &FileInfo{
			ID:          r.ID,
			KbID:        r.KbID,
			Name:        r.Name,
			Type:        r.Type,
			StoragePath: r.StoragePath,
		})
	}
	return out, nil
}
