package eval

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type GenCounts struct {
	Lookup      int `json:"lookup"`
	Complex     int `json:"complex"`
	Enumeration int `json:"enumeration"`
	MultiHop    int `json:"multihop"`
}

type GenJobParams struct {
	Lang     string    `json:"lang"`
	Name     string    `json:"name"`
	MaxFiles int       `json:"max_files"`
	Model    string    `json:"model"`
	Counts   GenCounts `json:"counts"`
}

func (p GenJobParams) toJSON() (json.RawMessage, error) {
	b, err := json.Marshal(p)
	return json.RawMessage(b), err
}

func parseGenJobParams(raw json.RawMessage) (GenJobParams, error) {
	var p GenJobParams
	err := json.Unmarshal(raw, &p)
	return p, err
}

type GenJob struct {
	ID          uuid.UUID       `json:"id"`
	KBID        uuid.UUID       `json:"kb_id"`
	Status      string          `json:"status"`
	Params      json.RawMessage `json:"params"`
	GoldenSetID *uuid.UUID      `json:"golden_set_id,omitempty"`
	Error       string          `json:"error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	CreatedBy   *uuid.UUID      `json:"created_by,omitempty"`
}

type GenJobStore struct{ db *pgxpool.Pool }

func NewGenJobStore(db *pgxpool.Pool) *GenJobStore { return &GenJobStore{db: db} }

func (s *GenJobStore) Create(ctx context.Context, kbID uuid.UUID, params GenJobParams, createdBy *uuid.UUID) (uuid.UUID, error) {
	raw, err := params.toJSON()
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = s.db.QueryRow(ctx,
		`INSERT INTO eval_golden_set_jobs (kb_id, status, params, created_by)
		 VALUES ($1, 'queued', $2, $3) RETURNING id`,
		kbID, raw, createdBy).Scan(&id)
	return id, err
}

func (s *GenJobStore) MarkRunning(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE eval_golden_set_jobs SET status='running' WHERE id=$1`, id)
	return err
}

func (s *GenJobStore) MarkCompleted(ctx context.Context, id, goldenSetID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE eval_golden_set_jobs SET status='completed', golden_set_id=$2, error=NULL WHERE id=$1`,
		id, goldenSetID)
	return err
}

func (s *GenJobStore) MarkFailed(ctx context.Context, id uuid.UUID, msg string) error {
	_, err := s.db.Exec(ctx, `UPDATE eval_golden_set_jobs SET status='failed', error=$2 WHERE id=$1`, id, msg)
	return err
}

func (s *GenJobStore) Get(ctx context.Context, id uuid.UUID) (*GenJob, error) {
	var j GenJob
	err := s.db.QueryRow(ctx,
		`SELECT id, kb_id, status, params, golden_set_id, COALESCE(error,''), created_at, created_by
		 FROM eval_golden_set_jobs WHERE id=$1`, id).
		Scan(&j.ID, &j.KBID, &j.Status, &j.Params, &j.GoldenSetID, &j.Error, &j.CreatedAt, &j.CreatedBy)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *GenJobStore) List(ctx context.Context, limit int) ([]GenJob, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx,
		`SELECT id, kb_id, status, params, golden_set_id, COALESCE(error,''), created_at, created_by
		 FROM eval_golden_set_jobs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GenJob{}
	for rows.Next() {
		var j GenJob
		if err := rows.Scan(&j.ID, &j.KBID, &j.Status, &j.Params, &j.GoldenSetID, &j.Error, &j.CreatedAt, &j.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
