package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/store"
)

// PGStore is a PostgreSQL-backed implementation of kb.Store, kb.UpdateStore,
// and kb.ShareStore.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertions.
var (
	_ Store       = (*PGStore)(nil)
	_ UpdateStore = (*PGStore)(nil)
	_ ShareStore  = (*PGStore)(nil)
)

// ---------------------------------------------------------------------------
// Internal scan structs
// ---------------------------------------------------------------------------

// kbFullRow is an internal struct with db tags for scanning full knowledge_bases rows.
type kbFullRow struct {
	ID             string          `db:"id"`
	Name           string          `db:"name"`
	UserID         *string         `db:"user_id"`
	Description    *string         `db:"description"`
	IsGlobal       bool            `db:"is_global"`
	IsPublished    bool            `db:"is_published"`
	Language       string          `db:"language"`
	AIConfigID     *string         `db:"ai_config_id"`
	ChatModel      *string         `db:"chat_model"`
	EmbeddingModel *string         `db:"embedding_model"`
	RerankModel    *string         `db:"rerank_model"`
	TTSModel       *string         `db:"tts_model"`
	SttModel       *string         `db:"stt_model"`
	SystemPrompt   *string         `db:"system_prompt"`
	HeaderText     *string         `db:"header_text"`
	ExamplePrompts *string         `db:"example_prompts"`
	StudioConfig   json.RawMessage `db:"studio_config"`
	ChunkSize      *int            `db:"chunk_size"`
	ChunkOverlap   *int            `db:"chunk_overlap"`
	CreatedAt      time.Time       `db:"created_at"`
	// Optional owner attribution — only populated by queries that LEFT JOIN
	// the users table (e.g. ListKnowledgeBases). Other queries leave these nil.
	OwnerFirstName *string `db:"owner_first_name"`
	OwnerLastName  *string `db:"owner_last_name"`
	OwnerUsername  *string `db:"owner_username"`
}

const kbSelectCols = `kb.id, kb.name, kb.user_id, kb.description, kb.is_global, kb.is_published,
       kb.language, kb.ai_config_id, kb.chat_model, kb.embedding_model, kb.rerank_model, kb.tts_model, kb.stt_model,
       kb.system_prompt, kb.header_text, kb.example_prompts, kb.studio_config,
       kb.chunk_size, kb.chunk_overlap, kb.created_at`

const kbSelectColsNoAlias = `id, name, user_id, description, is_global, is_published,
       language, ai_config_id, chat_model, embedding_model, rerank_model, tts_model, stt_model,
       system_prompt, header_text, example_prompts, studio_config, chunk_size, chunk_overlap, created_at,
       NULL::text AS owner_first_name, NULL::text AS owner_last_name, NULL::text AS owner_username`

func toKBRow(r kbFullRow) KBRow {
	return KBRow{
		ID:             r.ID,
		Name:           r.Name,
		UserID:         r.UserID,
		Description:    r.Description,
		IsGlobal:       r.IsGlobal,
		IsPublished:    r.IsPublished,
		Language:       r.Language,
		AIConfigID:     r.AIConfigID,
		ChatModel:      r.ChatModel,
		EmbeddingModel: r.EmbeddingModel,
		RerankModel:    r.RerankModel,
		TTSModel:       r.TTSModel,
		SttModel:       r.SttModel,
		SystemPrompt:   r.SystemPrompt,
		HeaderText:     r.HeaderText,
		ExamplePrompts: r.ExamplePrompts,
		StudioConfig:   r.StudioConfig,
		ChunkSize:      r.ChunkSize,
		ChunkOverlap:   r.ChunkOverlap,
		CreatedAt:      r.CreatedAt,
		OwnerFirstName: r.OwnerFirstName,
		OwnerLastName:  r.OwnerLastName,
		OwnerUsername:  r.OwnerUsername,
	}
}

// ---------------------------------------------------------------------------
// kb.Store implementation
// ---------------------------------------------------------------------------

// listKnowledgeBasesMaxLimit is a defense-in-depth cap on the per-page row
// count surfaced to callers of ListKnowledgeBases. All current HTTP callers
// already enforce their own bounds (kb/http.go:100, publicapi:100,
// openaicompat:1000 hardcoded, KB router:100); this guards against a future
// caller passing an unbounded user-supplied limit.
const listKnowledgeBasesMaxLimit = 1000

