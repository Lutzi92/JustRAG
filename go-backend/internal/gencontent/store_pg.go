package gencontent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of gencontent.Store.
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
// Internal scan structs
// ---------------------------------------------------------------------------

// genContentDBRow is an internal struct with db tags for scanning generated_content rows.
type genContentDBRow struct {
	ID        string          `db:"id"`
	KbID      string          `db:"kb_id"`
	UserID    string          `db:"user_id"`
	Type      string          `db:"type"`
	Title     string          `db:"title"`
	Content   json.RawMessage `db:"content"`
	CreatedAt time.Time       `db:"created_at"`
}

const genContentColumns = `id, kb_id, user_id, type, title, content, created_at`

// toGenContentRow converts an internal genContentDBRow to the exported GenContentRow.
// The raw JSON content is unmarshalled into an any value so callers can inspect it as a map.
func toGenContentRow(r genContentDBRow) (*GenContentRow, error) {
	var content any
	if len(r.Content) > 0 {
		if err := json.Unmarshal(r.Content, &content); err != nil {
			return nil, fmt.Errorf("toGenContentRow unmarshal content: %w", err)
		}
	}
	return &GenContentRow{
		ID:        r.ID,
		KbID:      r.KbID,
		UserID:    r.UserID,
		Type:      r.Type,
		Title:     r.Title,
		Content:   content,
		CreatedAt: r.CreatedAt,
	}, nil
}

// ---------------------------------------------------------------------------
// Store implementation
// ---------------------------------------------------------------------------

// GetGenContentByID returns the generated_content row with the given ID, or nil if not found.
func (s *PGStore) GetGenContentByID(ctx context.Context, id string) (*GenContentRow, error) {
	const sql = `SELECT ` + genContentColumns + ` FROM generated_content WHERE id = $1`

	rows, err := pgxutil.QueryRows[genContentDBRow](ctx, s.pool, sql, id)
	if err != nil {
		return nil, fmt.Errorf("GetGenContentByID: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	row, err := toGenContentRow(rows[0])
	if err != nil {
		return nil, fmt.Errorf("GetGenContentByID: %w", err)
	}
	return row, nil
}

// UpdateGenContent replaces the content JSONB field for the record with the given ID
// and returns the updated row. Returns nil if no row with that ID exists.
func (s *PGStore) UpdateGenContent(ctx context.Context, id string, content any) (*GenContentRow, error) {
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("UpdateGenContent marshal content: %w", err)
	}

	const sql = `
		UPDATE generated_content SET content = $1::jsonb WHERE id = $2
		RETURNING ` + genContentColumns

	rows, err := pgxutil.QueryRows[genContentDBRow](ctx, s.pool, sql, contentJSON, id)
	if err != nil {
		return nil, fmt.Errorf("UpdateGenContent: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	row, err := toGenContentRow(rows[0])
	if err != nil {
		return nil, fmt.Errorf("UpdateGenContent: %w", err)
	}
	return row, nil
}

// DeleteGenContent removes the generated_content row with the given ID.
// Associated records cascade-delete via DB FK constraints.
func (s *PGStore) DeleteGenContent(ctx context.Context, id string) error {
	const sql = `DELETE FROM generated_content WHERE id = $1`
	_, err := s.pool.Exec(ctx, sql, id)
	if err != nil {
		return fmt.Errorf("DeleteGenContent: %w", err)
	}
	return nil
}

// CreateGeneratedContent inserts a new row into the generated_content table and
// returns the created record. content is serialised to JSONB.
func (s *PGStore) CreateGeneratedContent(ctx context.Context, kbID, userID, contentType, title string, content any) (*GenContentRow, error) {
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("CreateGeneratedContent marshal: %w", err)
	}

	const insertSQL = `
		INSERT INTO generated_content (kb_id, user_id, type, title, content)
		VALUES ($1, $2, $3, $4, $5::jsonb)
		RETURNING ` + genContentColumns

	rows, err := pgxutil.QueryRows[genContentDBRow](ctx, s.pool, insertSQL, kbID, userID, contentType, title, contentJSON)
	if err != nil {
		return nil, fmt.Errorf("CreateGeneratedContent: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CreateGeneratedContent: no row returned")
	}
	row, err := toGenContentRow(rows[0])
	if err != nil {
		return nil, fmt.Errorf("CreateGeneratedContent: %w", err)
	}
	return row, nil
}

// ---------------------------------------------------------------------------
// ListGeneratedContent — used by the inline handler in main.go
// ---------------------------------------------------------------------------

// GeneratedContentItem is returned to API consumers by ListGeneratedContent.
type GeneratedContentItem struct {
	ID        string    `json:"id"`
	KbID      string    `json:"kbId"`
	UserID    string    `json:"userId"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Content   any       `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListGeneratedContent returns generated content for a KB filtered by user.
// Uses genContentDBRow (json.RawMessage) for proper JSONB decoding, same as
// GetGenContentByID.
func (s *PGStore) ListGeneratedContent(ctx context.Context, kbID, userID string) ([]GeneratedContentItem, error) {
	rows, err := pgxutil.QueryRows[genContentDBRow](ctx, s.pool,
		`SELECT id, kb_id, user_id, type, title, content, created_at
		 FROM generated_content
		 WHERE kb_id = $1 AND user_id = $2
		 ORDER BY created_at DESC`, kbID, userID)
	if err != nil {
		return nil, err
	}
	items := make([]GeneratedContentItem, len(rows))
	for i, r := range rows {
		row, err := toGenContentRow(r)
		if err != nil {
			return nil, err
		}
		items[i] = GeneratedContentItem{
			ID: row.ID, KbID: row.KbID, UserID: row.UserID,
			Type: row.Type, Title: row.Title, Content: row.Content,
			CreatedAt: row.CreatedAt,
		}
	}
	return items, nil
}
