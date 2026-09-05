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
	"github.com/justrag/go-backend/internal/syncwindow"
)

// GoldenSet is a named fixture stored in the DB as a JSON array.
type GoldenSet struct {
	ID            uuid.UUID       `json:"id"`
	KBID          uuid.UUID       `json:"kb_id"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Content       json.RawMessage `json:"content,omitempty"`
	ContentHash   string          `json:"content_hash"`
	QuestionCount int             `json:"question_count"`
	CreatedAt     time.Time       `json:"created_at"`
	CreatedBy     *uuid.UUID      `json:"created_by,omitempty"`
	// Schedule is manual | daily | weekly (syncwindow constants). Non-manual
	// sets are picked up by the night-window sweeper (internal/syncsched)
	// and run through the in-app eval runner. NextRunAt is the stamped slot;
	// nil on a non-manual set means "not yet stamped".
	Schedule  string     `json:"schedule"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
}

// GoldenSetStore — CRUD on eval_golden_sets.
type GoldenSetStore struct {
	pool *pgxpool.Pool
}

// NewGoldenSetStore returns a GoldenSetStore backed by pool.
func NewGoldenSetStore(pool *pgxpool.Pool) *GoldenSetStore {
	return &GoldenSetStore{pool: pool}
}

// ErrGoldenSetNameTaken is returned by Create when the unique name constraint fails.
var ErrGoldenSetNameTaken = errors.New("golden set name already taken")

// Create inserts a new golden set and returns the generated id and created_at.
func (s *GoldenSetStore) Create(ctx context.Context, g GoldenSet) (uuid.UUID, time.Time, error) {
	const q = `
		INSERT INTO eval_golden_sets
			(name, description, content, content_hash, question_count, created_by, kb_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`
	var id uuid.UUID
	var createdAt time.Time
	var desc *string
	if g.Description != "" {
		d := g.Description
		desc = &d
	}
	err := s.pool.QueryRow(ctx, q, g.Name, desc, g.Content, g.ContentHash, g.QuestionCount, g.CreatedBy, g.KBID).Scan(&id, &createdAt)
	if err != nil {
		if pgxutil.IsUniqueViolation(err) {
			return uuid.Nil, time.Time{}, ErrGoldenSetNameTaken
		}
		return uuid.Nil, time.Time{}, err
	}
	return id, createdAt, nil
}

// Get returns one golden set with full content.
// Returns store.ErrNotFound if the id is not found.
func (s *GoldenSetStore) Get(ctx context.Context, id uuid.UUID) (*GoldenSet, error) {
	const q = `
		SELECT id, kb_id, name, description, content, content_hash, question_count, created_at, created_by,
		       schedule, next_run_at
		FROM eval_golden_sets
		WHERE id = $1`
	var g GoldenSet
	var desc *string
	// kb_id may be NULL for legacy "orphan" rows when the strictly-per-KB
	// enforcement has been deferred (see migrate.EnsureGoldenSetKBID). Scan
	// through a NullUUID so such a row doesn't crash the fetch; KBID stays the
	// zero UUID, which never matches a real KB in the ownership checks.
	var kb uuid.NullUUID
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&g.ID, &kb, &g.Name, &desc, &g.Content, &g.ContentHash, &g.QuestionCount, &g.CreatedAt, &g.CreatedBy,
		&g.Schedule, &g.NextRunAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	if kb.Valid {
		g.KBID = kb.UUID
	}
	if desc != nil {
		g.Description = *desc
	}
	return &g, nil
}