// ListKnowledgeBases returns the user's personal KBs: those they own plus those
// explicitly shared with them. Global published KBs are intentionally excluded
// — the frontend and API clients fetch those separately via
// ListGlobalKnowledgeBases, and including them here caused them to appear
// twice in the UI.
// Results are ordered by created_at DESC with limit/offset pagination.
// limit is clamped to listKnowledgeBasesMaxLimit and silently coerced to 1
// when ≤ 0; offset < 0 is coerced to 0.
func (s *PGStore) ListKnowledgeBases(ctx context.Context, userID string, limit, offset int) ([]KBRow, error) {
	if limit <= 0 {
		limit = 1
	}
	if limit > listKnowledgeBasesMaxLimit {
		limit = listKnowledgeBasesMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	const sql = `
		SELECT DISTINCT ` + kbSelectCols + `,
		       u.first_name AS owner_first_name,
		       u.last_name  AS owner_last_name,
		       u.username   AS owner_username
		FROM knowledge_bases kb
		LEFT JOIN knowledge_base_shares kbs ON kb.id = kbs.kb_id AND kbs.user_id = $1
		LEFT JOIN users u ON kb.user_id = u.id
		WHERE kb.user_id = $1
		   OR kbs.user_id = $1
		ORDER BY kb.created_at DESC
		LIMIT $2 OFFSET $3`

	rows, err := pgxutil.QueryRows[kbFullRow](ctx, s.pool, sql, userID, limit, offset)
	if err != nil {
		return []KBRow{}, err
	}

	result := make([]KBRow, len(rows))
	for i, r := range rows {
		result[i] = toKBRow(r)
	}
	return result, nil
}

// ListGlobalKnowledgeBases returns global KBs. Admins see all; non-admins see only published.
//
// Results are capped at listKnowledgeBasesMaxLimit rows as a defense-in-depth
// guard: on a deployment with many global KBs an uncapped query would stream
// every row into the process on every call. The admin variant skips the
// is_published filter and is especially exposed.
// userID is part of the interface contract for future per-user filtering of
// global KBs (currently only the published/unpublished split applies); it is
// intentionally unused in this implementation.
func (s *PGStore) ListGlobalKnowledgeBases(ctx context.Context, userID string, isAdmin bool) ([]KBRow, error) {
	var sql string
	if isAdmin {
		sql = `
			SELECT ` + kbSelectColsNoAlias + `
			FROM knowledge_bases
			WHERE is_global = true
			ORDER BY created_at DESC
			LIMIT $1`
	} else {
		sql = `
			SELECT ` + kbSelectColsNoAlias + `
			FROM knowledge_bases
			WHERE is_global = true AND is_published = true
			ORDER BY created_at DESC
			LIMIT $1`
	}

	rows, err := pgxutil.QueryRows[kbFullRow](ctx, s.pool, sql, listKnowledgeBasesMaxLimit)
	if err != nil {
		return []KBRow{}, err
	}

	result := make([]KBRow, len(rows))
	for i, r := range rows {
		result[i] = toKBRow(r)
	}
	return result, nil
}

// CreateKnowledgeBase inserts a new knowledge base owned by userID and returns the created row.
func (s *PGStore) CreateKnowledgeBase(ctx context.Context, name string, description *string, userID string, systemPrompt *string) (*KBRow, error) {
	const sql = `
		INSERT INTO knowledge_bases (name, description, user_id, system_prompt)
		VALUES ($1, $2, $3, $4)
		RETURNING ` + kbSelectColsNoAlias

	row, err := pgxutil.QueryOne[kbFullRow](ctx, s.pool, sql, name, description, userID, systemPrompt)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("CreateKnowledgeBase: no row returned")
	}
	r := toKBRow(*row)
	return &r, nil
}

// ---------------------------------------------------------------------------
// kb.UpdateStore implementation
// ---------------------------------------------------------------------------

// updateBuilder accumulates `column = $N` clauses + the matching bind args in
// lock-step so the manual `param` counter cannot drift. Each addValue call
// appends to both lists and derives `$N` from len(args) directly. addNull
// emits a `column = NULL` clause with no bind argument.
//
// Bind numbering assumption: the caller will pass these args after the id
// (which is $1 in the WHERE clause), so we offset by +1. See callsite in
// UpdateKnowledgeBase.
type updateBuilder struct {
	clauses []string
	args    []any
}

