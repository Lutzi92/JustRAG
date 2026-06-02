package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/store"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Run is the Go-side view of a row in eval_runs.
type Run struct {
	ID             uuid.UUID       `json:"id"`
	CreatedAt      time.Time       `json:"created_at"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	Status         string          `json:"status"`
	TriggeredBy    *uuid.UUID      `json:"triggered_by,omitempty"`
	KBID           uuid.UUID       `json:"kb_id"`
	GoldenSetID    *uuid.UUID      `json:"golden_set_id,omitempty"`
	FixtureHash    string          `json:"fixture_hash"`
	ConfigSnapshot json.RawMessage `json:"config_snapshot"`
	JudgeEnabled   bool            `json:"judge_enabled"`
	TopK           int             `json:"top_k"`
	Report         json.RawMessage `json:"report,omitempty"`
	ErrorMessage   string          `json:"error_message,omitempty"`
	Label          string          `json:"label,omitempty"`
}

// ListOpts controls filtering and pagination for List.
type ListOpts struct {
	Limit  int        // default 50 if <=0, max 200
	Offset int        // default 0
	Status string     // optional filter; empty = no filter
	KBID   *uuid.UUID // optional filter
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// Store is a PostgreSQL-backed store for eval_runs.
// NOTE: integration tests for this store are deferred to the handler tests
// (Tasks 8–12) which will exercise all paths via a mock; no test-DB helper
// exists in this repo to run in-process Postgres tests.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a new Store backed by pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// ---------------------------------------------------------------------------
// evalRunRow is an internal scan struct for eval_runs rows (without report).
// pgxutil.QueryRows uses pgx.RowToStructByName which requires db struct tags.
// ---------------------------------------------------------------------------

type evalRunRow struct {
	ID             uuid.UUID  `db:"id"`
	CreatedAt      time.Time  `db:"created_at"`
	StartedAt      *time.Time `db:"started_at"`
	FinishedAt     *time.Time `db:"finished_at"`
	Status         string     `db:"status"`
	TriggeredBy    *uuid.UUID `db:"triggered_by"`
	KBID           uuid.UUID  `db:"kb_id"`
	GoldenSetID    *uuid.UUID `db:"golden_set_id"`
	FixtureHash    string     `db:"fixture_hash"`
	ConfigSnapshot []byte     `db:"config_snapshot"`
	JudgeEnabled   bool       `db:"judge_enabled"`
	TopK           int        `db:"top_k"`
	ErrorMessage   *string    `db:"error_message"`
	Label          *string    `db:"label"`
}

// toRun converts an evalRunRow to a Run domain object.
func toRun(r evalRunRow) Run {
	run := Run{
		ID:             r.ID,
		CreatedAt:      r.CreatedAt,
		StartedAt:      r.StartedAt,
		FinishedAt:     r.FinishedAt,
		Status:         r.Status,
		TriggeredBy:    r.TriggeredBy,
		KBID:           r.KBID,
		GoldenSetID:    r.GoldenSetID,
		FixtureHash:    r.FixtureHash,
		ConfigSnapshot: json.RawMessage(r.ConfigSnapshot),
		JudgeEnabled:   r.JudgeEnabled,
		TopK:           r.TopK,
	}
	if r.ErrorMessage != nil {
		run.ErrorMessage = *r.ErrorMessage
	}
	if r.Label != nil {
		run.Label = *r.Label
	}
	return run
}

// ---------------------------------------------------------------------------
// Insert
// ---------------------------------------------------------------------------

// Insert inserts a new eval run with status 'queued' and returns the generated id.
func (s *Store) Insert(ctx context.Context, r Run) (uuid.UUID, error) {
	const q = `
		INSERT INTO eval_runs
		  (status, triggered_by, kb_id, golden_set_id, fixture_hash,
		   config_snapshot, judge_enabled, top_k, label)
		VALUES ('queued', $1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`

	var id uuid.UUID
	err := s.pool.QueryRow(ctx, q,
		r.TriggeredBy,
		r.KBID,
		r.GoldenSetID,
		r.FixtureHash,
		[]byte(r.ConfigSnapshot),
		r.JudgeEnabled,
		r.TopK,
		nullableString(r.Label),
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("eval insert run: %w", err)
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// HasActiveRun
// ---------------------------------------------------------------------------

// HasActiveRun reports whether kbID already has a queued or running eval. Used
// by the KB-scoped CreateRun handler to reject (409) a second concurrent
// submission for the same KB.
func (s *Store) HasActiveRun(ctx context.Context, kbID uuid.UUID) (bool, error) {
	var exists bool
	const q = `SELECT exists(SELECT 1 FROM eval_runs WHERE kb_id = $1 AND status IN ('queued','running'))`
	if err := s.pool.QueryRow(ctx, q, kbID).Scan(&exists); err != nil {
		return false, fmt.Errorf("eval has active run: %w", err)
	}
	return exists, nil
}

// ---------------------------------------------------------------------------
// MarkRunning
// ---------------------------------------------------------------------------

// MarkRunning transitions a queued run to running. Idempotent — if the run is
// already running or completed, no error is returned.
func (s *Store) MarkRunning(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE eval_runs SET status='running', started_at=now() WHERE id=$1 AND status='queued'`,
		id,
	)
	if err != nil {
		return fmt.Errorf("eval mark running: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// MarkCompleted
// ---------------------------------------------------------------------------

// MarkCompleted transitions a run to completed and stores its report.
func (s *Store) MarkCompleted(ctx context.Context, id uuid.UUID, report json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE eval_runs SET status='completed', report=$2, finished_at=now() WHERE id=$1`,
		id, []byte(report),
	)
	if err != nil {
		return fmt.Errorf("eval mark completed: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// MarkFailed
// ---------------------------------------------------------------------------

// MarkFailed transitions a run to failed and records the error message.
func (s *Store) MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE eval_runs SET status='failed', error_message=$2, finished_at=now() WHERE id=$1`,
		id, errMsg,
	)
	if err != nil {
		return fmt.Errorf("eval mark failed: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

// Get returns the full run record including the report, or (nil, store.ErrNotFound)
// when the row doesn't exist.
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Run, error) {
	const q = `
		SELECT id, created_at, started_at, finished_at, status, triggered_by,
		       kb_id, golden_set_id, fixture_hash, config_snapshot,
		       judge_enabled, top_k, report, error_message, label
		FROM eval_runs
		WHERE id = $1`

	var id2 uuid.UUID
	var createdAt time.Time
	var startedAt, finishedAt *time.Time
	var status string
	var triggeredBy *uuid.UUID
	var kbID uuid.UUID
	var goldenSetID *uuid.UUID
	var fixtureHash string
	var configSnapshot, report []byte
	var judgeEnabled bool
	var topK int
	var errorMessage, label *string

	err := s.pool.QueryRow(ctx, q, id).Scan(
		&id2, &createdAt, &startedAt, &finishedAt, &status, &triggeredBy,
		&kbID, &goldenSetID, &fixtureHash, &configSnapshot,
		&judgeEnabled, &topK, &report, &errorMessage, &label,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("eval get run: %w", err)
	}

	run := Run{
		ID:             id2,
		CreatedAt:      createdAt,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		Status:         status,
		TriggeredBy:    triggeredBy,
		KBID:           kbID,
		GoldenSetID:    goldenSetID,
		FixtureHash:    fixtureHash,
		ConfigSnapshot: json.RawMessage(configSnapshot),
		JudgeEnabled:   judgeEnabled,
		TopK:           topK,
		Report:         json.RawMessage(report),
	}
	if errorMessage != nil {
		run.ErrorMessage = *errorMessage
	}
	if label != nil {
		run.Label = *label
	}
	return &run, nil
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

// clampLimit normalises the Limit field: default 50, max 200.
func clampLimit(l int) int {
	if l <= 0 {
		return 50
	}
	if l > 200 {
		return 200
	}
	return l
}

// List returns a paginated slice of runs sorted by created_at DESC together
// with the total count of matching rows. The report column is intentionally
// excluded; use Get to retrieve a full record with the report.
func (s *Store) List(ctx context.Context, opts ListOpts) ([]Run, int, error) {
	limit := clampLimit(opts.Limit)
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	// Build a reusable WHERE fragment to keep the main query and the count
	// query consistent.
	args := []any{}
	where := "WHERE 1=1"
	if opts.Status != "" {
		args = append(args, opts.Status)
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if opts.KBID != nil {
		args = append(args, *opts.KBID)
		where += fmt.Sprintf(" AND kb_id = $%d", len(args))
	}

	// Single statement: page rows + window-function total. Both numbers
	// derive from the same MVCC snapshot so concurrent inserts/deletes
	// can't make the count and the page disagree.
	args = append(args, limit, offset)
	listSQL := fmt.Sprintf(`
		SELECT id, created_at, started_at, finished_at, status, triggered_by,
		       kb_id, golden_set_id, fixture_hash, config_snapshot,
		       judge_enabled, top_k, error_message, label,
		       COUNT(*) OVER ()::int AS total_count
		FROM eval_runs
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`,
		where, len(args)-1, len(args),
	)

	rows, err := s.pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("eval list runs query: %w", err)
	}
	defer rows.Close()

	var (
		result []Run
		total  int
	)
	for rows.Next() {
		var row evalRunRow
		var rowTotal int
		if err := rows.Scan(
			&row.ID, &row.CreatedAt, &row.StartedAt, &row.FinishedAt, &row.Status, &row.TriggeredBy,
			&row.KBID, &row.GoldenSetID, &row.FixtureHash, &row.ConfigSnapshot,
			&row.JudgeEnabled, &row.TopK, &row.ErrorMessage, &row.Label,
			&rowTotal,
		); err != nil {
			return nil, 0, fmt.Errorf("eval list runs scan: %w", err)
		}
		total = rowTotal
		result = append(result, toRun(row))
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("eval list runs iterate: %w", err)
	}

	if result == nil {
		// Empty page (offset past end or empty table). Issue a separate
		// COUNT so callers see the true total even when no rows came back.
		countSQL := "SELECT COUNT(*)::int FROM eval_runs " + where
		// Strip the LIMIT/OFFSET args that were appended above.
		countArgs := args[:len(args)-2]
		if err := s.pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("eval list runs count: %w", err)
		}
		result = []Run{}
	}
	return result, total, nil
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

// Delete removes an eval run by id within a transaction. Returns:
//   - (true,  false, nil)  — deleted successfully.
//   - (false, true,  nil)  — run is currently running; not deleted (caller should 409).
//   - (false, false, nil)  — run not found (caller should 404).
//   - (false, false, err)  — unexpected DB error.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) (deleted bool, running bool, err error) {
	// Serializable: the body is a read-check-write cycle (status check → DELETE).
	// At READ COMMITTED a concurrent transition from "running" to "completed"
	// after the SELECT but before the DELETE could let us race a worker that
	// considers the run still active; Serializable surfaces that race as a
	// serialization failure. WithSerializableRetry transparently retries on
	// 40001 / 40P01 so a transient contention spike doesn't surface as a
	// 5xx to the admin user.
	var (
		notFound bool
		isRun    bool
	)
	txErr := pgxutil.WithSerializableRetry(ctx, s.pool, func(tx pgx.Tx) error {
		// Reset retry-visible state at the start of every attempt so a retry
		// of the same logical operation can't see a stale flag from an
		// aborted attempt's partial progress.
		notFound, isRun = false, false

		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM eval_runs WHERE id = $1`, id).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				notFound = true
				return nil
			}
			return fmt.Errorf("eval delete run lookup: %w", err)
		}
		if status == "running" {
			isRun = true
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM eval_runs WHERE id = $1`, id); err != nil {
			return fmt.Errorf("eval delete run exec: %w", err)
		}
		return nil
	})
	if txErr != nil {
		return false, false, txErr
	}
	if notFound {
		return false, false, nil
	}
	if isRun {
		return false, true, nil
	}
	return true, false, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// nullableString converts an empty string to nil so it is stored as NULL in
// nullable text columns (label, etc.).
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