// List returns all golden sets ordered by created_at DESC, or an empty
// (non-nil) slice when there are none — matches the pgxutil nil-row
// convention so JSON encoders emit `[]` rather than `null`. The content
// column is intentionally omitted; use Get to fetch full content.
func (s *GoldenSetStore) List(ctx context.Context) ([]GoldenSet, error) {
	const q = `
		SELECT id, name, description, content_hash, question_count, created_at, created_by,
		       schedule, next_run_at
		FROM eval_golden_sets
		ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return []GoldenSet{}, err
	}
	defer rows.Close()
	out := []GoldenSet{}
	for rows.Next() {
		var g GoldenSet
		var desc *string
		if err := rows.Scan(&g.ID, &g.Name, &desc, &g.ContentHash, &g.QuestionCount, &g.CreatedAt, &g.CreatedBy,
			&g.Schedule, &g.NextRunAt); err != nil {
			return []GoldenSet{}, err
		}
		if desc != nil {
			g.Description = *desc
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListByKB returns golden sets owned by kbID, newest first (content omitted).
func (s *GoldenSetStore) ListByKB(ctx context.Context, kbID uuid.UUID) ([]GoldenSet, error) {
	const q = `
		SELECT id, kb_id, name, description, content_hash, question_count, created_at, created_by,
		       schedule, next_run_at
		FROM eval_golden_sets
		WHERE kb_id = $1
		ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q, kbID)
	if err != nil {
		return []GoldenSet{}, err
	}
	defer rows.Close()
	out := []GoldenSet{}
	for rows.Next() {
		var g GoldenSet
		var desc *string
		if err := rows.Scan(&g.ID, &g.KBID, &g.Name, &desc, &g.ContentHash, &g.QuestionCount, &g.CreatedAt, &g.CreatedBy,
			&g.Schedule, &g.NextRunAt); err != nil {
			return []GoldenSet{}, err
		}
		if desc != nil {
			g.Description = *desc
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Delete hard-deletes a golden set by id. eval_runs.golden_set_id is SET NULL
// via the FK ON DELETE SET NULL constraint. Returns (true, nil) on success,
// (false, nil) when the id is not found.
func (s *GoldenSetStore) Delete(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM eval_golden_sets WHERE id = $1`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---------------------------------------------------------------------------
// Sweeper contract (internal/syncsched)
// ---------------------------------------------------------------------------

// Kind identifies this store to the night-window sweeper.
func (s *GoldenSetStore) Kind() string { return "eval" }

type dueGoldenSetRow struct {
	ID       string `db:"id"`
	Schedule string `db:"schedule"`
}

func toDue(rows []dueGoldenSetRow) []syncwindow.DueSource {
	out := make([]syncwindow.DueSource, 0, len(rows))
	for _, r := range rows {
		out = append(out, syncwindow.DueSource{ID: r.ID, Schedule: r.Schedule})
	}
	return out
}

// ListDue returns non-manual sets whose stamped slot is at or before now.
func (s *GoldenSetStore) ListDue(ctx context.Context, now time.Time) ([]syncwindow.DueSource, error) {
	const q = `
		SELECT id::text AS id, schedule FROM eval_golden_sets
		 WHERE schedule <> 'manual' AND next_run_at IS NOT NULL AND next_run_at <= $1`
	rows, err := pgxutil.QueryRows[dueGoldenSetRow](ctx, s.pool, q, now)
	if err != nil {
		return nil, fmt.Errorf("ListDue(eval): %w", err)
	}
	return toDue(rows), nil
}

// ListUnscheduled returns non-manual sets with no stamped slot yet.
func (s *GoldenSetStore) ListUnscheduled(ctx context.Context) ([]syncwindow.DueSource, error) {
	const q = `
		SELECT id::text AS id, schedule FROM eval_golden_sets
		 WHERE schedule <> 'manual' AND next_run_at IS NULL`
	rows, err := pgxutil.QueryRows[dueGoldenSetRow](ctx, s.pool, q)
	if err != nil {
		return nil, fmt.Errorf("ListUnscheduled(eval): %w", err)
	}
	return toDue(rows), nil
}

// MarkScheduled stamps the next slot.
func (s *GoldenSetStore) MarkScheduled(ctx context.Context, id string, next time.Time) error {
	if _, err := s.pool.Exec(ctx, `UPDATE eval_golden_sets SET next_run_at = $2 WHERE id = $1`, id, next); err != nil {
		return fmt.Errorf("MarkScheduled(eval, %s): %w", id, err)
	}
	return nil
}

// ErrInvalidSchedule is returned by SetSchedule for values outside
// manual|daily|weekly.
var ErrInvalidSchedule = errors.New("invalid schedule")

// SetSchedule changes a set's schedule and clears the stamped slot so the
// sweeper re-slots it (a daily→weekly change must not keep tomorrow's slot;
// a →manual change must never fire again). Returns false when id is unknown.
func (s *GoldenSetStore) SetSchedule(ctx context.Context, id uuid.UUID, schedule string) (bool, error) {
	if !syncwindow.Valid(schedule) {
		return false, fmt.Errorf("%w: %q", ErrInvalidSchedule, schedule)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE eval_golden_sets SET schedule = $2, next_run_at = NULL WHERE id = $1`, id, schedule)
	if err != nil {
		return false, fmt.Errorf("SetSchedule(%s): %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}