func (b *updateBuilder) addValue(column string, val any) {
	b.args = append(b.args, val)
	b.clauses = append(b.clauses, fmt.Sprintf("%s = $%d", column, len(b.args)+1))
}

func (b *updateBuilder) addNull(column string) {
	b.clauses = append(b.clauses, column+" = NULL")
}

// addOrNull combines the two patterns used across the nullable-string columns:
// set to val when non-nil, or set to NULL when jsonKey is in data.NullFields.
func (b *updateBuilder) addOrNull(column string, val *string, nullFields map[string]bool, jsonKey string) {
	if val != nil {
		b.addValue(column, *val)
	} else if nullFields[jsonKey] {
		b.addNull(column)
	}
}

// UpdateKnowledgeBase applies the non-nil fields in data to the knowledge base
// identified by id. Returns (nil, store.ErrNotFound) when no matching row
// exists — callers match with errors.Is to distinguish "missing" from other
// failure modes.
func (s *PGStore) UpdateKnowledgeBase(ctx context.Context, id string, data KBUpdate) (*KBRow, error) {
	b := &updateBuilder{}

	if data.Name != nil {
		b.addValue("name", *data.Name)
	}
	b.addOrNull("description", data.Description, data.NullFields, "description")
	if data.Language != nil {
		b.addValue("language", *data.Language)
	}
	b.addOrNull("system_prompt", data.SystemPrompt, data.NullFields, "systemPrompt")
	b.addOrNull("header_text", data.HeaderText, data.NullFields, "headerText")
	b.addOrNull("example_prompts", data.ExamplePrompts, data.NullFields, "examplePrompts")
	b.addOrNull("tts_model", data.TTSModel, data.NullFields, "ttsModel")
	b.addOrNull("stt_model", data.SttModel, data.NullFields, "sttModel")
	b.addOrNull("ai_config_id", data.AIConfigID, data.NullFields, "aiConfigId")
	b.addOrNull("chat_model", data.ChatModel, data.NullFields, "chatModel")
	b.addOrNull("embedding_model", data.EmbeddingModel, data.NullFields, "embeddingModel")
	b.addOrNull("rerank_model", data.RerankModel, data.NullFields, "rerankModel")
	if data.ChunkSize != nil {
		b.addValue("chunk_size", *data.ChunkSize)
	}
	if data.ChunkOverlap != nil {
		b.addValue("chunk_overlap", *data.ChunkOverlap)
	}
	if data.IsPublished != nil {
		b.addValue("is_published", *data.IsPublished)
	}
	if data.StudioConfig != nil {
		jsonBytes, err := json.Marshal(data.StudioConfig)
		if err != nil {
			return nil, fmt.Errorf("UpdateKnowledgeBase: marshal studio_config: %w", err)
		}
		b.addValue("studio_config", jsonBytes)
	}

	if len(b.clauses) == 0 {
		// Nothing to update — return the current row unchanged.
		row, err := pgxutil.QueryOne[kbFullRow](ctx, s.pool,
			`SELECT `+kbSelectColsNoAlias+` FROM knowledge_bases WHERE id = $1`, id)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, store.ErrNotFound
		}
		r := toKBRow(*row)
		return &r, nil
	}

	updateSQL := fmt.Sprintf(
		`UPDATE knowledge_bases SET %s WHERE id = $1 RETURNING `+kbSelectColsNoAlias,
		strings.Join(b.clauses, ", "),
	)
	allArgs := append([]any{id}, b.args...)

	row, err := pgxutil.QueryOne[kbFullRow](ctx, s.pool, updateSQL, allArgs...)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, store.ErrNotFound
	}
	r := toKBRow(*row)
	return &r, nil
}

// fileDBRow is an internal struct with db tags for scanning files rows.
// TotalCount is populated only by ListFiles via a COUNT(*) OVER () window
// so the total and the page derive from a single MVCC snapshot.
type fileDBRow struct {
	ID                 string    `db:"id"`
	Name               string    `db:"name"`
	Type               string    `db:"type"`
	Size               *int      `db:"size"`
	Status             string    `db:"status"`
	Progress           int       `db:"progress"`
	Origin             string    `db:"origin"`
	RSSFeedID          *string   `db:"rss_feed_id"`
	ConfluenceSourceID *string   `db:"confluence_source_id"`
	CreatedAt          time.Time `db:"created_at"`
	TotalCount         int       `db:"total_count"`
}

