package files

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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
	// visibility ist seit Migration 0065 die Schreibwahrheit; is_global ist nur
	// noch eine generierte Spiegelspalte und faellt im Cleanup-Release. Der
	// Alias haelt kbRow.IsGlobal und damit EffectiveRole unveraendert.
	const sql = `SELECT id, user_id, (visibility = 'public') AS is_global, is_published
	             FROM knowledge_bases WHERE id = $1`

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

	// published_at is passed as a typed nil for every origin that has no
	// publication date of its own, which is all of them but RSS today
	// (W3-R9) — the column then stays NULL and COALESCE(published_at,
	// created_at) falls back to the ingest timestamp at every read site.
	if data.RSSFeedID != "" {
		sqlStr = `
			INSERT INTO files (kb_id, name, type, size, status, origin, storage_path, rss_feed_id, published_at)
			VALUES ($1, $2, $3, $4, 'pending', $5, $6, $7, $8)
			RETURNING id, kb_id, name, type, size, status, progress, origin, storage_path, created_at`
		args = []any{data.KbID, data.Name, data.Type, data.Size, data.Origin, data.StoragePath, data.RSSFeedID, data.PublishedAt}
	} else {
		sqlStr = `
			INSERT INTO files (kb_id, name, type, size, status, origin, storage_path, published_at)
			VALUES ($1, $2, $3, $4, 'pending', $5, $6, $7)
			RETURNING id, kb_id, name, type, size, status, progress, origin, storage_path, created_at`
		args = []any{data.KbID, data.Name, data.Type, data.Size, data.Origin, data.StoragePath, data.PublishedAt}
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
// Source dates (freshness surface)
// ---------------------------------------------------------------------------

// FileDates carries the two date columns of a files row: CreatedAt is the
// ingest timestamp, PublishedAt the document's own publication date (NULL for
// every origin that does not carry one — see CreateFileData.PublishedAt).
// Mirrors chat.FileDates; this package deliberately does not import the chat
// package, so production wires a tiny adapter in the route setup, the same
// way UploadLimits is wired.
type FileDates struct {
	CreatedAt   time.Time
	PublishedAt *time.Time
}

// fileDatesRow scans one row of the batch date lookup.
type fileDatesRow struct {
	ID          string     `db:"id"`
	CreatedAt   time.Time  `db:"created_at"`
	PublishedAt *time.Time `db:"published_at"`
}

// FileDatesByIDs resolves file ids to their dates in one indexed query. Used
// once per chat turn to stamp freshness onto the answer's sources; ids that
// no longer exist are simply absent from the result map.
func (s *PGStore) FileDatesByIDs(ctx context.Context, ids []string) (map[string]FileDates, error) {
	if len(ids) == 0 {
		return map[string]FileDates{}, nil
	}
	// `id = ANY($1::uuid[])`, never `id::text = ANY($1)`: casting the COLUMN
	// makes the primary-key index unusable and Postgres falls back to a Seq
	// Scan over files — which this query would then do on every chat turn.
	// Casting the PARAMETER instead keeps it an index scan.
	const sql = `SELECT id::text AS id, created_at, published_at
	               FROM files
	              WHERE id = ANY($1::uuid[])`
	rows, err := pgxutil.QueryRows[fileDatesRow](ctx, s.pool, sql, ids)
	if err != nil {
		return nil, fmt.Errorf("FileDatesByIDs: %w", err)
	}
	out := make(map[string]FileDates, len(rows))
	for _, r := range rows {
		out[r.ID] = FileDates{CreatedAt: r.CreatedAt, PublishedAt: r.PublishedAt}
	}
	return out, nil
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
	const sql = `UPDATE files SET current_stage = NULL, stage_index = NULL, stage_total = NULL, stage_detail = NULL WHERE id = $1`
	if _, err := s.pool.Exec(ctx, sql, fileID); err != nil {
		return fmt.Errorf("ClearFileStage: %w", err)
	}
	return nil
}

// SetFileParseReport records the per-file spreadsheet ingest report (sheet
// kinds, header rows, row counts, coercion failures — see
// internal/tabular.ParseReport). May contain cell-derived text (column
// names, sample-derived diagnostics), so callers must never log it in full.
func (s *PGStore) SetFileParseReport(ctx context.Context, fileID string, report []byte) error {
	const sql = `UPDATE files SET parse_report = $1::jsonb WHERE id = $2`
	if _, err := s.pool.Exec(ctx, sql, report, fileID); err != nil {
		return fmt.Errorf("SetFileParseReport: %w", err)
	}
	return nil
}

// UpdateFileStageDetail records a human-readable progress detail for the
// current stage (e.g. "Blatt 2/3 · 120000 Zeilen") so the upload spinner can
// show more than a bare n/x during a long spreadsheet materialisation. Also
// bumps progress_updated_at — see UpdateFileStage's comment on why a stage
// transition doubles as a liveness heartbeat. An empty detail clears the
// column (NULLIF) rather than storing "".
func (s *PGStore) UpdateFileStageDetail(ctx context.Context, fileID, detail string) error {
	const sql = `UPDATE files SET stage_detail = NULLIF($1, ''), progress_updated_at = NOW() WHERE id = $2`
	if _, err := s.pool.Exec(ctx, sql, detail, fileID); err != nil {
		return fmt.Errorf("UpdateFileStageDetail: %w", err)
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

// GetFileOrigin returns the files.origin value for fileID ("upload", "rss",
// "confluence", "git", "crawl", "websearch", "research"), or "" when no such
// file exists. Split out as its own one-column read because the ingest
// prompt-injection screen needs the origin and nothing else — threading an
// Origin field through ProcessFileInput instead would need every one of the
// (currently six) construction sites to remember to populate it, and a
// missed one fails open silently.
func (s *PGStore) GetFileOrigin(ctx context.Context, fileID string) (string, error) {
	const sql = `SELECT origin FROM files WHERE id = $1`
	var origin string
	if err := s.pool.QueryRow(ctx, sql, fileID).Scan(&origin); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("GetFileOrigin: %w", err)
	}
	return origin, nil
}

// SetInjectionFlag records an ingest-time prompt-injection screening hit
// (migration 0072). detail is a promptsafety.Finding plus a screened_at
// timestamp, marshalled by the caller. The flag is advisory only — nothing
// in retrieval or answering reads it, it exists so an operator can see which
// external documents carry instruction-shaped text.
//
// detail contains untrusted, document-derived text and must never be logged
// in full or fed back into a prompt (same posture as SetFileParseReport).
func (s *PGStore) SetInjectionFlag(ctx context.Context, fileID string, detail []byte) error {
	const sql = `UPDATE files SET injection_flag = true, injection_detail = $1::jsonb WHERE id = $2`
	if _, err := s.pool.Exec(ctx, sql, detail, fileID); err != nil {
		return fmt.Errorf("SetInjectionFlag: %w", err)
	}
	return nil
}

// MarkInjectionScreenedClean records a screening pass that found nothing:
// injection_flag = false with a detail carrying ONLY {"screened_at": …} —
// no rule, no position, no snippet. That is what makes the three states of
// these two columns distinguishable:
//
//	detail IS NULL            never screened (ingested before the screen
//	                          existed, an origin that is never screened, or
//	                          the kill switch was off)
//	detail = {screened_at}    screened, clean
//	detail carries "rule"     screened, flagged (injection_flag is true)
//
// It also drops a stale flag: a re-ingest of a file that was flagged on a
// previous pass (the source page was fixed, or the pattern set changed)
// must not keep the badge forever.
//
// The WHERE clause makes this a no-op for a row that is already recorded as
// clean. Every RSS/Confluence/git poll re-ingests unchanged documents, and
// an unconditional UPDATE would rewrite (and bloat) the files table on every
// sweep just to store a new timestamp nothing reads. The three disjuncts are
// exactly the rows whose verdict actually changes: currently flagged, never
// screened, or carrying an old finding.
func (s *PGStore) MarkInjectionScreenedClean(ctx context.Context, fileID string, detail []byte) error {
	const sql = `
		UPDATE files SET injection_flag = false, injection_detail = $1::jsonb
		WHERE id = $2
		  AND (injection_flag OR injection_detail IS NULL OR injection_detail ? 'rule')`
	if _, err := s.pool.Exec(ctx, sql, detail, fileID); err != nil {
		return fmt.Errorf("MarkInjectionScreenedClean: %w", err)
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
// clears its error detail, previous parse report, and stage detail. Returns
// false when the file is not in 'error' status (already retried, deleted,
// or still processing) — the WHERE clause doubles as the double-click /
// concurrent-retry guard. parse_report/stage_detail are cleared here (not
// only overwritten by a successful re-ingest) so a retry that fails again
// before reaching the spreadsheet ingester — e.g. a parse-stage error —
// never leaves the PREVIOUS attempt's report/detail visible as if it were
// current.
func (s *PGStore) ResetFileForRetry(ctx context.Context, fileID string) (bool, error) {
	const sql = `
		UPDATE files SET status = 'pending', progress = 0,
		       error_stage = NULL, error_message = NULL,
		       parse_report = NULL, stage_detail = NULL
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

// ListSpreadsheetFiles returns the FileInfo of every spreadsheet file
// (.xlsx/.xls/.ods/.csv/.tsv, matched case-insensitively on the file name)
// in kbID that has passed through ingestion at least once — status
// 'completed', 'partial', or 'error' (the same recovery-inclusive set as
// ListReembedableFilesByKBID: an errored spreadsheet still needs its old
// chunks and tables torn down and rebuilt). Backs the per-KB tabular
// rematerialize endpoint, which enqueues one re-embedding job per row
// returned here so an operator can apply changed tabular_* settings without
// re-uploading. Oldest first (stable order).
func (s *PGStore) ListSpreadsheetFiles(ctx context.Context, kbID string) ([]*FileInfo, error) {
	const sql = `
		SELECT id, kb_id, name, type, storage_path
		FROM files
		WHERE kb_id = $1
		  AND status IN ('completed', 'partial', 'error')
		  AND (
			lower(name) LIKE '%.xlsx' OR
			lower(name) LIKE '%.xls' OR
			lower(name) LIKE '%.ods' OR
			lower(name) LIKE '%.csv' OR
			lower(name) LIKE '%.tsv'
		  )
		ORDER BY created_at`
	rows, err := pgxutil.QueryRows[fileInfoDBRow](ctx, s.pool, sql, kbID)
	if err != nil {
		return nil, fmt.Errorf("ListSpreadsheetFiles: %w", err)
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
