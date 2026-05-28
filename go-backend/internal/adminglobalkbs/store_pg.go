package adminglobalkbs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of the adminglobalkbs Store interface.
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
// Scan structs & helpers
// ---------------------------------------------------------------------------

// globalKBDBRow is an internal struct with db tags for scanning global KB rows.
type globalKBDBRow struct {
	ID             string          `db:"id"`
	Name           string          `db:"name"`
	Description    *string         `db:"description"`
	Language       string          `db:"language"`
	IsPublished    bool            `db:"is_published"`
	SystemPrompt   *string         `db:"system_prompt"`
	HeaderText     *string         `db:"header_text"`
	ExamplePrompts *string         `db:"example_prompts"`
	AIConfigID     *string         `db:"ai_config_id"`
	ChatModel      *string         `db:"chat_model"`
	EmbeddingModel *string         `db:"embedding_model"`
	RerankModel    *string         `db:"rerank_model"`
	TTSModel       *string         `db:"tts_model"`
	SttModel       *string         `db:"stt_model"`
	StudioConfig   json.RawMessage `db:"studio_config"`
	CreatedAt      time.Time       `db:"created_at"`
}

// globalKBSelectCols is the column list every GlobalKBRow query selects.
// Kept as a const so List / Create / Update / no-op SELECT all stay in sync.
const globalKBSelectCols = `id, name, description, language, is_published,
	system_prompt, header_text, example_prompts,
	ai_config_id, chat_model, embedding_model, rerank_model, tts_model, stt_model,
	studio_config, created_at`

func toGlobalKBRow(r globalKBDBRow) GlobalKBRow {
	return GlobalKBRow{
		ID:             r.ID,
		Name:           r.Name,
		Description:    r.Description,
		Language:       r.Language,
		IsPublished:    r.IsPublished,
		SystemPrompt:   r.SystemPrompt,
		HeaderText:     r.HeaderText,
		ExamplePrompts: r.ExamplePrompts,
		AIConfigID:     r.AIConfigID,
		ChatModel:      r.ChatModel,
		EmbeddingModel: r.EmbeddingModel,
		RerankModel:    r.RerankModel,
		TTSModel:       r.TTSModel,
		SttModel:       r.SttModel,
		StudioConfig:   r.StudioConfig,
		CreatedAt:      r.CreatedAt,
	}
}

// globalKBEditorDBRow is an internal struct for scanning editor rows joined with users.
type globalKBEditorDBRow struct {
	ID        string    `db:"id"`
	Username  string    `db:"username"`
	FirstName *string   `db:"first_name"`
	LastName  *string   `db:"last_name"`
	CreatedAt time.Time `db:"created_at"`
}

// ---------------------------------------------------------------------------
// CRUD methods
// ---------------------------------------------------------------------------

// ListGlobalKBs returns all knowledge bases with is_global = true ordered by created_at DESC.
func (s *PGStore) ListGlobalKBs(ctx context.Context) ([]GlobalKBRow, error) {
	const sql = `
		SELECT ` + globalKBSelectCols + `
		FROM knowledge_bases
		WHERE is_global = true
		ORDER BY created_at DESC`

	rows, err := pgxutil.QueryRows[globalKBDBRow](ctx, s.pool, sql)
	if err != nil {
		return nil, err
	}

	result := make([]GlobalKBRow, len(rows))
	for i, r := range rows {
		result[i] = toGlobalKBRow(r)
	}
	return result, nil
}

// CreateGlobalKB inserts a new global knowledge base (is_global=true, user_id=NULL)
// and returns the created row.
func (s *PGStore) CreateGlobalKB(ctx context.Context, data GlobalKBCreate) (*GlobalKBRow, error) {
	const sql = `
		INSERT INTO knowledge_bases (name, description, language, is_global, user_id)
		VALUES ($1, $2, $3, true, NULL)
		RETURNING ` + globalKBSelectCols

	rows, err := pgxutil.QueryRows[globalKBDBRow](ctx, s.pool, sql, data.Name, data.Description, data.Language)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("create global KB: no row returned")
	}
	r := toGlobalKBRow(rows[0])
	return &r, nil
}