// ListFiles returns a paginated slice of files for kbID, ordered by created_at DESC,
// together with the total count of files in that KB. The count is computed
// via COUNT(*) OVER () in the same statement as the page so both reflect the
// same MVCC snapshot — no race window where the count and the rows can
// disagree under concurrent inserts/deletes.
func (s *PGStore) ListFiles(ctx context.Context, kbID string, limit, offset int) ([]FileRow, int, error) {
	const listSQL = `
		SELECT id, name, type, size, status, progress, origin,
		       rss_feed_id, confluence_source_id, created_at,
		       COUNT(*) OVER ()::int AS total_count
		FROM files
		WHERE kb_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`

	rows, err := pgxutil.QueryRows[fileDBRow](ctx, s.pool, listSQL, kbID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("ListFiles query: %w", err)
	}

	if len(rows) == 0 {
		// Empty page — issue a separate count so an offset past the end
		// (or an empty KB) still surfaces the correct total. Same MVCC
		// concern applies, but the page is empty so no row/count drift
		// can be observed.
		var total int
		if err := s.pool.QueryRow(ctx,
			`SELECT COUNT(*)::int FROM files WHERE kb_id = $1`, kbID,
		).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("ListFiles count: %w", err)
		}
		return []FileRow{}, total, nil
	}

	result := make([]FileRow, len(rows))
	for i, r := range rows {
		result[i] = FileRow{
			ID:                 r.ID,
			Name:               r.Name,
			Type:               r.Type,
			Size:               r.Size,
			Status:             r.Status,
			Progress:           r.Progress,
			Origin:             r.Origin,
			RSSFeedID:          r.RSSFeedID,
			ConfluenceSourceID: r.ConfluenceSourceID,
			CreatedAt:          r.CreatedAt,
		}
	}
	return result, rows[0].TotalCount, nil
}

// ---------------------------------------------------------------------------
// kb.ShareStore implementation
// ---------------------------------------------------------------------------

// kbShareListRow is an internal struct for scanning knowledge_base_shares joined with users.
type kbShareListRow struct {
	ID         string    `db:"id"`
	UserID     string    `db:"user_id"`
	Username   string    `db:"username"`
	Permission string    `db:"permission"`
	CreatedAt  time.Time `db:"created_at"`
}

// ListKBShares returns all share entries for the given KB, joined with users for username.
func (s *PGStore) ListKBShares(ctx context.Context, kbID string) ([]ShareRow, error) {
	const sql = `
		SELECT kbs.id, kbs.user_id, u.username, kbs.permission, kbs.created_at
		FROM knowledge_base_shares kbs
		INNER JOIN users u ON kbs.user_id = u.id
		WHERE kbs.kb_id = $1
		ORDER BY kbs.created_at DESC`

	rows, err := pgxutil.QueryRows[kbShareListRow](ctx, s.pool, sql, kbID)
	if err != nil {
		return []ShareRow{}, err
	}

	result := make([]ShareRow, len(rows))
	for i, r := range rows {
		result[i] = ShareRow{
			ID:         r.ID,
			UserID:     r.UserID,
			Username:   r.Username,
			Permission: r.Permission,
			CreatedAt:  r.CreatedAt,
		}
	}
	return result, nil
}

// addShareRow is an internal struct for scanning the RETURNING clause of AddKBShare.
type addShareRow struct {
	ID         string    `db:"id"`
	UserID     string    `db:"user_id"`
	Permission string    `db:"permission"`
	CreatedAt  time.Time `db:"created_at"`
}

// AddKBShare upserts a share entry for kbID+userID. If the pair already exists,
// the permission is updated. Returns the stored row (username is not populated).
func (s *PGStore) AddKBShare(ctx context.Context, kbID, userID, permission string) (*ShareRow, error) {
	const sql = `
		INSERT INTO knowledge_base_shares (kb_id, user_id, permission)
		VALUES ($1, $2, $3)
		ON CONFLICT (kb_id, user_id) DO UPDATE SET permission = EXCLUDED.permission
		RETURNING id, user_id, permission, created_at`

	row, err := pgxutil.QueryOne[addShareRow](ctx, s.pool, sql, kbID, userID, permission)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("AddKBShare: no row returned")
	}
	return &ShareRow{
		ID:         row.ID,
		UserID:     row.UserID,
		Permission: row.Permission,
		CreatedAt:  row.CreatedAt,
	}, nil
}

