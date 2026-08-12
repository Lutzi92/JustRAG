package adminglobalkbs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kbmembers"
	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of the adminglobalkbs Store interface.
type PGStore struct {
	pool *pgxpool.Pool
	// members backs the editor endpoints. Editors are kb_members rows with
	// role='admin'; see the "Editors" section below for why global_kb_editors
	// is no longer consulted.
	members kbmembers.Store
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, members: kbmembers.NewStore(pool)}
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
	return GlobalKBRow(r)
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
	// base=1: $1 is reserved for WHERE id = $1, so the first SET assignment
	// binds $2.
	b := pgxutil.NewClauseBuilder(1)

	// setVal: non-null value update.
	setVal := func(col string, val any) {
		b.Add(col+" = $%d", val)
	}
	// setOrNull: nullable pointer field that tolerates explicit JSON nulls.
	setOrNull := func(col, jsonKey string, val *string) {
		switch {
		case val != nil:
			setVal(col, *val)
		case data.NullFields[jsonKey]:
			b.AddRaw(col + " = NULL")
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
		raw, err := json.Marshal(data.StudioConfig)
		if err != nil {
			return nil, fmt.Errorf("UpdateGlobalKB: marshal studio_config: %w", err)
		}
		setVal("studio_config", raw)
	}

	if b.Len() == 0 {
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
		strings.Join(b.Clauses(), ", "),
	)
	allArgs := append([]any{id}, b.Args()...)

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

// ---------------------------------------------------------------------------
// Editors — backed by kb_members, not global_kb_editors
// ---------------------------------------------------------------------------
//
// A "global KB editor" is exactly a kb_members row with role='admin' on a
// global KB: migration 0064 backfilled global_kb_editors that way, and
// kbaccess.EffectiveRole resolves access from kb_members alone. The three
// operations below therefore delegate to kbmembers.Store — which also carries
// the owner invariants in SQL, so this surface cannot demote or delete a KB
// owner by accident.
//
// The queries that used to read and write global_kb_editors here were the last
// place that table took part in an access decision. It survives as a table only
// for expand/contract and is dropped in a release after Phase 2 of the KB role
// model (visibility enum, system user, subscriptions, categories, catalogue);
// see docs/superpowers/specs/2026-08-12-kb-rollen-und-sichtbarkeit-design.md.
// Nothing may consult it for access again. internal/cascade still DELETEs from
// it, which is cleanup, not an access decision.

// ListGlobalKBEditors returns the KB's role='admin' members — the four-role
// successor to the old global_kb_editors listing. Ordered by username (the
// order kbmembers.ListMembers produces within one role) rather than the old
// joined-date DESC.
func (s *PGStore) ListGlobalKBEditors(ctx context.Context, kbID string) ([]GlobalKBEditorRow, error) {
	members, err := s.members.ListMembers(ctx, kbID)
	if err != nil {
		return nil, err
	}

	result := make([]GlobalKBEditorRow, 0, len(members))
	for _, m := range members {
		if m.Role != kbaccess.RoleAdmin {
			continue
		}
		// GlobalKBEditorRow.ID is the *user* id — the wire shape predates
		// kb_members and is kept so the admin UI needs no contract change.
		result = append(result, GlobalKBEditorRow{
			ID:        m.UserID,
			Username:  m.Username,
			FirstName: m.FirstName,
			LastName:  m.LastName,
			CreatedAt: m.CreatedAt,
		})
	}
	return result, nil
}

// AddGlobalKBEditor grants a user KB-admin on a global KB. grantedBy is the
// acting operator, recorded in kb_members.created_by; it may be empty.
// Returns kbmembers.ErrOwnerImmutable if the target already owns the KB.
func (s *PGStore) AddGlobalKBEditor(ctx context.Context, kbID, userID, grantedBy string) error {
	return s.members.SetRole(ctx, kbID, userID, kbaccess.RoleAdmin, grantedBy)
}

// RemoveGlobalKBEditor revokes a user's KB-admin role on a global KB. Returns
// kbmembers.ErrNotFound when they had no membership at all and
// kbmembers.ErrOwnerImmutable when the row is the owner's.
func (s *PGStore) RemoveGlobalKBEditor(ctx context.Context, kbID, userID string) error {
	return s.members.RemoveMember(ctx, kbID, userID)
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