// UpdateGlobalKB applies the non-nil fields in data to the global KB with
// the given ID and returns the updated row. Returns nil if no matching
// is_global row exists. Supports explicit nulls via data.NullFields so
// admins can clear header_text / example_prompts / etc. from the UI.
func (s *PGStore) UpdateGlobalKB(ctx context.Context, id string, data GlobalKBUpdate) (*GlobalKBRow, error) {
	var setClauses []string
	var args []any
	param := 2 // $1 is reserved for WHERE id = $1

	// setVal: non-null value update.
	setVal := func(col string, val any) {
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, param))
		args = append(args, val)
		param++
	}
	// setOrNull: nullable pointer field that tolerates explicit JSON nulls.
	setOrNull := func(col, jsonKey string, val *string) {
		switch {
		case val != nil:
			setVal(col, *val)
		case data.NullFields[jsonKey]:
			setClauses = append(setClauses, col+" = NULL")
		}
	}

	if data.Name != nil {
		setVal("name", *data.Name)
	}
	if data.Description != nil || data.NullFields["description"] {
		setOrNull("description", "description", data.Description)
	}
	if data.Language != nil {
		setVal("language", *data.Language)
	}
	if data.IsPublished != nil {
		setVal("is_published", *data.IsPublished)
	}
	if data.SystemPrompt != nil || data.NullFields["systemPrompt"] {
		setOrNull("system_prompt", "systemPrompt", data.SystemPrompt)
	}
	if data.HeaderText != nil || data.NullFields["headerText"] {
		setOrNull("header_text", "headerText", data.HeaderText)
	}
	if data.ExamplePrompts != nil || data.NullFields["examplePrompts"] {
		setOrNull("example_prompts", "examplePrompts", data.ExamplePrompts)
	}
	if data.AIConfigID != nil || data.NullFields["aiConfigId"] {
		setOrNull("ai_config_id", "aiConfigId", data.AIConfigID)
	}
	if data.ChatModel != nil || data.NullFields["chatModel"] {
		setOrNull("chat_model", "chatModel", data.ChatModel)
	}
	if data.EmbeddingModel != nil || data.NullFields["embeddingModel"] {
		setOrNull("embedding_model", "embeddingModel", data.EmbeddingModel)
	}
	if data.RerankModel != nil || data.NullFields["rerankModel"] {
		setOrNull("rerank_model", "rerankModel", data.RerankModel)
	}
	if data.TTSModel != nil || data.NullFields["ttsModel"] {
		setOrNull("tts_model", "ttsModel", data.TTSModel)
	}
	if data.SttModel != nil || data.NullFields["sttModel"] {
		setOrNull("stt_model", "sttModel", data.SttModel)
	}
	if data.StudioConfig != nil {
		b, err := json.Marshal(data.StudioConfig)
		if err != nil {
			return nil, fmt.Errorf("UpdateGlobalKB: marshal studio_config: %w", err)
		}
		setVal("studio_config", b)
	}

	if len(setClauses) == 0 {
		// Nothing to update — return the current row unchanged.
		rows, err := pgxutil.QueryRows[globalKBDBRow](ctx, s.pool,
			`SELECT `+globalKBSelectCols+`
			 FROM knowledge_bases WHERE id = $1 AND is_global = true`, id)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, nil
		}
		r := toGlobalKBRow(rows[0])
		return &r, nil
	}

	updateSQL := fmt.Sprintf(
		`UPDATE knowledge_bases SET %s WHERE id = $1 AND is_global = true
		 RETURNING `+globalKBSelectCols,
		strings.Join(setClauses, ", "),
	)
	allArgs := append([]any{id}, args...)

	rows, err := pgxutil.QueryRows[globalKBDBRow](ctx, s.pool, updateSQL, allArgs...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toGlobalKBRow(rows[0])
	return &r, nil
}

// ListGlobalKBEditors returns all editors for a global KB, ordered by joined date DESC.
func (s *PGStore) ListGlobalKBEditors(ctx context.Context, kbID string) ([]GlobalKBEditorRow, error) {
	const sql = `
		SELECT u.id, u.username, u.first_name, u.last_name, gke.created_at
		FROM global_kb_editors gke
		INNER JOIN users u ON gke.user_id = u.id
		WHERE gke.kb_id = $1
		ORDER BY gke.created_at DESC`

	rows, err := pgxutil.QueryRows[globalKBEditorDBRow](ctx, s.pool, sql, kbID)
	if err != nil {
		return nil, err
	}

	result := make([]GlobalKBEditorRow, len(rows))
	for i, r := range rows {
		result[i] = GlobalKBEditorRow{
			ID:        r.ID,
			Username:  r.Username,
			FirstName: r.FirstName,
			LastName:  r.LastName,
			CreatedAt: r.CreatedAt,
		}
	}
	return result, nil
}

// AddGlobalKBEditor adds a user as an editor of a global KB.
// If the (kb_id, user_id) pair already exists, it is a no-op (ON CONFLICT DO NOTHING).
func (s *PGStore) AddGlobalKBEditor(ctx context.Context, kbID, userID string) error {
	const sql = `
		INSERT INTO global_kb_editors (kb_id, user_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING`
	_, err := s.pool.Exec(ctx, sql, kbID, userID)
	return err
}

// RemoveGlobalKBEditor removes a user from the editors of a global KB.
func (s *PGStore) RemoveGlobalKBEditor(ctx context.Context, kbID, userID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM global_kb_editors WHERE kb_id = $1 AND user_id = $2`, kbID, userID)
	return err
}

// ---------------------------------------------------------------------------
// LogAuditAction (required by adminglobalkbs.Store interface)
// ---------------------------------------------------------------------------

// LogAuditAction inserts a row into admin_audit_logs.
func (s *PGStore) LogAuditAction(ctx context.Context, operatorID, action, targetType, targetID string, diff any) error {
	var diffJSON []byte
	if diff != nil {
		var err error
		diffJSON, err = json.Marshal(diff)
		if err != nil {
			return fmt.Errorf("LogAuditAction marshal diff: %w", err)
		}
	}

	const sql = `
		INSERT INTO admin_audit_logs (operator_id, action, target_type, target_id, diff)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := s.pool.Exec(ctx, sql, operatorID, action, targetType, targetID, diffJSON)
	return err
}