// RemoveKBShare deletes the share entry for kbID+userID.
// Wraps store.ErrNotFound with the (kbID, userID) pair if no row was deleted.
func (s *PGStore) RemoveKBShare(ctx context.Context, kbID, userID string) error {
	ct, err := s.pool.Exec(ctx,
		`DELETE FROM knowledge_base_shares WHERE kb_id = $1 AND user_id = $2`, kbID, userID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("kb_share kb=%s user=%s: %w", kbID, userID, store.ErrNotFound)
	}
	return nil
}

// ---------------------------------------------------------------------------
// GetKBChunkConfig — used by worker.KBChunkConfigStore
// ---------------------------------------------------------------------------

// GetKBChunkConfig returns the chunk_size and chunk_overlap for a KB. Returns
// 0, 0, nil when the KB row is absent or the columns are NULL — callers
// fall back to the global default in either case, so a missing KB is not
// distinguished from "no override set" here.
func (s *PGStore) GetKBChunkConfig(ctx context.Context, kbID string) (chunkSize, chunkOverlap int, err error) {
	var cs, co *int
	err = s.pool.QueryRow(ctx, `SELECT chunk_size, chunk_overlap FROM knowledge_bases WHERE id = $1`, kbID).Scan(&cs, &co)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	if cs != nil {
		chunkSize = *cs
	}
	if co != nil {
		chunkOverlap = *co
	}
	return chunkSize, chunkOverlap, nil
}

// ---------------------------------------------------------------------------
// GetKBSystemPrompt — used by openaicompat.Store and publicapi.Store
// ---------------------------------------------------------------------------

// GetKBSystemPrompt returns the system_prompt for a KB, or nil if not set.
// A missing KB row maps to (nil, nil) — same as "column is NULL" — because
// callers treat both as "no per-KB override".
func (s *PGStore) GetKBSystemPrompt(ctx context.Context, kbID string) (*string, error) {
	var sp *string
	err := s.pool.QueryRow(ctx, `SELECT system_prompt FROM knowledge_bases WHERE id = $1`, kbID).Scan(&sp)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return sp, nil
}

// ---------------------------------------------------------------------------
// ListAllKBIDs / ListReembedableFilesByKBID — used by reembed-all handler
// ---------------------------------------------------------------------------

// kbIDRow holds a single knowledge base ID.
type kbIDRow struct {
	ID string `db:"id"`
}

// ListAllKBIDs returns the IDs of every knowledge base in the system.
func (s *PGStore) ListAllKBIDs(ctx context.Context) ([]string, error) {
	rows, err := pgxutil.QueryRows[kbIDRow](ctx, s.pool, `SELECT id FROM knowledge_bases ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("ListAllKBIDs: %w", err)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

// FileReembedRow holds the minimal fields needed to enqueue a re-embed job.
type FileReembedRow struct {
	ID          string  `db:"id"`
	KbID        string  `db:"kb_id"`
	StoragePath *string `db:"storage_path"`
	Name        string  `db:"name"`
	Type        string  `db:"type"`
}

// ListReembedableFilesByKBID returns file records eligible for re-embedding:
// 'completed', 'partial', or 'error'. Including 'error' is the key recovery
// path — re-embed deletes old chunks before re-processing, so when a file
// transiently fails (e.g. embedder rate-limit) it ends up with no chunks AND
// status='error'. Without 'error' in this list, re-embed-all skips them on
// every subsequent run and the recall hole becomes permanent.
func (s *PGStore) ListReembedableFilesByKBID(ctx context.Context, kbID string) ([]FileReembedRow, error) {
	const sql = `
		SELECT id, kb_id, storage_path, name, type
		FROM files
		WHERE kb_id = $1
		  AND status IN ('completed', 'partial', 'error')
		ORDER BY created_at`
	return pgxutil.QueryRows[FileReembedRow](ctx, s.pool, sql, kbID)
}

// GetKBInfo returns the name of the knowledge base with the given UUID and
// whether it exists. A non-nil error signals a DB failure distinct from
// "not found" (which returns ("", false, nil)).
func (s *PGStore) GetKBInfo(ctx context.Context, id uuid.UUID) (string, bool, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT name FROM knowledge_bases WHERE id = $1`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return name, true, nil
}
